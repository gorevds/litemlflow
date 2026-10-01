package server_test

// Regression tests for security/correctness fixes in the server middleware
// stack (RBAC target workspace, public paths under a workspace cookie,
// artifact upload cap, OIDC return_to escaping, large-transfer deadlines).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/config"
)

// TestRBACWorkspaceManagementUsesPathTarget: an admin of ws-a must not be
// able to manage ws-b (where they are only a viewer) just by selecting ws-a
// via X-Workspace, and the open-mode default workspace must not grant
// management of a workspace that has members.
func TestRBACWorkspaceManagementUsesPathTarget(t *testing.T) {
	t.Parallel()
	const user, pass = "mallory", "mallorypass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-own", user, "admin")
	adminSetup(t, srv, user, pass, "ws-victim", user, "viewer")

	cases := []struct {
		name, method, path, header string
		body                       any
	}{
		{"escalate via own admin ws", http.MethodPut, "/api/v1/workspaces/ws-victim/members/" + user, "ws-own", map[string]string{"role": "admin"}},
		{"escalate via open default", http.MethodPut, "/api/v1/workspaces/ws-victim/members/" + user, "", map[string]string{"role": "admin"}},
		{"delete via own admin ws", http.MethodDelete, "/api/v1/workspaces/ws-victim", "ws-own", nil},
		{"rename via open default", http.MethodPatch, "/api/v1/workspaces/ws-victim", "", map[string]string{"name": "pwned"}},
	}
	for _, tc := range cases {
		resp, body := rbacDo(t, srv, tc.method, tc.path, tc.header, user, pass, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d: %v", tc.name, resp.StatusCode, body)
		}
	}

	// Viewer of the target may still read it from another workspace context.
	resp, body := rbacDo(t, srv, http.MethodGet, "/api/v1/workspaces/ws-victim", "ws-own", user, pass, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer read of target: want 200, got %d: %v", resp.StatusCode, body)
	}
	// Admin of the target manages it regardless of the header workspace.
	resp, body = rbacDo(t, srv, http.MethodPatch, "/api/v1/workspaces/ws-own", "ws-victim", user, pass,
		map[string]string{"name": "renamed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin of target: want 200, got %d: %v", resp.StatusCode, body)
	}
}

// TestPublicPathsIgnoreWorkspaceMembership: a client whose lmf_workspace
// selects a workspace it is not a member of must still reach login, health,
// and UI endpoints (previously every request 403'd, locking the browser out).
func TestPublicPathsIgnoreWorkspaceMembership(t *testing.T) {
	t.Parallel()
	const user, pass = "nina", "ninapass"
	srv := newRBACServer(t, user, pass)

	// ws-locked ends up with a member that is not `user`.
	adminSetup(t, srv, user, pass, "ws-locked", user, "admin")
	resp, body := rbacDo(t, srv, http.MethodPut, "/api/v1/workspaces/ws-locked/members/someone-else",
		"ws-locked", user, pass, map[string]string{"role": "admin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add other admin: %d %v", resp.StatusCode, body)
	}
	resp, body = rbacDo(t, srv, http.MethodDelete, "/api/v1/workspaces/ws-locked/members/"+user,
		"ws-locked", user, pass, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove self: %d %v", resp.StatusCode, body)
	}

	resp, body = rbacDo(t, srv, http.MethodPost, "/api/v1/auth/login", "ws-locked", "", "",
		map[string]string{"user": user, "pass": pass})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login with non-member workspace selected: want 200, got %d: %v", resp.StatusCode, body)
	}
	resp, _ = rbacDo(t, srv, http.MethodGet, "/healthz", "ws-locked", "", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz with non-member workspace selected: want 200, got %d", resp.StatusCode)
	}
	// Non-public API paths are still denied.
	resp, _ = rbacDo(t, srv, http.MethodGet, "/api/v1/workspaces/current", "ws-locked", user, pass, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("API read as non-member: want 403, got %d", resp.StatusCode)
	}
}

func newArtifactTestRun(t *testing.T, base string) string {
	t.Helper()
	_, raw := mustPostJSON(t, base+"/api/2.0/mlflow/experiments/create", `{"name":"art-limit"}`)
	var ce struct {
		ExperimentID string `json:"experiment_id"`
	}
	_ = json.Unmarshal(raw, &ce)
	_, runRaw := mustPostJSON(t, base+"/api/2.0/mlflow/runs/create",
		fmt.Sprintf(`{"experiment_id":"%s"}`, ce.ExperimentID))
	var cr struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.Unmarshal(runRaw, &cr)
	if cr.Run.Info.RunID == "" {
		t.Fatalf("create run: %s", runRaw)
	}
	return cr.Run.Info.RunID
}

func putArtifact(t *testing.T, base, runID, name string, body io.Reader) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, base+"/api/2.0/mlflow-artifacts/artifacts/"+runID+"/"+name, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestArtifactUploadCappedByMaxArtifactSize: MaxArtifactSize was never
// enforced, so artifact PUTs were unbounded.
func TestArtifactUploadCappedByMaxArtifactSize(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{MaxArtifactSize: 1024})
	runID := newArtifactTestRun(t, ts.URL)

	if code := putArtifact(t, ts.URL, runID, "small.bin", bytes.NewReader(make([]byte, 512))); code != http.StatusOK {
		t.Fatalf("upload under cap: want 200, got %d", code)
	}
	if code := putArtifact(t, ts.URL, runID, "big.bin", bytes.NewReader(make([]byte, 4096))); code < 400 {
		t.Fatalf("upload over cap: want error status, got %d", code)
	}
}

// TestOIDCRedirectEscapesReturnTo: the original query string must survive
// intact inside return_to instead of being split into /oidc/start params.
func TestOIDCRedirectEscapesReturnTo(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{
		Auth:            "oidc",
		OIDCIssuer:      "https://fake.example.com",
		OIDCClientID:    "fake-client",
		OIDCRedirectURL: "http://localhost/cb",
	})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/workspaces?a=1&return_to=//evil.example", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if got := q["return_to"]; len(got) != 1 || got[0] != "/api/v1/workspaces?a=1&return_to=//evil.example" {
		t.Fatalf("return_to not preserved as a single value: %q (Location %s)", got, loc)
	}
}

// slowReader yields n bytes in small chunks with a delay between them.
type slowReader struct {
	remaining int
	delay     time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	n := len(p)
	if n > 64 {
		n = 64
	}
	if n > s.remaining {
		n = s.remaining
	}
	for i := range n {
		p[i] = 'x'
	}
	s.remaining -= n
	return n, nil
}

// TestArtifactUploadOutlivesReadTimeout: the server-wide ReadTimeout covers the
// whole request body, so slow multi-GiB artifact uploads were cut off. The
// artifact path must get an extended deadline.
func TestArtifactUploadOutlivesReadTimeout(t *testing.T) {
	t.Parallel()
	_, srv := newTestServer(t, config.Config{})
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.Config.ReadTimeout = 300 * time.Millisecond
	ts.Config.WriteTimeout = 300 * time.Millisecond
	ts.Start()
	t.Cleanup(ts.Close)

	runID := newArtifactTestRun(t, ts.URL)
	// ~8 chunks × 100ms ≈ 800ms, well past the 300ms ReadTimeout.
	code := putArtifact(t, ts.URL, runID, "slow.bin", &slowReader{remaining: 512, delay: 100 * time.Millisecond})
	if code != http.StatusOK {
		t.Fatalf("slow artifact upload: want 200, got %d", code)
	}
	// Sanity: an ordinary endpoint still honours the short ReadTimeout.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/2.0/mlflow/experiments/create",
		io.MultiReader(strings.NewReader(`{"name":"`), &slowReader{remaining: 512, delay: 100 * time.Millisecond}))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("non-artifact slow body should still hit ReadTimeout")
		}
	}
}

// TestStaleWorkspaceCookieDoesNotBlockPublicPaths: a cookie naming a deleted
// workspace must not 400 the UI shell / login; API paths still reject it.
func TestStaleWorkspaceCookieDoesNotBlockPublicPaths(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	get := func(path string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: "lmf_workspace", Value: "deleted-ws"})
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for _, p := range []string{"/healthz", "/ui/"} {
		if code := get(p); code == http.StatusBadRequest {
			t.Errorf("%s with stale workspace cookie: got 400", p)
		}
	}
	if code := get("/api/v1/workspaces/current"); code != http.StatusBadRequest {
		t.Errorf("API path with unknown workspace: want 400, got %d", code)
	}
}

// TestMetricsUnmatchedPathsCollapse: unmatched routes and junk methods must
// not mint new label values per distinct client-chosen path.
func TestMetricsUnmatchedPathsCollapse(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	for _, p := range []string{"/nope-a/x", "/nope-b/y", "/api/v1/zz-not-a-route"} {
		req, _ := http.NewRequest("BREW", ts.URL+p, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	body := string(raw)
	for _, bad := range []string{"nope-a", "nope-b", "zz-not-a-route", `method="BREW"`} {
		if strings.Contains(body, bad) {
			t.Errorf("metrics leaked unbounded label %q", bad)
		}
	}
	if !strings.Contains(body, `method="OTHER",path="unmatched"`) {
		t.Errorf("expected collapsed OTHER/unmatched series; got:\n%s", body)
	}
}

package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/server"
)

// newTestServer brings up a fresh server backed by an httptest.Server.
func newTestServer(t *testing.T, cfg config.Config) (*httptest.Server, *server.Server) {
	t.Helper()
	dir := t.TempDir()
	cfg.DataDir = dir
	cfg.DBPath = filepath.Join(dir, "t.db")
	cfg.ArtifactsDir = filepath.Join(dir, "art")
	cfg.MaxRequestSize = 100 << 20
	cfg.Addr = ":0"
	if cfg.Auth == "" {
		cfg.Auth = "none"
	}
	c, err := config.FromEnv(cfg)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	srv, err := server.New(context.Background(), c, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return ts, srv
}

func TestHealthAndVersion(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	for _, path := range []string{"/healthz", "/readyz", "/version"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s: status %d body=%s", path, resp.StatusCode, body)
		}
		_ = resp.Body.Close()
	}
}

func TestRedirectRoot(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("want 302, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "/ui/" {
		t.Fatalf("want Location /ui/, got %s", resp.Header.Get("Location"))
	}
}

func TestUserHeaderStripped(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	// Try to spoof identity via the header.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/auth/whoami", nil)
	req.Header.Set("X-LiteMLflow-User", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "anonymous") {
		t.Fatalf("expected anonymous (header should be stripped), got %s", body)
	}
	if strings.Contains(string(body), "admin") {
		t.Fatalf("client successfully spoofed user identity: %s", body)
	}
}

func TestArtifactRequiresExistingRun(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	// PUT to a non-existent run id should 404 (RESOURCE_DOES_NOT_EXIST).
	req, _ := http.NewRequest("PUT", ts.URL+"/api/2.0/mlflow-artifacts/artifacts/deadbeef00000000/file.bin",
		strings.NewReader("payload"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 404, got %d body=%s", resp.StatusCode, body)
	}
}

func TestBasicAuth(t *testing.T) {
	t.Parallel()
	pwHash := sha256.Sum256([]byte("hunter2"))
	cfg := config.Config{
		Auth:          "basic",
		BasicUser:     "alice",
		BasicPassHash: hex.EncodeToString(pwHash[:]),
	}
	ts, _ := newTestServer(t, cfg)

	// No credentials -> 401.
	resp, _ := http.Get(ts.URL + "/api/2.0/mlflow/experiments/search")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Wrong password -> 401.
	req, _ := http.NewRequest("GET", ts.URL+"/api/2.0/mlflow/experiments/search", nil)
	req.SetBasicAuth("alice", "wrong")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 wrong, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Right credentials -> 200.
	req, _ = http.NewRequest("GET", ts.URL+"/api/2.0/mlflow/experiments/search", nil)
	req.SetBasicAuth("alice", "hunter2")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200, got %d body=%s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Healthz is public even under basic auth.
	resp, _ = http.Get(ts.URL + "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz must be public, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestExperimentCRUDViaHTTP(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// Create
	resp, err := http.Post(ts.URL+"/api/2.0/mlflow/experiments/create",
		"application/json", strings.NewReader(`{"name":"e1"}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: status %d body=%s", resp.StatusCode, body)
	}

	// Duplicate -> 400 with RESOURCE_ALREADY_EXISTS
	resp, _ = http.Post(ts.URL+"/api/2.0/mlflow/experiments/create",
		"application/json", strings.NewReader(`{"name":"e1"}`))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(string(body), "RESOURCE_ALREADY_EXISTS") {
		t.Fatalf("want 400 RESOURCE_ALREADY_EXISTS, got %d %s", resp.StatusCode, body)
	}
}

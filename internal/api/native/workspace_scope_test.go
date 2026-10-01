package native

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/federation"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

type nativeFixture struct {
	st     *store.SQLiteStore
	router http.Handler
	expA   int64 // workspace "default"
	runA   string
	whA    int64 // webhook in "default"
}

func newNativeFixture(t *testing.T) *nativeFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.OpenSQLite(ctx, filepath.Join(dir, "db.sqlite"), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkspace(ctx, &model.Workspace{ID: "wsb", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	f := &nativeFixture{st: st}
	if f.expA, err = st.CreateExperiment(ctx, &model.Experiment{
		Name: "secret-exp", WorkspaceID: "default", Tags: []model.KV{{Key: "owner", Value: "team-a"}},
	}); err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ExperimentID: f.expA}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	f.runA = run.ID
	if err := st.LogMetric(ctx, run.ID, model.Metric{Key: "loss", Value: 0.5, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	if f.whA, err = st.CreateWebhook(ctx, &model.Webhook{
		Name: "a", URL: "lmf://echo", Events: "run_finished", WorkspaceID: "default", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{Store: st, FederationCache: federation.NewCache(0, 0)}
	r := chi.NewRouter()
	h.Mount(r)
	f.router = r
	return f
}

func (f *nativeFixture) do(t *testing.T, method, path, ws, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if ws != "" {
		req.Header.Set("X-LiteMLflow-Workspace", ws)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestWebhookByIDIsWorkspaceScoped: PATCH/DELETE/test looked webhooks up by
// id alone.
func TestWebhookByIDIsWorkspaceScoped(t *testing.T) {
	f := newNativeFixture(t)
	id := strconv.FormatInt(f.whA, 10)
	cases := []struct{ method, path, body string }{
		{"PATCH", "/api/v1/webhooks/" + id, `{"enabled":false}`},
		{"POST", "/api/v1/webhooks/" + id + "/test", ``},
		{"DELETE", "/api/v1/webhooks/" + id, ``},
	}
	for _, c := range cases {
		if code, body := f.do(t, c.method, c.path, "wsb", c.body); code != http.StatusNotFound {
			t.Errorf("cross-workspace %s %s: %d (%s), want 404", c.method, c.path, code, body)
		}
	}
	wh, err := f.st.GetWebhook(context.Background(), f.whA)
	if err != nil {
		t.Fatalf("webhook deleted from another workspace: %v", err)
	}
	if !wh.Enabled {
		t.Error("webhook modified from another workspace")
	}
	if code, _ := f.do(t, "PATCH", "/api/v1/webhooks/"+id, "default", `{"enabled":false}`); code != http.StatusOK {
		t.Errorf("owner PATCH: %d, want 200", code)
	}
}

// TestAnalyticsQueryIgnoresBodyWorkspace: the DSL body's workspace_id used to
// override the request workspace.
func TestAnalyticsQueryIgnoresBodyWorkspace(t *testing.T) {
	f := newNativeFixture(t)
	q := `{"metric":"loss","agg":"max","workspace_id":"default"}`
	code, out := f.do(t, "POST", "/api/v1/analytics/query", "wsb", q)
	if code != http.StatusOK {
		t.Fatalf("analytics: %d %s", code, out)
	}
	var res store.AnalyticsResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.TotalRunsScanned != 0 || (len(res.Rows) != 0 && res.Rows[0].RunCount > 0) {
		t.Errorf("workspace B aggregated workspace A's runs: %s", out)
	}
	// Owner still sees its data.
	_, out = f.do(t, "POST", "/api/v1/analytics/query", "default", q)
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.TotalRunsScanned == 0 {
		t.Errorf("owner query scanned nothing: %s", out)
	}
}

// TestRunWritesAreWorkspaceScoped: evals (upsert by run_id) and trace spans
// could be written against another workspace's run.
func TestRunWritesAreWorkspaceScoped(t *testing.T) {
	f := newNativeFixture(t)
	if code, body := f.do(t, "POST", "/api/v1/evals", "wsb", `{"run_id":"`+f.runA+`","score":0}`); code != http.StatusNotFound {
		t.Errorf("cross-workspace eval: %d (%s), want 404", code, body)
	}
	spans := `{"spans":[{"name":"s","run_id":"` + f.runA + `","start_time_ns":1}]}`
	if code, body := f.do(t, "POST", "/api/v1/traces", "wsb", spans); code != http.StatusNotFound {
		t.Errorf("cross-workspace traces: %d (%s), want 404", code, body)
	}
	otlp := `{"resourceSpans":[{"resource":{"attributes":[{"key":"litemlflow.run_id","value":{"stringValue":"` +
		f.runA + `"}}]},"scopeSpans":[{"spans":[{"traceId":"t","spanId":"s1","name":"n","startTimeUnixNano":"1"}]}]}]}`
	if code, body := f.do(t, "POST", "/v1/traces", "wsb", otlp); code != http.StatusNotFound {
		t.Errorf("cross-workspace OTLP: %d (%s), want 404", code, body)
	}
	got, err := f.st.GetSpansByRun(context.Background(), f.runA)
	if err != nil || len(got) != 0 {
		t.Errorf("spans landed on foreign run: %v %v", got, err)
	}
	if code, body := f.do(t, "POST", "/api/v1/traces", "default", spans); code != http.StatusOK {
		t.Errorf("owner traces: %d (%s)", code, body)
	}
}

// TestCloneExperimentIsWorkspaceScoped: cloning copied a foreign
// experiment's name and tags into the caller's workspace.
func TestCloneExperimentIsWorkspaceScoped(t *testing.T) {
	f := newNativeFixture(t)
	path := "/api/v1/experiments/" + strconv.FormatInt(f.expA, 10) + "/clone"
	if code, body := f.do(t, "POST", path, "wsb", `{}`); code != http.StatusNotFound {
		t.Errorf("cross-workspace clone: %d (%s), want 404", code, body)
	}
	if code, body := f.do(t, "POST", path, "default", `{"name":"copy"}`); code != http.StatusOK {
		t.Errorf("owner clone: %d (%s)", code, body)
	}
}

// TestFederationInboundBodyIsBounded: the unauthenticated federation
// endpoints read the whole body before checking the HMAC.
func TestFederationInboundBodyIsBounded(t *testing.T) {
	f := newNativeFixture(t)
	req := httptest.NewRequest("POST", "/api/v1/federate/search",
		bytes.NewReader(bytes.Repeat([]byte("x"), maxFederationRequestBytes+10)))
	req.Header.Set(federation.HeaderPeer, "p")
	req.Header.Set(federation.HeaderSignature, "sha256=00")
	req.Header.Set(federation.HeaderTimestamp, strconv.FormatInt(time.Now().UnixMilli(), 10))
	h := &Handler{Store: f.st}
	if _, err := h.validateFederationRequest(req); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized federation body: err = %v, want size error", err)
	}
}

package mlflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
	"github.com/gorevds/litemlflow/internal/webhooks"
)

type scopeFixture struct {
	st      *store.SQLiteStore
	h       *Handler
	router  http.Handler
	expA    int64 // in "default"
	expB    int64 // in "wsb"
	runA    string
	notifyM sync.Mutex
	notify  []string // workspaces seen by the notifier
}

type recordingNotifier struct{ f *scopeFixture }

func (n recordingNotifier) Notify(ctx context.Context, _ string, _ *model.Run) {
	n.f.notifyM.Lock()
	n.f.notify = append(n.f.notify, webhooks.WorkspaceFromContext(ctx))
	n.f.notifyM.Unlock()
}

func newScopeFixture(t *testing.T) *scopeFixture {
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
	f := &scopeFixture{st: st}
	if f.expA, err = st.CreateExperiment(ctx, &model.Experiment{Name: "a", WorkspaceID: "default"}); err != nil {
		t.Fatal(err)
	}
	if f.expB, err = st.CreateExperiment(ctx, &model.Experiment{Name: "b", WorkspaceID: "wsb"}); err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ExperimentID: f.expA}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	f.runA = run.ID
	arts, err := artifact.NewFilesystemStore(filepath.Join(dir, "art"))
	if err != nil {
		t.Fatal(err)
	}
	f.h = &Handler{Store: st, Artifacts: arts, MaxArtifactSize: 16}
	f.h.Dispatcher = recordingNotifier{f}
	r := chi.NewRouter()
	f.h.Mount(r)
	f.router = r
	return f
}

func (f *scopeFixture) do(t *testing.T, method, path, ws, body string) (int, string) {
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

// TestExperimentEndpointsAreWorkspaceScoped: experiment_id-addressed
// endpoints looked experiments up by id alone, so workspace B could read,
// rename, tag, delete or restore workspace A's experiments.
func TestExperimentEndpointsAreWorkspaceScoped(t *testing.T) {
	f := newScopeFixture(t)
	a := strconv.FormatInt(f.expA, 10)

	if code, _ := f.do(t, "GET", "/api/2.0/mlflow/experiments/get?experiment_id="+a, "wsb", ""); code != http.StatusNotFound {
		t.Errorf("cross-workspace get: %d, want 404", code)
	}
	if code, _ := f.do(t, "GET", "/api/2.0/mlflow/experiments/get?experiment_id="+a, "default", ""); code != http.StatusOK {
		t.Errorf("same-workspace get: %d, want 200", code)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/2.0/mlflow/experiments/update", `{"experiment_id":"` + a + `","new_name":"pwned"}`},
		{"/api/2.0/mlflow/experiments/set-experiment-tag", `{"experiment_id":"` + a + `","key":"k","value":"v"}`},
		{"/api/2.0/mlflow/experiments/delete", `{"experiment_id":"` + a + `"}`},
		{"/api/2.0/mlflow/experiments/restore", `{"experiment_id":"` + a + `"}`},
		{"/api/2.0/mlflow/runs/create", `{"experiment_id":"` + a + `"}`},
	} {
		if code, body := f.do(t, "POST", c.path, "wsb", c.body); code != http.StatusNotFound {
			t.Errorf("cross-workspace POST %s: %d (%s), want 404", c.path, code, body)
		}
	}
	e, err := f.st.GetExperiment(context.Background(), f.expA)
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "a" || e.LifecycleStage != model.LifecycleActive || len(e.Tags) != 0 {
		t.Errorf("experiment A was mutated from workspace B: %+v", e)
	}
}

// TestSearchRunsIsWorkspaceScoped: runs/search passed experiment ids straight
// to a store query with no workspace filter.
func TestSearchRunsIsWorkspaceScoped(t *testing.T) {
	f := newScopeFixture(t)
	count := func(ws, body string) int {
		code, out := f.do(t, "POST", "/api/2.0/mlflow/runs/search", ws, body)
		if code != http.StatusOK {
			t.Fatalf("search (%s, %s): %d %s", ws, body, code, out)
		}
		var resp searchRunsResp
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatal(err)
		}
		return len(resp.Runs)
	}
	a := strconv.FormatInt(f.expA, 10)
	if n := count("wsb", `{"experiment_ids":["`+a+`"]}`); n != 0 {
		t.Errorf("workspace B sees %d of A's runs", n)
	}
	if n := count("wsb", `{}`); n != 0 {
		t.Errorf("empty experiment_ids returned %d runs across workspaces", n)
	}
	if n := count("default", `{"experiment_ids":["`+a+`"]}`); n != 1 {
		t.Errorf("owner sees %d runs, want 1", n)
	}
	// Filter validation still applies with no visible experiments.
	if code, _ := f.do(t, "POST", "/api/2.0/mlflow/runs/search", "wsb", `{"page_token":"!!!"}`); code != http.StatusBadRequest {
		t.Errorf("bad page_token: %d, want 400", code)
	}
}

// TestArtifactUploadSizeCap: PUT passed maxSize=0 (unlimited) and the route
// is exempt from the global body limit.
func TestArtifactUploadSizeCap(t *testing.T) {
	f := newScopeFixture(t)
	url := "/api/2.0/mlflow-artifacts/artifacts/" + f.runA + "/f.bin"
	if code, body := f.do(t, "PUT", url, "default", strings.Repeat("x", 17)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize upload: %d (%s), want 413", code, body)
	}
	if code, body := f.do(t, "PUT", url, "default", strings.Repeat("x", 16)); code != http.StatusOK {
		t.Errorf("upload at cap: %d (%s), want 200", code, body)
	}
	// Opening a directory is a client error, not a 500.
	if code, _ := f.do(t, "PUT", "/api/2.0/mlflow-artifacts/artifacts/"+f.runA+"/d/x", "default", "x"); code != http.StatusOK {
		t.Fatalf("upload d/x: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/2.0/mlflow-artifacts/artifacts/"+f.runA+"/d", "default", ""); code != http.StatusBadRequest {
		t.Errorf("GET directory: %d, want 400", code)
	}
}

// TestRunEventsCarryWorkspace: webhook notifications must be resolved in the
// request's workspace.
func TestRunEventsCarryWorkspace(t *testing.T) {
	f := newScopeFixture(t)
	b := strconv.FormatInt(f.expB, 10)
	code, out := f.do(t, "POST", "/api/2.0/mlflow/runs/create", "wsb", `{"experiment_id":"`+b+`"}`)
	if code != http.StatusOK {
		t.Fatalf("create run: %d %s", code, out)
	}
	f.notifyM.Lock()
	defer f.notifyM.Unlock()
	if len(f.notify) != 1 || f.notify[0] != "wsb" {
		t.Errorf("notifier workspaces = %q, want [wsb]", f.notify)
	}
}

// TestDeleteModelAliasAcceptsJSONBody: the MLflow Python client sends DELETE
// parameters as a JSON body; the handler only read the query string (400).
func TestDeleteModelAliasAcceptsJSONBody(t *testing.T) {
	f := newScopeFixture(t)
	steps := []struct{ method, path, body string }{
		{"POST", "/api/2.0/mlflow/registered-models/create", `{"name":"m"}`},
		{"POST", "/api/2.0/mlflow/model-versions/create", `{"name":"m","source":"s3://x"}`},
		{"POST", "/api/2.0/mlflow/registered-models/alias", `{"name":"m","alias":"champion","version":"1"}`},
		{"DELETE", "/api/2.0/mlflow/registered-models/alias", `{"name":"m","alias":"champion"}`},
	}
	for _, s := range steps {
		if code, out := f.do(t, s.method, s.path, "default", s.body); code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", s.method, s.path, code, out)
		}
	}
	if code, _ := f.do(t, "GET", "/api/2.0/mlflow/registered-models/alias?name=m&alias=champion", "default", ""); code != http.StatusNotFound {
		t.Errorf("alias still resolvable after delete: %d", code)
	}
}

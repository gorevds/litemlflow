package server_test

// Guards the independent-review finding that native read/write-by-run-ID
// handlers (run data, note, traces, eval) queried by run ID with no workspace
// filter, so a caller in workspace B could read/modify a run that lives in
// workspace A by guessing its 32-hex ID. Each handler must now 404 when the
// run does not belong to the caller's workspace.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

func TestRunByIDHandlersAreWorkspaceScoped(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{}) // auth=none → RBAC inactive

	do := func(method, path, ws, body string) (*http.Response, []byte) {
		t.Helper()
		var r *strings.Reader
		if body != "" {
			r = strings.NewReader(body)
		} else {
			r = strings.NewReader("")
		}
		req, err := http.NewRequest(method, ts.URL+path, r)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if ws != "" {
			req.Header.Set("X-Workspace", ws)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		raw := make([]byte, 0)
		buf := make([]byte, 4096)
		for {
			n, e := resp.Body.Read(buf)
			raw = append(raw, buf[:n]...)
			if e != nil {
				break
			}
		}
		_ = resp.Body.Close()
		return resp, raw
	}

	// Workspaces must exist before the middleware accepts the X-Workspace header.
	for _, ws := range []string{"ws-a", "ws-b"} {
		resp, body := do(http.MethodPost, "/api/v1/workspaces", "",
			`{"id":"`+ws+`","name":"`+ws+`"}`)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create workspace %s: want 201, got %d: %s", ws, resp.StatusCode, body)
		}
	}

	// Create an experiment + run in ws-a.
	resp, body := do(http.MethodPost, "/api/2.0/mlflow/experiments/create", "ws-a", `{"name":"scoped-exp"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create experiment: %d %s", resp.StatusCode, body)
	}
	var exp struct {
		ExperimentID string `json:"experiment_id"`
	}
	_ = json.Unmarshal(body, &exp)

	resp, body = do(http.MethodPost, "/api/2.0/mlflow/runs/create", "ws-a",
		`{"experiment_id":"`+exp.ExperimentID+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create run: %d %s", resp.StatusCode, body)
	}
	var run struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.Unmarshal(body, &run)
	runID := run.Run.Info.RunID
	if runID == "" {
		t.Fatalf("no run_id: %s", body)
	}

	// Set a note in ws-a so GetRunNote has content to return.
	if resp, body := do(http.MethodPut, "/api/v1/runs/"+runID+"/note", "ws-a", `{"content":"hi"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("set note in ws-a: %d %s", resp.StatusCode, body)
	}
	// Create an eval in ws-a so GetEval has content to leak.
	if resp, body := do(http.MethodPost, "/api/v1/evals", "ws-a", `{"run_id":"`+runID+`","score":0.9}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("create eval in ws-a: %d %s", resp.StatusCode, body)
	}

	// Owning workspace (ws-a): all run-ID handlers succeed.
	for _, path := range []string{
		"/api/v1/runs/" + runID + "/data",
		"/api/v1/runs/" + runID + "/note",
		"/api/v1/runs/" + runID + "/traces",
		"/api/v1/runs/" + runID + "/lineage",
		"/api/v1/evals/" + runID,
	} {
		if resp, body := do(http.MethodGet, path, "ws-a", ""); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s in owning ws-a: want 200, got %d: %s", path, resp.StatusCode, body)
		}
	}

	// Foreign workspace (ws-b): every run-ID handler must 404 (cross-tenant
	// access denied; constant 404 shape so the run ID cannot be probed).
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/runs/" + runID + "/data", ""},
		{http.MethodGet, "/api/v1/runs/" + runID + "/note", ""},
		{http.MethodPut, "/api/v1/runs/" + runID + "/note", `{"content":"pwned"}`},
		{http.MethodGet, "/api/v1/runs/" + runID + "/traces", ""},
		{http.MethodGet, "/api/v1/runs/" + runID + "/lineage", ""},
		{http.MethodGet, "/api/v1/evals/" + runID, ""},
	} {
		resp, body := do(tc.method, tc.path, "ws-b", tc.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s from foreign ws-b: want 404, got %d: %s", tc.method, tc.path, resp.StatusCode, body)
		}
	}

	// The note must be unchanged after the cross-workspace PUT attempt.
	if resp, body := do(http.MethodGet, "/api/v1/runs/"+runID+"/note", "ws-a", ""); resp.StatusCode == http.StatusOK {
		if !strings.Contains(string(body), "hi") || strings.Contains(string(body), "pwned") {
			t.Errorf("note was modified across workspaces: %s", body)
		}
	}
}

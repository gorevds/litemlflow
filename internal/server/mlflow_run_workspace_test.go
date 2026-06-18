package server_test

// Guards a cross-tenant isolation gap symmetric to the P0 native-API fix:
// the MLflow-compat run endpoints (runs/get, runs/update, runs/log-metric, …)
// looked runs up by run_id with no workspace check, so a caller in workspace B
// could read or mutate a run belonging to workspace A by guessing its id. The
// as_of read path was already scoped; this covers the common paths.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

func TestMLflowRunEndpointsAreWorkspaceScoped(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{}) // auth=none

	do := func(method, path, ws, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
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
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	if st, body := do(http.MethodPost, "/api/v1/workspaces", "", `{"id":"ws-b","name":"ws-b"}`); st != http.StatusCreated {
		t.Fatalf("create workspace ws-b: %d %s", st, body)
	}

	// Create an experiment + run in the DEFAULT workspace.
	st, body := do(http.MethodPost, "/api/2.0/mlflow/experiments/create", "", `{"name":"victim-exp"}`)
	if st != http.StatusOK {
		t.Fatalf("create experiment: %d %s", st, body)
	}
	var expResp struct {
		ExperimentID string `json:"experiment_id"`
	}
	if err := json.Unmarshal([]byte(body), &expResp); err != nil {
		t.Fatalf("decode experiment: %v", err)
	}
	st, body = do(http.MethodPost, "/api/2.0/mlflow/runs/create", "", `{"experiment_id":"`+expResp.ExperimentID+`","start_time":1}`)
	if st != http.StatusOK {
		t.Fatalf("create run: %d %s", st, body)
	}
	var runResp struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(body), &runResp); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	runID := runResp.Run.Info.RunID
	if runID == "" {
		t.Fatal("empty run_id")
	}

	// ws-b owns the run in its own workspace? No. Every access from ws-b must 404.
	cases := []struct {
		name, method, path, body string
	}{
		{"get", http.MethodGet, "/api/2.0/mlflow/runs/get?run_id=" + runID, ""},
		{"update", http.MethodPost, "/api/2.0/mlflow/runs/update", `{"run_id":"` + runID + `","status":"FINISHED"}`},
		{"delete", http.MethodPost, "/api/2.0/mlflow/runs/delete", `{"run_id":"` + runID + `"}`},
		{"log-metric", http.MethodPost, "/api/2.0/mlflow/runs/log-metric", `{"run_id":"` + runID + `","key":"m","value":1,"timestamp":1,"step":0}`},
		{"log-parameter", http.MethodPost, "/api/2.0/mlflow/runs/log-parameter", `{"run_id":"` + runID + `","key":"p","value":"v"}`},
		{"set-tag", http.MethodPost, "/api/2.0/mlflow/runs/set-tag", `{"run_id":"` + runID + `","key":"t","value":"v"}`},
		{"log-batch", http.MethodPost, "/api/2.0/mlflow/runs/log-batch", `{"run_id":"` + runID + `","metrics":[{"key":"m","value":1,"timestamp":1,"step":0}]}`},
		{"delete-tag", http.MethodPost, "/api/2.0/mlflow/runs/delete-tag", `{"run_id":"` + runID + `","key":"t"}`},
		{"log-inputs", http.MethodPost, "/api/2.0/mlflow/runs/log-inputs", `{"run_id":"` + runID + `","datasets":[]}`},
		{"get-metric-history", http.MethodGet, "/api/2.0/mlflow/metrics/get-history?run_id=" + runID + "&metric_key=m", ""},
		{"artifacts-list", http.MethodGet, "/api/2.0/mlflow/artifacts/list?run_id=" + runID, ""},
		{"artifact-proxy-list", http.MethodGet, "/api/2.0/mlflow-artifacts/artifacts?path=" + runID, ""},
		{"artifact-get", http.MethodGet, "/api/2.0/mlflow-artifacts/artifacts/" + runID + "/model.pkl", ""},
		{"artifact-put", http.MethodPut, "/api/2.0/mlflow-artifacts/artifacts/" + runID + "/evil.txt", "payload"},
		{"artifact-delete", http.MethodDelete, "/api/2.0/mlflow-artifacts/artifacts/" + runID + "/model.pkl", ""},
	}
	for _, c := range cases {
		t.Run(c.name+" from foreign ws-b", func(t *testing.T) {
			st, body := do(c.method, c.path, "ws-b", c.body)
			if st != http.StatusNotFound {
				t.Errorf("%s on foreign run: want 404, got %d %s", c.name, st, body)
			}
		})
	}

	// ws-b must not create a run inside the default workspace's experiment.
	if st, body := do(http.MethodPost, "/api/2.0/mlflow/runs/create", "ws-b",
		`{"experiment_id":"`+expResp.ExperimentID+`","start_time":1}`); st != http.StatusNotFound {
		t.Errorf("create run in foreign experiment: want 404, got %d %s", st, body)
	}

	// Sanity: the owning workspace (default) can still access the run.
	if st, body := do(http.MethodGet, "/api/2.0/mlflow/runs/get?run_id="+runID, "", ""); st != http.StatusOK {
		t.Errorf("get from owning workspace: want 200, got %d %s", st, body)
	}
}

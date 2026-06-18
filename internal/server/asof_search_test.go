package server_test

// Guards independent-review finding 2.6: SearchRuns with as_of evaluated
// lifecycle/filter/order against CURRENT state, so a run that was active at
// as_of but later deleted was dropped before historical reconstruction. The
// fix forces lifecycle=all under as_of and rejects filter/order_by + as_of.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

func TestSearchRunsAsOfIncludesLaterDeletedRun(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	post := func(path, body string) (int, string) {
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		for {
			n, e := resp.Body.Read(buf)
			b.Write(buf[:n])
			if e != nil {
				break
			}
		}
		return resp.StatusCode, b.String()
	}

	_, expBody := post("/api/2.0/mlflow/experiments/create", `{"name":"asof-exp"}`)
	var exp struct {
		ExperimentID string `json:"experiment_id"`
	}
	_ = json.Unmarshal([]byte(expBody), &exp)

	// Run created at start_time=1000 (long before "now").
	_, runBody := post("/api/2.0/mlflow/runs/create",
		fmt.Sprintf(`{"experiment_id":%q,"start_time":1000}`, exp.ExperimentID))
	var run struct {
		Run struct {
			Info struct {
				RunID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.Unmarshal([]byte(runBody), &run)
	runID := run.Run.Info.RunID
	if runID == "" {
		t.Fatalf("no run_id: %s", runBody)
	}

	// as_of = now (after creation, before deletion).
	asOf := timeNowMillis()

	// Delete the run (lifecycle -> deleted) AFTER as_of.
	if st, body := post("/api/2.0/mlflow/runs/delete", fmt.Sprintf(`{"run_id":%q}`, runID)); st != http.StatusOK {
		t.Fatalf("delete run: %d %s", st, body)
	}

	// as_of search (default ACTIVE_ONLY view) must still return the run, since
	// it was active at as_of.
	st, body := post(fmt.Sprintf("/api/2.0/mlflow/runs/search?as_of=%d", asOf),
		fmt.Sprintf(`{"experiment_ids":[%q]}`, exp.ExperimentID))
	if st != http.StatusOK {
		t.Fatalf("as_of search: %d %s", st, body)
	}
	if !strings.Contains(body, runID) {
		t.Errorf("as_of=%d search should include run active at that time (later deleted); body=%s", asOf, body)
	}

	// filter + as_of must be rejected with 400.
	st, _ = post(fmt.Sprintf("/api/2.0/mlflow/runs/search?as_of=%d", asOf),
		fmt.Sprintf(`{"experiment_ids":[%q],"filter":"metrics.loss < 0.5"}`, exp.ExperimentID))
	if st != http.StatusBadRequest {
		t.Errorf("filter + as_of: want 400, got %d", st)
	}
}

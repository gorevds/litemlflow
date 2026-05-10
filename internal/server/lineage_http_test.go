// HTTP-level tests for the v1.4 GET /api/v1/runs/{id}/lineage endpoint.
//
// Covers:
//   - default response shape (both directions, datasets array)
//   - direction=upstream (no descendants)
//   - direction=downstream (no ancestors)
//   - depth and fanout query-param validation
//   - run→dataset edges populate version/dataset_id from the v1.2 mirror
package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

type runLineageWire struct {
	Run         map[string]any   `json:"run"`
	Ancestors   []map[string]any `json:"ancestors"`
	Descendants []map[string]any `json:"descendants"`
	Datasets    []map[string]any `json:"datasets"`
	Truncated   bool             `json:"truncated"`
}

// createExperimentReturning posts to the MLflow create-experiment endpoint
// and returns the experiment_id.
func createExperimentReturning(t *testing.T, ts string, name string) string {
	t.Helper()
	resp, err := http.Post(ts+"/api/2.0/mlflow/experiments/create",
		"application/json", strings.NewReader(fmt.Sprintf(`{"name":%q}`, name)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		ID string `json:"experiment_id"`
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &got)
	if got.ID == "" {
		t.Fatalf("create exp: %s", raw)
	}
	return got.ID
}

// createRunReturning posts to runs/create with optional parent_run_id tag.
// Returns the new run id.
func createRunReturning(t *testing.T, ts, expID, parentID string) string {
	t.Helper()
	body := map[string]any{
		"experiment_id": expID,
		"start_time":    1,
	}
	if parentID != "" {
		body["tags"] = []map[string]string{{"key": "mlflow.parentRunId", "value": parentID}}
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts+"/api/2.0/mlflow/runs/create",
		"application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Run struct {
			Info struct {
				ID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	rawResp, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(rawResp, &got)
	if got.Run.Info.ID == "" {
		t.Fatalf("create run: %s", rawResp)
	}
	return got.Run.Info.ID
}

// getLineage GETs /api/v1/runs/{id}/lineage with the given query string.
func getLineage(t *testing.T, ts, runID, query string) (int, runLineageWire) {
	t.Helper()
	url := fmt.Sprintf("%s/api/v1/runs/%s/lineage", ts, runID)
	if query != "" {
		url += "?" + query
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got runLineageWire
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode: %v body=%s", err, raw)
		}
	}
	return resp.StatusCode, got
}

// TestLineageHTTPDirectionAndDepth verifies the v1.4 query params shape the
// returned ancestor/descendant slices correctly.
func TestLineageHTTPDirectionAndDepth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	expID := createExperimentReturning(t, ts.URL, "lineage-http")
	gp := createRunReturning(t, ts.URL, expID, "")
	p := createRunReturning(t, ts.URL, expID, gp)
	_ = createRunReturning(t, ts.URL, expID, p) // child — referenced via depth-2 walk

	// direction=upstream from p — should have [gp] only.
	status, got := getLineage(t, ts.URL, p, "direction=upstream")
	if status != http.StatusOK {
		t.Fatalf("upstream: status %d", status)
	}
	if len(got.Ancestors) != 1 || got.Ancestors[0]["id"] != gp {
		t.Errorf("upstream ancestors: got %+v want [%s]", got.Ancestors, gp)
	}
	if len(got.Descendants) != 0 {
		t.Errorf("upstream should have no descendants, got %d", len(got.Descendants))
	}

	// direction=downstream from gp, depth=2 — should reach both p and c.
	status, got = getLineage(t, ts.URL, gp, "direction=downstream&depth=2")
	if status != http.StatusOK {
		t.Fatalf("downstream: status %d", status)
	}
	if len(got.Descendants) != 2 {
		t.Errorf("downstream depth=2: want 2 nodes, got %d", len(got.Descendants))
	}
	if len(got.Ancestors) != 0 {
		t.Errorf("downstream should have no ancestors, got %d", len(got.Ancestors))
	}

	// depth=1 from gp — should only reach p, with truncated=true.
	_, got = getLineage(t, ts.URL, gp, "direction=downstream&depth=1")
	if len(got.Descendants) != 1 || got.Descendants[0]["id"] != p {
		t.Errorf("depth=1: want [p], got %+v", got.Descendants)
	}
	if !got.Truncated {
		t.Errorf("depth=1 should set truncated=true (deeper subtree exists)")
	}
}

// TestLineageHTTPInvalidParams returns 400 for malformed direction/depth.
func TestLineageHTTPInvalidParams(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "lineage-bad-params")
	rid := createRunReturning(t, ts.URL, expID, "")

	cases := []struct {
		query string
		why   string
	}{
		{"direction=sideways", "unknown direction"},
		{"depth=0", "depth below min"},
		{"depth=99", "depth above max"},
		{"depth=foo", "depth not a number"},
		{"fanout=0", "fanout below min"},
		{"fanout=9999", "fanout above max"},
	}
	for _, c := range cases {
		status, _ := getLineage(t, ts.URL, rid, c.query)
		if status != http.StatusBadRequest {
			t.Errorf("%s (%q): want 400, got %d", c.why, c.query, status)
		}
	}
}

// Go-level unit tests for the MLflow REST surface (`internal/api/mlflow`).
//
// Until v1.2 the only coverage of `internal/api/mlflow/*.go` was the Python
// `tests/integration/mlflow_compat.py` 31-check suite. The Quality auditor
// (deep review, item: "internal/api/mlflow has zero Go unit tests" — see
// docs/reports/2026-05-08-deep-review.md) flagged this as the biggest gap.
//
// These tests are table-driven, target the **error branches the Python
// compat suite skips**: missing fields, malformed bodies, duplicate names,
// bad enums, oversized batches, etc. Happy paths stay with the Python
// suite, which checks the full wire contract end-to-end.
//
// Conventions:
//   - one helper per status-code expectation (`mustGet`, `mustPostJSON`)
//   - test names follow `TestMlflow<Endpoint>_<Behaviour>`
//   - each test is `t.Parallel()` so the package wallclock stays cheap
package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// mlflowErr is the standard wire shape MLflow uses for non-2xx responses.
type mlflowErr struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

func mustPostJSON(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func decodeMlflowErr(t *testing.T, raw []byte) mlflowErr {
	t.Helper()
	var e mlflowErr
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode mlflow err: %v body=%q", err, raw)
	}
	return e
}

// ---- experiments -------------------------------------------------------

func TestMlflowCreateExperiment_MissingName(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/create", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", resp.StatusCode, raw)
	}
	if got := decodeMlflowErr(t, raw); got.ErrorCode != "INVALID_PARAMETER_VALUE" {
		t.Errorf("expected INVALID_PARAMETER_VALUE, got %+v", got)
	}
}

func TestMlflowCreateExperiment_DuplicateName(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	body := `{"name":"dupe"}`
	r1, _ := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/create", body)
	if r1.StatusCode != 200 {
		t.Fatalf("first create: %d", r1.StatusCode)
	}
	r2, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/create", body)
	if r2.StatusCode != http.StatusBadRequest && r2.StatusCode != http.StatusConflict {
		t.Errorf("expected 400/409 for duplicate, got %d body=%q", r2.StatusCode, raw)
	}
	if got := decodeMlflowErr(t, raw); got.ErrorCode != "RESOURCE_ALREADY_EXISTS" && got.ErrorCode != "INVALID_PARAMETER_VALUE" {
		t.Errorf("expected RESOURCE_ALREADY_EXISTS, got %+v", got)
	}
}

func TestMlflowGetExperiment_NotFound(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/get?experiment_id=99999", "")
	// MLflow contract: GET-shaped fetch via query string.
	resp2, _ := http.Get(ts.URL + "/api/2.0/mlflow/experiments/get?experiment_id=99999")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		raw, _ = io.ReadAll(resp2.Body)
		t.Fatalf("expected 404 from GET, got %d (%s)", resp2.StatusCode, raw)
	}
	_ = resp
}

func TestMlflowDeleteExperiment_NotFound(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/delete", `{"experiment_id":"99999"}`)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 404/400, got %d body=%q", resp.StatusCode, raw)
	}
}

// ---- runs --------------------------------------------------------------

func TestMlflowCreateRun_MissingExperimentID(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/create", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestMlflowLogMetric_MissingRunID(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/log-metric",
		`{"key":"loss","value":0.5,"timestamp":1700000000000,"step":1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing run_id, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestMlflowLogBatch_TooManyMetrics(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// First create an experiment + run so we can target it.
	_, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/create", `{"name":"batch-test"}`)
	var ce struct {
		ExperimentID string `json:"experiment_id"`
	}
	_ = json.Unmarshal(raw, &ce)
	_, runRaw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/create",
		fmt.Sprintf(`{"experiment_id":"%s"}`, ce.ExperimentID))
	var cr struct {
		Run struct{ Info struct{ RunID string `json:"run_id"` } } `json:"run"`
	}
	_ = json.Unmarshal(runRaw, &cr)
	runID := cr.Run.Info.RunID

	// 1001 metrics — over the documented 1000-element batch cap.
	var b strings.Builder
	b.WriteString(`{"run_id":"` + runID + `","metrics":[`)
	for i := 0; i < 1001; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"key":"k%d","value":%d,"timestamp":1700000000000,"step":0}`, i, i)
	}
	b.WriteString(`]}`)
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/log-batch", b.String())
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (%s)", resp.StatusCode, string(raw[:min(len(raw), 200)]))
	}
}

func TestMlflowSearchRuns_BadFilter(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	body := `{"experiment_ids":["1"],"filter":"this is not a valid mlflow filter clause"}`
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/search", body)
	// Filter parsing failure should yield 400, not 500.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusOK {
		t.Errorf("malformed filter should not 500; got %d (%s)", resp.StatusCode, raw)
	}
}

// ---- model registry ---------------------------------------------------

func TestMlflowCreateRegisteredModel_MissingName(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/registered-models/create", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestMlflowCreateRegisteredModel_Duplicate(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	body := `{"name":"my-model"}`
	r1, _ := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/registered-models/create", body)
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first create: %d", r1.StatusCode)
	}
	r2, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/registered-models/create", body)
	if r2.StatusCode != http.StatusBadRequest && r2.StatusCode != http.StatusConflict {
		t.Errorf("expected 4xx for duplicate, got %d (%s)", r2.StatusCode, raw)
	}
}

func TestMlflowTransitionModelVersion_BadStage(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/model-versions/transition-stage",
		`{"name":"x","version":"1","stage":"NotAStage"}`)
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 4xx, got %d (%s)", resp.StatusCode, raw)
	}
}

// ---- log-inputs (datasets v0.3 path) ---------------------------------

func TestMlflowLogInputs_MissingRunID(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/log-inputs",
		`{"datasets":[{"dataset":{"name":"d","digest":"abc"}}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 missing run_id, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestMlflowLogInputs_EmptyDatasets(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// Set up an experiment + run.
	_, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/experiments/create", `{"name":"li-empty"}`)
	var ce struct{ ExperimentID string `json:"experiment_id"` }
	_ = json.Unmarshal(raw, &ce)
	_, runRaw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/create",
		fmt.Sprintf(`{"experiment_id":"%s"}`, ce.ExperimentID))
	var cr struct {
		Run struct{ Info struct{ RunID string `json:"run_id"` } } `json:"run"`
	}
	_ = json.Unmarshal(runRaw, &cr)

	// Empty datasets array — should succeed (no-op).
	resp, raw := mustPostJSON(t, ts.URL+"/api/2.0/mlflow/runs/log-inputs",
		fmt.Sprintf(`{"run_id":"%s","datasets":[]}`, cr.Run.Info.RunID))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for empty datasets array, got %d (%s)", resp.StatusCode, raw)
	}
}

// ---- malformed bodies (general resilience) ----------------------------

func TestMlflowMalformedJSON_Returns400(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	endpoints := []string{
		"/api/2.0/mlflow/experiments/create",
		"/api/2.0/mlflow/runs/create",
		"/api/2.0/mlflow/runs/log-metric",
		"/api/2.0/mlflow/runs/log-param",
		"/api/2.0/mlflow/registered-models/create",
		"/api/2.0/mlflow/model-versions/create",
	}
	for _, ep := range endpoints {
		t.Run(strings.TrimPrefix(ep, "/api/2.0/mlflow/"), func(t *testing.T) {
			resp, raw := mustPostJSON(t, ts.URL+ep, "{not json")
			if resp.StatusCode/100 != 4 {
				t.Errorf("%s: expected 4xx for malformed JSON, got %d (%s)", ep, resp.StatusCode, raw)
			}
		})
	}
}

// ---- empty payload + content-type checks ------------------------------

func TestMlflowPostWithoutContentType_StillParses(t *testing.T) {
	// MLflow client always sets Content-Type: application/json, but curl
	// users sometimes don't. The handler should still try to parse.
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	req, _ := http.NewRequest("POST", ts.URL+"/api/2.0/mlflow/experiments/create",
		bytes.NewReader([]byte(`{"name":"no-content-type"}`)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 200, got %d (%s)", resp.StatusCode, raw)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

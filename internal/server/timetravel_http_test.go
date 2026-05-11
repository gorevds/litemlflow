// HTTP-level test for v1.5 ?as_of=<unix_ms> on:
//   - GET /api/2.0/mlflow/runs/get
//   - GET /api/2.0/mlflow/metrics/get-history
//
// Acceptance: log a run, mutate, snapshot the run "as of" the
// pre-mutation timestamp via the API, verify pre-mutation values come
// back. Same for a single metric whose timestamp predates a later
// metric write.
package server_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/config"
)

// updateRun calls /api/2.0/mlflow/runs/update with optional fields. Empty
// values are skipped from the payload.
func updateRun(t *testing.T, ts, runID, status, name string, endTime int64) {
	t.Helper()
	body := map[string]any{"run_id": runID}
	if status != "" {
		body["status"] = status
	}
	if name != "" {
		body["run_name"] = name
	}
	if endTime != 0 {
		body["end_time"] = endTime
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts+"/api/2.0/mlflow/runs/update",
		"application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("update run: %d body=%s", resp.StatusCode, raw)
	}
}

// logMetric posts a metric to /api/2.0/mlflow/runs/log-metric.
func logMetric(t *testing.T, ts, runID, key string, value float64, ms, step int64) {
	t.Helper()
	body := fmt.Sprintf(`{"run_id":%q,"key":%q,"value":%v,"timestamp":%d,"step":%d}`,
		runID, key, value, ms, step)
	resp, err := http.Post(ts+"/api/2.0/mlflow/runs/log-metric",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("log metric: %d body=%s", resp.StatusCode, raw)
	}
}

// TestTimeTravelRunsGetAsOf — the headline acceptance for v1.5-rc1.
// Log a run, rename + finish it, query as-of pre-mutation timestamp via
// the standard MLflow runs/get endpoint, verify pre-mutation state.
func TestTimeTravelRunsGetAsOf(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt")
	runID := createRunReturning(t, ts.URL, expID, "")

	// Initial rename so the run has a name (createRunReturning didn't).
	updateRun(t, ts.URL, runID, "", "before-snapshot", 0)

	tBefore := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	// Mutate.
	updateRun(t, ts.URL, runID, "FINISHED", "after-snapshot", time.Now().UnixMilli())

	// Current state via runs/get without as_of: post-mutation.
	resp, err := http.Get(ts.URL + "/api/2.0/mlflow/runs/get?run_id=" + runID)
	if err != nil {
		t.Fatal(err)
	}
	curBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var cur runResp
	if err := json.Unmarshal(curBody, &cur); err != nil {
		t.Fatalf("decode current: %v", err)
	}
	if cur.Run.Info.RunName != "after-snapshot" {
		t.Errorf("current run_name: got %q want after-snapshot", cur.Run.Info.RunName)
	}
	if cur.Run.Info.Status != "FINISHED" {
		t.Errorf("current status: got %q want FINISHED", cur.Run.Info.Status)
	}

	// As-of tBefore: pre-mutation.
	resp, err = http.Get(fmt.Sprintf("%s/api/2.0/mlflow/runs/get?run_id=%s&as_of=%d",
		ts.URL, runID, tBefore))
	if err != nil {
		t.Fatal(err)
	}
	asOfBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("as-of status: %d body=%s", resp.StatusCode, asOfBody)
	}
	var asOf runResp
	if err := json.Unmarshal(asOfBody, &asOf); err != nil {
		t.Fatalf("decode as-of: %v", err)
	}
	if asOf.Run.Info.RunName != "before-snapshot" {
		t.Errorf("as-of run_name: got %q want before-snapshot", asOf.Run.Info.RunName)
	}
	if asOf.Run.Info.Status != "RUNNING" {
		t.Errorf("as-of status: got %q want RUNNING", asOf.Run.Info.Status)
	}
}

// TestTimeTravelRunsGetBeforeStartTime → 404. Uses a high start_time so
// we can query strictly below it (the as_of validator rejects 0/negative).
func TestTimeTravelRunsGetBeforeStartTime(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt-pre")

	body := fmt.Sprintf(`{"experiment_id":%q,"start_time":5000}`, expID)
	resp, err := http.Post(ts.URL+"/api/2.0/mlflow/runs/create",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created struct {
		Run struct {
			Info struct {
				ID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.Unmarshal(raw, &created)
	runID := created.Run.Info.ID
	if runID == "" {
		t.Fatalf("create run: %s", raw)
	}

	resp, err = http.Get(fmt.Sprintf("%s/api/2.0/mlflow/runs/get?run_id=%s&as_of=4999",
		ts.URL, runID))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("want 404 for as_of before run start, got %d (%s)", resp.StatusCode, raw)
	}
}

// TestTimeTravelMetricHistoryAsOf — metrics are append-only with native
// timestamps so as_of is a free filter. Verify the filter applies.
func TestTimeTravelMetricHistoryAsOf(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt-metrics")
	runID := createRunReturning(t, ts.URL, expID, "")

	// 3 metric points at different timestamps.
	logMetric(t, ts.URL, runID, "loss", 1.0, 1000, 0)
	logMetric(t, ts.URL, runID, "loss", 0.5, 2000, 1)
	logMetric(t, ts.URL, runID, "loss", 0.1, 3000, 2)

	// as_of=2000 should include points at ts=1000 and ts=2000, exclude ts=3000.
	url := fmt.Sprintf("%s/api/2.0/mlflow/metrics/get-history?run_id=%s&metric_key=loss&as_of=2000",
		ts.URL, runID)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got struct {
		Metrics []struct {
			Timestamp int64   `json:"timestamp"`
			Value     float64 `json:"value"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(got.Metrics) != 2 {
		t.Errorf("as_of=2000: want 2 points, got %d (%s)", len(got.Metrics), raw)
	}
	for _, m := range got.Metrics {
		if m.Timestamp > 2000 {
			t.Errorf("metric leaked past as_of cutoff: ts=%d", m.Timestamp)
		}
	}
}

// TestTimeTravelInvalidAsOf checks param validation.
func TestTimeTravelInvalidAsOf(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt-bad")
	runID := createRunReturning(t, ts.URL, expID, "")

	cases := []string{"as_of=abc", "as_of=-1", "as_of=0"}
	for _, q := range cases {
		url := fmt.Sprintf("%s/api/2.0/mlflow/runs/get?run_id=%s&%s", ts.URL, runID, q)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d (%s)", q, resp.StatusCode, raw)
		}
	}
}

// TestTimeTravelSearchRunsAsOf — v1.5 stable: search loop honors as_of.
// Creates two runs (one before T, one after), then searches with as_of=T.
// Only the pre-T run should appear; its name should be the pre-T value.
func TestTimeTravelSearchRunsAsOf(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt-search")

	// Run A: created before T, mutated after T → should appear with old name.
	body := fmt.Sprintf(`{"experiment_id":%q,"start_time":1000}`, expID)
	resp, err := http.Post(ts.URL+"/api/2.0/mlflow/runs/create",
		"application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var created struct {
		Run struct {
			Info struct {
				ID string `json:"run_id"`
			} `json:"info"`
		} `json:"run"`
	}
	_ = json.Unmarshal(raw, &created)
	runA := created.Run.Info.ID
	updateRun(t, ts.URL, runA, "", "alpha-before", 0)

	tBefore := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	updateRun(t, ts.URL, runA, "", "alpha-after", 0)

	// Run B: created after T → should be excluded from as_of=tBefore search.
	body = fmt.Sprintf(`{"experiment_id":%q,"start_time":%d}`, expID, tBefore+1000)
	resp, _ = http.Post(ts.URL+"/api/2.0/mlflow/runs/create",
		"application/json", strings.NewReader(body))
	resp.Body.Close()

	// Search with as_of=tBefore.
	url := fmt.Sprintf("%s/api/2.0/mlflow/runs/search?as_of=%d", ts.URL, tBefore)
	searchBody := fmt.Sprintf(`{"experiment_ids":[%q],"max_results":10}`, expID)
	resp, err = http.Post(url, "application/json", strings.NewReader(searchBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(resp.Body)
	var got struct {
		Runs []struct {
			Info struct {
				ID      string `json:"run_id"`
				RunName string `json:"run_name"`
			} `json:"info"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, raw)
	}
	if len(got.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d (%s)", len(got.Runs), raw)
	}
	if got.Runs[0].Info.ID != runA {
		t.Errorf("expected runA, got %q", got.Runs[0].Info.ID)
	}
	if got.Runs[0].Info.RunName != "alpha-before" {
		t.Errorf("expected name=alpha-before, got %q", got.Runs[0].Info.RunName)
	}
}

// TestTimeTravelFutureAsOfRejected — independent-review M1: an as_of
// timestamp clearly in the future should return 400, not silently alias
// to "now".
func TestTimeTravelFutureAsOfRejected(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})
	expID := createExperimentReturning(t, ts.URL, "v15-tt-future")
	runID := createRunReturning(t, ts.URL, expID, "")

	// 1 day in the future.
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	url := fmt.Sprintf("%s/api/2.0/mlflow/runs/get?run_id=%s&as_of=%d", ts.URL, runID, future)
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Errorf("future as_of: want 400, got %d (%s)", resp.StatusCode, raw)
	}
}

// runResp matches the MLflow GetRun response shape we care about.
type runResp struct {
	Run struct {
		Info struct {
			RunName string `json:"run_name"`
			Status  string `json:"status"`
		} `json:"info"`
	} `json:"run"`
}

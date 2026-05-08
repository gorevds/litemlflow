package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/litemlflow/litemlflow/internal/config"
)

// TestMetricsEndpointBasic hits /metrics twice and verifies:
//   - HTTP 200
//   - Content-Type is text/plain (OpenMetrics)
//   - At least 8 litemlflow_* metric families are present
//   - The second scrape contains a self-observation (method="GET", path="/metrics", status="200")
//
// Two scrapes are needed because the counter is incremented after the handler
// returns (middleware post-processing), so the first scrape sees 0; the second
// scrape sees the count from the first request.
func TestMetricsEndpointBasic(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// First scrape — warms up the counter.
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics (warm-up): %v", err)
	}
	resp.Body.Close()

	// Second scrape — the first request's observation is now recorded.
	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("want text/plain content-type, got %q", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	out := string(body)

	// Check that the mandatory EOF marker is present.
	if !strings.Contains(out, "# EOF") {
		t.Error("missing # EOF line")
	}

	// Count how many distinct litemlflow_* metric families appear via # HELP.
	families := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "# HELP litemlflow_") {
			families++
		}
	}
	if families < 8 {
		t.Errorf("expected >= 8 litemlflow_* metric families, found %d. Output:\n%s", families, out)
	}

	// The second scrape MUST contain the observation from the first /metrics
	// request: method="GET", path="/metrics", status="200".
	if !strings.Contains(out, "litemlflow_http_requests_total") {
		t.Errorf("expected litemlflow_http_requests_total in output:\n%s", out)
	}
	wantLine := `litemlflow_http_requests_total{method="GET",path="/metrics",status="200"} 1`
	if !strings.Contains(out, wantLine) {
		t.Errorf("expected self-observation line %q in second scrape:\n%s", wantLine, out)
	}
}

// TestMetricsEndpointSelfCount makes two requests to /metrics and verifies
// that litemlflow_http_requests_total grows between scrapes.
func TestMetricsEndpointSelfCount(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	scrape := func() string {
		resp, err := http.Get(ts.URL + "/metrics")
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	first := scrape()
	second := scrape()

	sum1 := sumCounterLines(t, first, "litemlflow_http_requests_total")
	sum2 := sumCounterLines(t, second, "litemlflow_http_requests_total")

	if sum2 <= sum1 {
		t.Errorf("counter did not increase between scrapes: first=%.0f second=%.0f", sum1, sum2)
	}
}

// TestMetricsPublicUnderBasicAuth verifies that /metrics is reachable without
// credentials when auth=basic is configured. Prometheus scrapers don't send
// credentials by default.
func TestMetricsPublicUnderBasicAuth(t *testing.T) {
	t.Parallel()
	pwHash := sha256.Sum256([]byte("s3cr3t"))
	cfg := config.Config{
		Auth:          "basic",
		BasicUser:     "bob",
		BasicPassHash: hex.EncodeToString(pwHash[:]),
	}
	ts, _ := newTestServer(t, cfg)

	// A protected endpoint should require auth.
	resp, _ := http.Get(ts.URL + "/api/2.0/mlflow/experiments/search")
	if resp.StatusCode != http.StatusUnauthorized {
		resp.Body.Close()
		t.Fatalf("protected endpoint: want 401, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// /metrics must be reachable WITHOUT credentials.
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics no-auth: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 on /metrics without creds, got %d body=%s", resp.StatusCode, body)
	}
}

// TestMetricsContainsExpectedFamilies checks for specific metric names that
// must always be present.
func TestMetricsContainsExpectedFamilies(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	required := []string{
		"litemlflow_http_requests_total",
		"litemlflow_http_request_duration_seconds",
		"litemlflow_runs_created_total",
		"litemlflow_metrics_logged_total",
		"litemlflow_active_sessions",
		"litemlflow_db_size_bytes",
		"litemlflow_build_info",
		"litemlflow_process_goroutines",
		"litemlflow_process_resident_memory_bytes",
	}
	for _, name := range required {
		if !strings.Contains(out, name) {
			t.Errorf("missing metric family %q in /metrics output", name)
		}
	}
}

// ---- helpers ---------------------------------------------------------------

// sumCounterLines sums the float values of all sample lines for the named
// metric family (skips HELP/TYPE/bucket/sum lines).
func sumCounterLines(t *testing.T, body, metricName string) float64 {
	t.Helper()
	var total float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, metricName) {
			continue
		}
		// Skip histogram bucket/sum/count sub-metrics that share the prefix.
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			if strings.HasPrefix(line, metricName+suffix) {
				goto next
			}
		}
		{
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				v, err := strconv.ParseFloat(parts[len(parts)-1], 64)
				if err == nil {
					total += v
				}
			}
		}
	next:
	}
	return total
}

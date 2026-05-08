package metrics_test

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/metrics"
)

// ---- Counter ---------------------------------------------------------------

func TestCounterIncrement(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	c := reg.Counter("test_counter_total", "a test counter", "method", "status")

	c.Inc("GET", "200")
	c.Inc("GET", "200")
	c.Inc("POST", "201")

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	// Expect GET/200 to appear with value 2.
	if !strings.Contains(out, `test_counter_total{method="GET",status="200"} 2`) {
		t.Errorf("expected GET/200=2 in output:\n%s", out)
	}
	// Expect POST/201 to appear with value 1.
	if !strings.Contains(out, `test_counter_total{method="POST",status="201"} 1`) {
		t.Errorf("expected POST/201=1 in output:\n%s", out)
	}
}

func TestCounterAdd(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	c := reg.Counter("bytes_total", "total bytes", "dir")
	c.Add(100, "in")
	c.Add(200, "in")
	c.Add(50, "out")

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `bytes_total{dir="in"} 300`) {
		t.Errorf("expected dir=in/300, got:\n%s", out)
	}
	if !strings.Contains(out, `bytes_total{dir="out"} 50`) {
		t.Errorf("expected dir=out/50, got:\n%s", out)
	}
}

func TestCounterNoLabels(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	c := reg.Counter("ops_total", "operations")
	c.Inc()
	c.Inc()

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ops_total 2") {
		t.Errorf("expected ops_total 2, got:\n%s", out)
	}
}

// TestCounterZeroEmitted checks that a counter with no observations still
// emits a zero line so the metric name appears in the output.
func TestCounterZeroEmitted(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	reg.Counter("absent_counter_total", "never incremented")

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "absent_counter_total 0") {
		t.Errorf("expected zero-line for absent counter, got:\n%s", out)
	}
}

// ---- Gauge -----------------------------------------------------------------

func TestGaugeSetGet(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	g := reg.Gauge("heap_bytes", "heap size")

	g.Set(1024)
	if got := g.Get(); got != 1024 {
		t.Fatalf("Get() = %v, want 1024", got)
	}

	g.Set(2048)
	if got := g.Get(); got != 2048 {
		t.Fatalf("after Set(2048) Get() = %v, want 2048", got)
	}
}

func TestGaugeAdd(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	g := reg.Gauge("conns", "connections")
	g.Set(10)
	g.Add(5)
	g.Add(-3)
	if got := g.Get(); got != 12 {
		t.Fatalf("Get() = %v, want 12", got)
	}
}

func TestGaugeInOutput(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	g := reg.Gauge("temperature", "degrees C")
	g.Set(36.6)

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "temperature 36.6") {
		t.Errorf("expected temperature 36.6, got:\n%s", out)
	}
}

// ---- Histogram -------------------------------------------------------------

func TestHistogramBuckets(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	h := reg.Histogram("latency_seconds", "request latency",
		[]float64{0.1, 0.5, 1.0},
	)

	// Observe a value exactly at a bucket boundary.
	h.Observe(0.1)   // lands in ≤0.1, ≤0.5, ≤1.0 buckets
	h.Observe(0.5)   // lands in ≤0.5, ≤1.0 buckets (not ≤0.1)
	h.Observe(0.05)  // lands in ≤0.1, ≤0.5, ≤1.0 buckets
	h.Observe(2.0)   // lands only in +Inf

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	// ≤0.1 bucket: observations 0.1, 0.05 → count 2
	if !strings.Contains(out, `latency_seconds_bucket{le="0.1"} 2`) {
		t.Errorf("le=0.1 count wrong:\n%s", out)
	}
	// ≤0.5 bucket: observations 0.1, 0.5, 0.05 → count 3
	if !strings.Contains(out, `latency_seconds_bucket{le="0.5"} 3`) {
		t.Errorf("le=0.5 count wrong:\n%s", out)
	}
	// ≤1.0 bucket: all 3 finite + 0.5 → count 3 (2.0 is excluded)
	if !strings.Contains(out, `latency_seconds_bucket{le="1"} 3`) {
		t.Errorf("le=1.0 count wrong:\n%s", out)
	}
	// +Inf bucket = total count = 4
	if !strings.Contains(out, `latency_seconds_bucket{le="+Inf"} 4`) {
		t.Errorf("+Inf count wrong:\n%s", out)
	}
	// sum = 0.1+0.5+0.05+2.0 = 2.65
	if !strings.Contains(out, "latency_seconds_sum 2.65") {
		t.Errorf("sum wrong:\n%s", out)
	}
	// count = 4
	if !strings.Contains(out, "latency_seconds_count 4") {
		t.Errorf("count wrong:\n%s", out)
	}
}

func TestHistogramInfBucket(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	h := reg.Histogram("req_seconds", "request duration",
		[]float64{0.1, 0.5},
	)
	// Value larger than all defined buckets → only in +Inf.
	h.Observe(999)

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `req_seconds_bucket{le="0.1"} 0`) {
		t.Errorf("le=0.1 should be 0:\n%s", out)
	}
	if !strings.Contains(out, `req_seconds_bucket{le="0.5"} 0`) {
		t.Errorf("le=0.5 should be 0:\n%s", out)
	}
	if !strings.Contains(out, `req_seconds_bucket{le="+Inf"} 1`) {
		t.Errorf("+Inf should be 1:\n%s", out)
	}
}

func TestHistogramWithLabels(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	h := reg.Histogram("rpc_duration_seconds", "rpc latency",
		[]float64{0.01, 0.1},
		"service",
	)
	h.Observe(0.005, "auth")
	h.Observe(0.05, "auth")
	h.Observe(0.2, "billing")

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, `rpc_duration_seconds_bucket{service="auth",le="0.01"} 1`) {
		t.Errorf("auth le=0.01 should be 1:\n%s", out)
	}
	if !strings.Contains(out, `rpc_duration_seconds_bucket{service="auth",le="0.1"} 2`) {
		t.Errorf("auth le=0.1 should be 2:\n%s", out)
	}
	if !strings.Contains(out, `rpc_duration_seconds_bucket{service="billing",le="+Inf"} 1`) {
		t.Errorf("billing +Inf should be 1:\n%s", out)
	}
}

// ---- WriteText format validation ------------------------------------------

var lineRE = regexp.MustCompile(`^[a-z_]+(\{[^}]*\})? [0-9.eE+\-NaIfn]+$`)

// TestWriteTextFormat checks that every sample line matches the OpenMetrics
// text line regex, and that HELP/TYPE headers are present.
func TestWriteTextFormat(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()

	c := reg.Counter("requests_total", "http requests", "method")
	c.Inc("GET")
	c.Inc("POST")

	g := reg.Gauge("active", "active connections")
	g.Set(5)

	h := reg.Histogram("size_bytes", "payload size", []float64{100, 1000})
	h.Observe(50)
	h.Observe(500)
	h.Observe(5000)

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "# HELP requests_total http requests") {
		t.Error("missing HELP line for requests_total")
	}
	if !strings.Contains(out, "# TYPE requests_total counter") {
		t.Error("missing TYPE line for requests_total")
	}
	if !strings.Contains(out, "# EOF") {
		t.Error("missing EOF line")
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !lineRE.MatchString(line) {
			t.Errorf("line does not match OpenMetrics format: %q", line)
		}
	}
}

// ---- Concurrency -----------------------------------------------------------

// TestConcurrentWrites verifies that concurrent counter increments and
// histogram observations do not race. Run with -race to detect data races.
func TestConcurrentWrites(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	c := reg.Counter("concurrent_total", "concurrency test", "worker")
	h := reg.Histogram("concurrent_hist", "concurrency hist", nil, "worker")

	const goroutines = 50
	const iters = 200

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			label := fmt.Sprintf("w%d", id%5)
			for j := 0; j < iters; j++ {
				c.Inc(label)
				h.Observe(float64(j)*0.001, label)
			}
		}(i)
	}
	wg.Wait()

	var buf bytes.Buffer
	if err := reg.WriteText(&buf); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
}

// TestConcurrentReadWrite runs WriteText while other goroutines are still
// incrementing counters — the output must not panic or corrupt.
func TestConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	reg := metrics.NewRegistry()
	c := reg.Counter("rw_total", "read-write race test")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					c.Inc()
				}
			}
		}()
	}

	// Readers.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					var buf bytes.Buffer
					_ = reg.WriteText(&buf)
				}
			}
		}()
	}

	// Let it run briefly.
	for i := 0; i < 100; i++ {
		var buf bytes.Buffer
		_ = reg.WriteText(&buf)
	}
	close(stop)
	wg.Wait()
}

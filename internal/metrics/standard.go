package metrics

import (
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/gorevds/litemlflow/pkg/version"
)

// Standard holds the pre-defined application metric set for LiteMLflow.
// Construct it with NewStandard and pass it around wherever metrics need to be
// incremented. All fields are safe for concurrent use.
type Standard struct {
	reg *Registry

	// HTTP instrumentation
	HTTPRequestsTotal          *Counter
	HTTPRequestDurationSeconds *Histogram

	// Business events
	RunsCreatedTotal   *Counter
	MetricsLoggedTotal *Counter

	// Resource gauges (refreshed on /metrics fetch)
	ActiveSessions *Gauge
	DBSizeBytes    *Gauge

	// Build info (always 1)
	BuildInfo *Gauge

	// Process gauges
	ProcessCPUSecondsTotal  *Gauge
	ProcessResidentMemBytes *Gauge
	ProcessOpenFDs          *Gauge
	ProcessGoroutines       *Gauge

	// dbPath is stored so RefreshProcess can stat the db file.
	dbPath string
}

// NewStandard registers all standard metrics on reg and returns them.
// dbPath is the path to the SQLite database file, used to compute
// litemlflow_db_size_bytes.
func NewStandard(reg *Registry, dbPath string) *Standard {
	s := &Standard{
		reg:    reg,
		dbPath: dbPath,
	}

	s.HTTPRequestsTotal = reg.Counter(
		"litemlflow_http_requests_total",
		"Total number of HTTP requests processed, partitioned by method, path template and status code.",
		"method", "path", "status",
	)
	s.HTTPRequestDurationSeconds = reg.Histogram(
		"litemlflow_http_request_duration_seconds",
		"HTTP request latency in seconds, partitioned by method and path template.",
		nil, // use DefaultHistogramBuckets
		"method", "path",
	)
	s.RunsCreatedTotal = reg.Counter(
		"litemlflow_runs_created_total",
		"Total number of experiment runs created.",
	)
	s.MetricsLoggedTotal = reg.Counter(
		"litemlflow_metrics_logged_total",
		"Total number of metric data-points logged (single + batch).",
	)
	s.ActiveSessions = reg.Gauge(
		"litemlflow_active_sessions",
		"Number of active user sessions currently stored in the session table.",
	)
	s.DBSizeBytes = reg.Gauge(
		"litemlflow_db_size_bytes",
		"Size of the SQLite database file in bytes (refreshed on each /metrics scrape).",
	)
	s.BuildInfo = reg.Gauge(
		"litemlflow_build_info",
		"Build metadata; value is always 1.",
	)
	s.ProcessCPUSecondsTotal = reg.Gauge(
		"litemlflow_process_cpu_seconds_total",
		"Total user and system CPU time spent in seconds.",
	)
	s.ProcessResidentMemBytes = reg.Gauge(
		"litemlflow_process_resident_memory_bytes",
		"Resident set size in bytes.",
	)
	s.ProcessOpenFDs = reg.Gauge(
		"litemlflow_process_open_fds",
		"Number of open file descriptors.",
	)
	s.ProcessGoroutines = reg.Gauge(
		"litemlflow_process_goroutines",
		"Number of goroutines currently running.",
	)

	// build_info is a fixed gauge — set it once at construction and it will
	// always appear in the output with value 1.  We encode version/commit as
	// a label-less gauge with a fixed name; the version string is emitted in
	// the HELP line for human readers.
	s.BuildInfo.Set(1)

	// Also register a labelled counter-style gauge for build_info with version
	// and commit labels so tooling can group by them.
	buildInfoLabelled := reg.Counter(
		"litemlflow_build_info_labels",
		"Build label set; value is always 1. Labels carry version and commit.",
		"version", "commit",
	)
	buildInfoLabelled.Add(1, version.Version, version.Commit)

	return s
}

// RefreshProcess updates process-level gauges from /proc/self (Linux) or
// runtime.MemStats on other platforms, and refreshes litemlflow_db_size_bytes.
// Call this once per /metrics request.
func (s *Standard) RefreshProcess() {
	s.ProcessGoroutines.Set(float64(runtime.NumGoroutine()))

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	// /proc/self/status is Linux-specific; use it when available for accurate
	// RSS; fall back to runtime heap stats otherwise.
	if rss, ok := readProcRSS(); ok {
		s.ProcessResidentMemBytes.Set(float64(rss))
	} else {
		s.ProcessResidentMemBytes.Set(float64(ms.Sys))
	}

	if cpu, ok := readProcCPU(); ok {
		s.ProcessCPUSecondsTotal.Set(cpu)
	}

	if fds, ok := countOpenFDs(); ok {
		s.ProcessOpenFDs.Set(float64(fds))
	}

	// DB size via os.Stat — cheap.
	if s.dbPath != "" {
		if fi, err := os.Stat(s.dbPath); err == nil {
			s.DBSizeBytes.Set(float64(fi.Size()))
		}
	}
}

// ---- /proc helpers ----------------------------------------------------------

// readProcRSS reads the VmRSS field from /proc/self/status.
// Returns (bytes, true) on success.
func readProcRSS() (int64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		// Format: "VmRSS:\t   12345 kB"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// readProcCPU parses /proc/self/stat to compute user+system CPU seconds.
func readProcCPU() (float64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// Fields are space-separated; field 14 (0-indexed 13) is utime (jiffies),
	// field 15 (0-indexed 14) is stime. We assume HZ=100.
	// The comm field (field 2) may contain spaces, so find it via parentheses.
	s := string(data)
	closeP := strings.LastIndex(s, ")")
	if closeP < 0 {
		return 0, false
	}
	after := strings.TrimSpace(s[closeP+1:])
	fields := strings.Fields(after)
	// After ')': state, ppid, pgrp, session, tty_nr, tpgid, flags, minflt,
	// cminflt, majflt, cmajflt, utime(idx11), stime(idx12)
	// So utime is at idx 11, stime at idx 12 (0-based within `after`).
	const utimeIdx = 11
	const stimeIdx = 12
	if len(fields) <= stimeIdx {
		return 0, false
	}
	utime, err := strconv.ParseInt(fields[utimeIdx], 10, 64)
	if err != nil {
		return 0, false
	}
	stime, err := strconv.ParseInt(fields[stimeIdx], 10, 64)
	if err != nil {
		return 0, false
	}
	const hz = 100
	return float64(utime+stime) / hz, true
}

// countOpenFDs counts the entries in /proc/self/fd.
func countOpenFDs() (int, bool) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return len(entries), true
}

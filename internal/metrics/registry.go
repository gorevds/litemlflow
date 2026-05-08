// Package metrics implements a tiny, zero-dependency Prometheus/OpenMetrics
// text exposition layer. It supports Counter, Gauge and Histogram metric
// families, each optionally labelled. The output follows the OpenMetrics text
// format (text/plain; version=0.0.4), which is accepted by all current
// Prometheus scrapers.
//
// Design decisions
//   - No reflection, no registration-by-struct tags. Every metric is created
//     explicitly via Registry.Counter / .Gauge / .Histogram.
//   - Label sets are keyed by a null-delimited string in declared key order so
//     lookup is O(1) after the one-time key build at observation time.
//   - sync.Mutex per metric family keeps the implementation simple; contention
//     is negligible for typical scrape intervals (≥ 1 s).
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DefaultHistogramBuckets are suitable for HTTP latency measured in seconds.
var DefaultHistogramBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry holds all registered metric families and serialises them on demand.
type Registry struct {
	mu         sync.Mutex
	counters   []*Counter
	gauges     []*Gauge
	histograms []*Histogram
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{} }

// Counter adds a new Counter family with the given label key names and returns
// it.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	c := &Counter{name: name, help: help, labelKeys: labels, values: map[string]float64{}}
	r.mu.Lock()
	r.counters = append(r.counters, c)
	r.mu.Unlock()
	return c
}

// Gauge adds a new scalar Gauge (no labels) and returns it.
func (r *Registry) Gauge(name, help string) *Gauge {
	g := &Gauge{name: name, help: help}
	r.mu.Lock()
	r.gauges = append(r.gauges, g)
	r.mu.Unlock()
	return g
}

// Histogram adds a new Histogram family. If buckets is nil,
// DefaultHistogramBuckets is used.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	if buckets == nil {
		buckets = DefaultHistogramBuckets
	}
	// Deduplicate and sort. +Inf is implicit; don't store it.
	seen := map[float64]bool{}
	unique := make([]float64, 0, len(buckets))
	for _, b := range buckets {
		if !seen[b] && !math.IsInf(b, 1) {
			seen[b] = true
			unique = append(unique, b)
		}
	}
	sort.Float64s(unique)

	h := &Histogram{
		name:      name,
		help:      help,
		labelKeys: labels,
		buckets:   unique,
		obs:       map[string]*histogramSample{},
	}
	r.mu.Lock()
	r.histograms = append(r.histograms, h)
	r.mu.Unlock()
	return h
}

// WriteText emits all registered metrics in OpenMetrics text format to w.
func (r *Registry) WriteText(w io.Writer) error {
	r.mu.Lock()
	counters := r.counters[:]
	gauges := r.gauges[:]
	histograms := r.histograms[:]
	r.mu.Unlock()

	for _, c := range counters {
		if err := c.writeTo(w); err != nil {
			return err
		}
	}
	for _, g := range gauges {
		if err := g.writeTo(w); err != nil {
			return err
		}
	}
	for _, h := range histograms {
		if err := h.writeTo(w); err != nil {
			return err
		}
	}
	// OpenMetrics requires the EOF marker.
	_, err := fmt.Fprintln(w, "# EOF")
	return err
}

// ---- Counter ---------------------------------------------------------------

// Counter is a monotonically increasing metric, optionally labelled.
type Counter struct {
	mu        sync.Mutex
	name      string
	help      string
	labelKeys []string
	values    map[string]float64 // labelSetKey → value
}

// Inc increments the counter for the given label values by 1.
func (c *Counter) Inc(labelValues ...string) { c.Add(1, labelValues...) }

// Add adds delta to the counter for the given label values.
func (c *Counter) Add(delta float64, labelValues ...string) {
	key := labelSetKey(c.labelKeys, labelValues)
	c.mu.Lock()
	c.values[key] += delta
	c.mu.Unlock()
}

func (c *Counter) writeTo(w io.Writer) error {
	c.mu.Lock()
	vals := make(map[string]float64, len(c.values))
	for k, v := range c.values {
		vals[k] = v
	}
	c.mu.Unlock()

	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n", c.name, c.help, c.name); err != nil {
		return err
	}
	keys := sortedStringKeys(vals)
	for _, k := range keys {
		labels := formatLabels(c.labelKeys, k)
		if _, err := fmt.Fprintf(w, "%s%s %s\n", c.name, labels, fmtFloat(vals[k])); err != nil {
			return err
		}
	}
	if len(vals) == 0 {
		if _, err := fmt.Fprintf(w, "%s 0\n", c.name); err != nil {
			return err
		}
	}
	return nil
}

// ---- Gauge -----------------------------------------------------------------

// Gauge is a scalar metric that can go up or down. No labels.
type Gauge struct {
	mu   sync.Mutex
	name string
	help string
	val  float64
}

// Set sets the gauge to v.
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.val = v
	g.mu.Unlock()
}

// Add adds delta to the gauge.
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	g.val += delta
	g.mu.Unlock()
}

// Get returns the current value.
func (g *Gauge) Get() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.val
}

func (g *Gauge) writeTo(w io.Writer) error {
	g.mu.Lock()
	v := g.val
	g.mu.Unlock()
	_, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n",
		g.name, g.help, g.name, g.name, fmtFloat(v))
	return err
}

// ---- Histogram -------------------------------------------------------------

type histogramSample struct {
	// counts[i] holds the number of observations ≤ buckets[i] (cumulative).
	counts []uint64
	sum    float64
	count  uint64
}

// Histogram tracks the distribution of observed values across configurable
// upper-bound buckets.
type Histogram struct {
	mu        sync.Mutex
	name      string
	help      string
	labelKeys []string
	buckets   []float64 // sorted, +Inf excluded
	obs       map[string]*histogramSample
}

// Observe records a single observation value with the supplied label values.
func (h *Histogram) Observe(value float64, labelValues ...string) {
	key := labelSetKey(h.labelKeys, labelValues)
	h.mu.Lock()
	s, ok := h.obs[key]
	if !ok {
		s = &histogramSample{counts: make([]uint64, len(h.buckets))}
		h.obs[key] = s
	}
	for i, b := range h.buckets {
		if value <= b {
			s.counts[i]++
		}
	}
	s.sum += value
	s.count++
	h.mu.Unlock()
}

func (h *Histogram) writeTo(w io.Writer) error {
	h.mu.Lock()
	type snap struct {
		key string
		s   histogramSample
	}
	snaps := make([]snap, 0, len(h.obs))
	for k, s := range h.obs {
		cp := histogramSample{
			counts: make([]uint64, len(s.counts)),
			sum:    s.sum,
			count:  s.count,
		}
		copy(cp.counts, s.counts)
		snaps = append(snaps, snap{k, cp})
	}
	h.mu.Unlock()

	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name); err != nil {
		return err
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].key < snaps[j].key })

	writeSnap := func(sn snap) error {
		labels := formatLabels(h.labelKeys, sn.key)
		for i, b := range h.buckets {
			bl := formatBucketLabels(h.labelKeys, sn.key, fmtFloat(b))
			if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, bl, sn.s.counts[i]); err != nil {
				return err
			}
		}
		// +Inf bucket equals total count.
		infL := formatBucketLabels(h.labelKeys, sn.key, "+Inf")
		if _, err := fmt.Fprintf(w, "%s_bucket%s %d\n", h.name, infL, sn.s.count); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_sum%s %s\n", h.name, labels, fmtFloat(sn.s.sum)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s_count%s %d\n", h.name, labels, sn.s.count); err != nil {
			return err
		}
		return nil
	}

	for _, sn := range snaps {
		if err := writeSnap(sn); err != nil {
			return err
		}
	}

	// Emit zero lines when there are no observations yet.
	if len(snaps) == 0 {
		for _, b := range h.buckets {
			if _, err := fmt.Fprintf(w, "%s_bucket{le=\"%s\"} 0\n", h.name, fmtFloat(b)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} 0\n%s_sum 0\n%s_count 0\n",
			h.name, h.name, h.name); err != nil {
			return err
		}
	}
	return nil
}

// ---- helpers ---------------------------------------------------------------

// labelSetKey builds a stable map key from parallel key/value slices.
// Format: "k1=v1\x00k2=v2\x00..." in declared key order.
func labelSetKey(keys, values []string) string {
	if len(keys) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('\x00')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		if i < len(values) {
			sb.WriteString(values[i])
		}
	}
	return sb.String()
}

// formatLabels converts a labelSetKey back into {k1="v1",k2="v2"} or "".
func formatLabels(keys []string, setKey string) string {
	if len(keys) == 0 || setKey == "" {
		return ""
	}
	parts := strings.Split(setKey, "\x00")
	var sb strings.Builder
	sb.WriteByte('{')
	for i, part := range parts {
		if i > 0 {
			sb.WriteByte(',')
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			sb.WriteString(part)
			continue
		}
		sb.WriteString(part[:eq])
		sb.WriteString(`="`)
		sb.WriteString(escapeLabelValue(part[eq+1:]))
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
	return sb.String()
}

// formatBucketLabels appends le="<le>" to a label set for histogram buckets.
func formatBucketLabels(keys []string, setKey, le string) string {
	if len(keys) == 0 || setKey == "" {
		return fmt.Sprintf(`{le="%s"}`, le)
	}
	existing := formatLabels(keys, setKey) // e.g. {method="GET",path="/foo"}
	// Insert le before the closing brace.
	return existing[:len(existing)-1] + `,le="` + le + `"}`
}

// escapeLabelValue escapes backslash, double-quote, and newline.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// sortedStringKeys returns the keys of a map in lexicographic order.
func sortedStringKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fmtFloat renders a float64 in a way Prometheus parsers accept.
func fmtFloat(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	if math.IsNaN(v) {
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

package webhooks

import (
	"strings"
	"sync"
	"time"
)

// EchoSchemePrefix marks webhook URLs that should be routed to the in-process
// echo ring buffer instead of dispatched as real HTTP. Format: lmf://echo
//
// Why: the live demo at lmf.gorev.space needs a way to show webhook deliveries
// without inviting users to set up an external receiver, and the SSRF defense
// blocks loopback URLs. The lmf:// scheme is a tiny escape hatch — never
// exposed to outbound network — that lets new operators see webhooks fire
// end-to-end with zero setup. In production, real webhooks use http(s)://.
const EchoSchemePrefix = "lmf://echo"

// EchoEntry is one recorded delivery in the in-process echo log.
type EchoEntry struct {
	Timestamp int64  `json:"timestamp"` // unix ms
	Event     string `json:"event"`
	WebhookID int64  `json:"webhook_id"`
	Body      string `json:"body"` // truncated JSON of the payload
	RunID     string `json:"run_id,omitempty"`
}

// EchoLog is a bounded ring buffer of recent webhook deliveries that targeted
// the lmf://echo URL. It is process-local: restarting the server clears it.
// Capacity is chosen to be large enough for a casual demo browsing session.
type EchoLog struct {
	mu      sync.Mutex
	entries []EchoEntry
	cap     int
}

// NewEchoLog returns a ring buffer with the given capacity. capacity <= 0
// defaults to 100.
func NewEchoLog(capacity int) *EchoLog {
	if capacity <= 0 {
		capacity = 100
	}
	return &EchoLog{cap: capacity}
}

// Record appends an entry. If the buffer is full the oldest entry is evicted.
func (l *EchoLog) Record(e EchoEntry) {
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	// Truncate body to avoid one giant payload monopolising memory.
	const maxBody = 4096
	if len(e.Body) > maxBody {
		e.Body = e.Body[:maxBody] + "…(truncated)"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
	if len(l.entries) > l.cap {
		l.entries = l.entries[len(l.entries)-l.cap:]
	}
}

// List returns up to max recent entries, newest first. max <= 0 means "all".
func (l *EchoLog) List(max int) []EchoEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.entries)
	if max > 0 && n > max {
		n = max
	}
	out := make([]EchoEntry, 0, n)
	// Walk newest first.
	for i := len(l.entries) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.entries[i])
	}
	return out
}

// IsEchoURL reports whether url is the special in-process echo target.
func IsEchoURL(url string) bool {
	return strings.HasPrefix(url, EchoSchemePrefix)
}

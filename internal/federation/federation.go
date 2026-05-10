// Package federation implements the v1.3 multi-server federation primitives:
//
//   - HMAC-signed HTTP client for outbound queries to peer instances
//   - Inbound-request validation helper
//   - Bounded in-memory response cache (30s TTL by default)
//
// Auth model: each peer relationship has a 32-byte secret known to BOTH
// servers. Outbound requests carry:
//
//	X-LiteMLflow-Federate-Sig: sha256=<hex(hmac(secret, body || method || path))>
//	X-LiteMLflow-Federate-Peer: <our_name>
//	X-LiteMLflow-Federate-Ts: <unix-ms>
//
// The receiver looks up the peer by name, recomputes the HMAC, and checks
// it matches in constant time. Timestamp drift > 5 minutes is rejected
// (replay defence with operator-friendly clock skew).
//
// Caching is keyed by (peer_id, query_hash) and bounded to 256 entries
// FIFO-style (oldest evicted on insert overflow). 30s TTL is the roadmap
// constant; tunable by reconstructing federation.NewCache(cap, ttl).
package federation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Header names for the federation transport.
const (
	HeaderSignature = "X-LiteMLflow-Federate-Sig"
	HeaderPeer      = "X-LiteMLflow-Federate-Peer"
	HeaderTimestamp = "X-LiteMLflow-Federate-Ts"

	// DefaultClockSkewMS is the maximum accepted timestamp drift between
	// the federating peer and us. Operator-friendly: NTP-ish hosts stay
	// well inside this; cron jobs that drifted 30 minutes need explicit
	// reconfiguration (which is the point).
	DefaultClockSkewMS = int64(5 * 60 * 1000)

	// DefaultCacheTTL is the per-entry lifetime of cached peer responses.
	DefaultCacheTTL = 30 * time.Second
)

// ErrSignatureMismatch is returned when an inbound request has a header
// that doesn't validate. The handler maps it to 401.
var (
	ErrSignatureMismatch = errors.New("federation signature mismatch")
	ErrPeerUnknown       = errors.New("federation peer unknown")
	ErrTimestampStale    = errors.New("federation timestamp out of clock-skew window")
	ErrSecretFormat      = errors.New("federation secret must be 64 hex chars")
	ErrResponseTooLarge  = errors.New("federation peer response exceeds size limit")
)

// NewSecret returns a 32-byte cryptographic random secret as 64 hex chars.
// Used when adding a new peer locally; the same value must be configured
// on the remote.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// signRequest computes the X-LiteMLflow-Federate-Sig value over the
// canonical request representation. Caller passes the literal body bytes
// (not a streaming reader) so the same value can be re-validated by the
// receiver after Read.
//
// Canonical input:
//
//	"<method>\n<path>\n<timestampMS>\n<body>"
//
// We include method+path so a body-less GET can't be replayed by switching
// the verb to DELETE.
func signRequest(secret []byte, method, path string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s\n%s\n%d\n", method, path, ts)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyRequest validates an inbound federation request.
//
//	peerName — the X-LiteMLflow-Federate-Peer header value
//	secretHex — the per-peer HMAC secret looked up locally by peerName
//	method, path — request method and URL.Path
//	ts — parsed timestamp header
//	body — full request body, already read into memory
//	sigHeader — the signature header value from the request
//
// Returns nil on success, one of the Err* sentinels otherwise.
func VerifyRequest(secretHex, method, path string, ts int64, body []byte, sigHeader string) error {
	secret, err := hex.DecodeString(secretHex)
	if err != nil || len(secret) != 32 {
		return ErrSecretFormat
	}
	now := time.Now().UnixMilli()
	if abs64(now-ts) > DefaultClockSkewMS {
		return ErrTimestampStale
	}
	expected := signRequest(secret, method, path, ts, body)
	if !hmac.Equal([]byte(expected), []byte(sigHeader)) {
		return ErrSignatureMismatch
	}
	return nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Client is an HTTP client that signs every outbound request with the
// peer-relationship's HMAC secret.
//
// Thread-safe; one Client per peer.
type Client struct {
	httpClient *http.Client
	baseURL    string
	ourName    string
	secret     []byte
}

// NewClient builds a federation client for one peer.
//
//	baseURL      — peer's API root (e.g. "https://lmf.team-b.example/")
//	ourName      — what we want the peer's logs/headers to call us
//	secretHex    — 64-hex-char HMAC secret shared with the peer
//
// timeout defaults to 5s if zero — federation requests should be fast.
func NewClient(baseURL, ourName, secretHex string, timeout time.Duration) (*Client, error) {
	secret, err := hex.DecodeString(secretHex)
	if err != nil || len(secret) != 32 {
		return nil, ErrSecretFormat
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    strings.TrimRight(baseURL, "/"),
		ourName:    ourName,
		secret:     secret,
	}, nil
}

// MaxResponseBytes caps how much of a peer's response body we will read.
// Federation responses are small JSON arrays of search hits; anything past
// this is either misconfiguration or a hostile peer trying to OOM us.
const MaxResponseBytes = 8 << 20 // 8 MiB

// Do performs a signed request against the peer. The body is fully read
// into memory so the signature can be computed and the same bytes can
// be re-played on retry. method should be one of GET/POST.
//
// Response bodies above MaxResponseBytes are rejected (see ErrResponseTooLarge).
func (c *Client) Do(method, path string, body []byte) (*http.Response, []byte, error) {
	return c.DoCtx(context.Background(), method, path, body)
}

// DoCtx is Do with a caller-controlled context — used so an inbound
// /api/v1/search request that is cancelled propagates cancellation to
// any in-flight peer fetches it triggered.
func (c *Client) DoCtx(ctx context.Context, method, path string, body []byte) (*http.Response, []byte, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, nil, fmt.Errorf("federation: path must be absolute, got %q", path)
	}
	ts := time.Now().UnixMilli()
	sig := signRequest(c.secret, method, path, ts, body)

	url := c.baseURL + path
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, c.ourName)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, sig)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	respBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(respBody)) > MaxResponseBytes {
		return nil, nil, ErrResponseTooLarge
	}
	return resp, respBody, nil
}

// ----- Cache -----

// cacheEntry is one stored response.
type cacheEntry struct {
	at      time.Time
	payload []byte
}

// Cache is a bounded in-memory cache for federation responses. Keyed on
// (peer_id, query_hash). Concurrent-safe.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	keys    []string // for FIFO eviction when len > capacity
	cap     int
	ttl     time.Duration
}

// NewCache returns a cache with capacity entries and TTL per entry.
// capacity ≤ 0 → 256.  ttl ≤ 0 → DefaultCacheTTL.
func NewCache(capacity int, ttl time.Duration) *Cache {
	if capacity <= 0 {
		capacity = 256
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &Cache{
		entries: make(map[string]cacheEntry, capacity),
		keys:    make([]string, 0, capacity),
		cap:     capacity,
		ttl:     ttl,
	}
}

// Key derives the cache key from peer ID + a query hash. The caller is
// responsible for hashing the query input deterministically (e.g.
// sha256(json.Marshal(req))).
func Key(peerID int64, queryHash string) string {
	return strconv.FormatInt(peerID, 10) + ":" + queryHash
}

// Get returns the cached payload if present and not expired.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > c.ttl {
		delete(c.entries, key)
		// Eagerly drop the key from the FIFO slice too. Otherwise a later
		// Put of the same key appends a duplicate, and the next eviction
		// can wipe the fresh entry while leaving the stale slot empty.
		for i, k := range c.keys {
			if k == key {
				c.keys = append(c.keys[:i], c.keys[i+1:]...)
				break
			}
		}
		return nil, false
	}
	return e.payload, true
}

// Put stores payload at key. Evicts the oldest entry on overflow.
func (c *Cache) Put(key string, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.keys = append(c.keys, key)
		// FIFO eviction.
		for len(c.keys) > c.cap {
			oldest := c.keys[0]
			c.keys = c.keys[1:]
			delete(c.entries, oldest)
		}
	}
	c.entries[key] = cacheEntry{at: time.Now(), payload: append([]byte(nil), payload...)}
}

// Reset is for tests that need a clean cache between cases.
func (c *Cache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry, c.cap)
	c.keys = c.keys[:0]
}

// ---------------------------------------------------------------------------
// Helpers shared between client + server: query-hash derivation.
// ---------------------------------------------------------------------------

// QueryHash returns a deterministic hex digest of an arbitrary JSON-shaped
// query (the federated search request). Stable across goroutines: the
// underlying json.Marshal sorts map keys.
func QueryHash(query any) string {
	b, _ := json.Marshal(query)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

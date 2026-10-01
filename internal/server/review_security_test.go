package server

// White-box regression tests for rate-limiter eviction/keying and RBAC
// fail-closed behaviour.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// A bucket left partially drained must still be evicted once it has been idle
// long enough to have refilled; previously the stale stored token count was
// compared, so such buckets lived forever (unbounded growth under IP churn).
func TestAuthRateLimiterSweepsPartiallyDrainedBuckets(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := newAuthRateLimiter(5, 1.0/12.0)
	rl.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		rl.allow("10.0.0." + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(rl.buckets) != 100 {
		t.Fatalf("setup: want 100 buckets, got %d", len(rl.buckets))
	}
	now = now.Add(10 * time.Minute) // 1 token per 12s ⇒ fully refilled
	rl.allow("trigger")
	if len(rl.buckets) != 1 {
		t.Fatalf("idle refilled buckets should be swept; %d remain", len(rl.buckets))
	}
}

func TestClientIPKeysIPv6By64(t *testing.T) {
	mk := func(addr string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = addr
		return r
	}
	a := clientIP(mk("[2001:db8:1:2:aaaa::1]:5555"))
	b := clientIP(mk("[2001:db8:1:2:ffff:1:2:3]:6666"))
	c := clientIP(mk("[2001:db8:1:3::1]:5555"))
	if a != b {
		t.Fatalf("same /64 must share a key: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different /64 must not share a key: %q", a)
	}
	if got := clientIP(mk("192.0.2.7:1234")); got != "192.0.2.7" {
		t.Fatalf("ipv4 key: got %q", got)
	}
	if got := clientIP(mk("[::ffff:192.0.2.7]:1234")); got != "192.0.2.7" {
		t.Fatalf("v4-mapped key: got %q", got)
	}
}

type listMembersFailStore struct{ store.Store }

func (listMembersFailStore) ListMembers(context.Context, string) ([]*model.WorkspaceMember, error) {
	return nil, errors.New("db down")
}

// A membership-lookup error on the default workspace must not be treated as
// "no members" (open mode) — that silently disabled RBAC.
func TestRBACFailsClosedOnMembershipError(t *testing.T) {
	called := false
	h := rbacMiddleware(config.Config{Auth: "basic"}, listMembersFailStore{})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "/api/2.0/mlflow/experiments/create", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUser, "eve"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called || rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 and no pass-through; got %d called=%v", rec.Code, called)
	}
}

func TestClientKeyTrustedProxies(t *testing.T) {
	trusted, err := config.ParseTrustedProxies("10.0.0.0/8, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	rl := newAuthRateLimiter(5, 1)
	mk := func(remote, xff, xri string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xri != "" {
			r.Header.Set("X-Real-IP", xri)
		}
		return r
	}
	// No trusted proxies configured: headers ignored.
	if got := rl.clientKey(mk("127.0.0.1:1", "203.0.113.9", "")); got != "127.0.0.1" {
		t.Fatalf("untrusted mode must ignore XFF; got %q", got)
	}
	rl.trusted = trusted
	cases := []struct{ remote, xff, xri, want string }{
		{"127.0.0.1:1", "203.0.113.9", "", "203.0.113.9"},
		// Left-most (client-supplied) entries are not believed.
		{"127.0.0.1:1", "6.6.6.6, 203.0.113.9, 10.1.2.3", "", "203.0.113.9"},
		{"127.0.0.1:1", "", "198.51.100.4", "198.51.100.4"},
		// Untrusted peer: its headers are ignored.
		{"192.0.2.50:1", "203.0.113.9", "1.1.1.1", "192.0.2.50"},
		// Chain made only of trusted hops falls back to the peer.
		{"10.0.0.1:1", "10.0.0.2", "", "10.0.0.1"},
	}
	for _, c := range cases {
		if got := rl.clientKey(mk(c.remote, c.xff, c.xri)); got != c.want {
			t.Errorf("remote=%s xff=%q xri=%q: got %q want %q", c.remote, c.xff, c.xri, got, c.want)
		}
	}
	if _, err := config.ParseTrustedProxies("not-an-ip"); err == nil {
		t.Fatal("invalid proxy entry must be rejected")
	}
}

func TestMetricsMethodBounded(t *testing.T) {
	if metricsMethod("GET") != "GET" || metricsMethod("XYZZY-123") != "OTHER" {
		t.Fatal("non-standard methods must collapse to OTHER")
	}
}

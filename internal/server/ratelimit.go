package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// authRateLimiter is a per-client-IP token bucket guarding credential endpoints
// against brute force (independent-review P2). Buckets refill continuously at
// `refill` tokens/sec up to `capacity`; idle buckets are evicted on a lazy
// periodic sweep so memory stays bounded under a churn of distinct IPs.
type authRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	capacity  float64
	refill    float64 // tokens per second
	now       func() time.Time
	lastSweep time.Time
	// trusted lists reverse proxies whose X-Forwarded-For / X-Real-IP are
	// believed (config TrustedProxies). Empty = RemoteAddr only.
	trusted []netip.Prefix
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newAuthRateLimiter(capacity, refillPerSec float64) *authRateLimiter {
	return &authRateLimiter{
		buckets:  map[string]*tokenBucket{},
		capacity: capacity,
		refill:   refillPerSec,
		now:      time.Now,
	}
}

// allow consumes one token for key, returning false when the bucket is empty.
func (rl *authRateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	b := rl.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: rl.capacity, last: now}
		rl.buckets[key] = b
	} else {
		b.tokens = min(rl.capacity, b.tokens+now.Sub(b.last).Seconds()*rl.refill)
		b.last = now
	}
	rl.sweep(now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep evicts buckets idle long enough to have refilled to capacity — they
// carry no rate-limiting state worth keeping. Runs at most once per minute.
// Caller holds rl.mu.
func (rl *authRateLimiter) sweep(now time.Time) {
	if now.Sub(rl.lastSweep) < time.Minute {
		return
	}
	rl.lastSweep = now
	for k, b := range rl.buckets {
		// b.tokens is only updated on access, so project the refill forward
		// to now. Comparing the stale stored value would never evict a bucket
		// that was left partially drained — an attacker rotating source
		// addresses (one failed attempt each) would grow the map without bound.
		idle := now.Sub(b.last)
		if idle > time.Minute && b.tokens+idle.Seconds()*rl.refill >= rl.capacity {
			delete(rl.buckets, k)
		}
	}
}

// rateLimitAuthMiddleware throttles credential-submission endpoints per client
// IP. Non-auth paths pass through untouched. It must run after
// apiV2AliasMiddleware so /api/v2/... has already been rewritten to /api/v1/...
func rateLimitAuthMiddleware(rl *authRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isRateLimitedAuthPath(r) && !rl.allow(rl.clientKey(r)) {
				w.Header().Set("Retry-After", "60")
				writeError(w, http.StatusTooManyRequests, CodeTooManyRequests,
					"too many authentication attempts; slow down and retry later")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isRateLimitedAuthPath matches the password-submission endpoint, the brute
// force vector. OIDC start/callback are not password oracles (callback is
// bound to a server-issued state) and logout/whoami are benign.
func isRateLimitedAuthPath(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login"
}

// clientIP returns the rate-limit key for the remote peer, derived from
// RemoteAddr only. Forwarded headers are honoured solely via
// authRateLimiter.clientKey when the peer is a configured trusted proxy: a
// spoofable header from an arbitrary client would let an attacker rotate the
// rate-limit key trivially.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	return ipKey(ip)
}

// ipKey normalises an address into a rate-limit key. IPv6 clients are keyed
// by their /64 prefix: a single end site is routinely delegated a whole /64
// (or larger), so keying on the full /128 would let one attacker rotate
// through 2^64 source addresses and never be throttled.
func ipKey(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if ip.Is4() {
		return ip.String()
	}
	p, _ := ip.Prefix(64)
	return p.String()
}

func isTrusted(trusted []netip.Prefix, ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, p := range trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientKey resolves the rate-limit key for r. When RemoteAddr is a trusted
// proxy, the real client is the right-most X-Forwarded-For hop that is NOT
// itself a trusted proxy (left-most entries are client-controlled and must
// not be believed); X-Real-IP is used when XFF is absent. Otherwise — and
// always when no proxies are configured — RemoteAddr is used.
func (rl *authRateLimiter) clientKey(r *http.Request) string {
	if len(rl.trusted) == 0 {
		return clientIP(r)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil || !isTrusted(rl.trusted, remote) {
		return clientIP(r)
	}
	if xffs := r.Header.Values("X-Forwarded-For"); len(xffs) > 0 {
		hops := strings.Split(strings.Join(xffs, ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			ip, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break // malformed hop: stop trusting the chain
			}
			if !isTrusted(rl.trusted, ip) {
				return ipKey(ip)
			}
		}
	} else if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return ipKey(ip)
	}
	return ipKey(remote)
}

package server

import (
	"net"
	"net/http"
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
		if now.Sub(b.last) > time.Minute && b.tokens >= rl.capacity {
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
			if isRateLimitedAuthPath(r) && !rl.allow(clientIP(r)) {
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

// clientIP returns the remote IP without the port. We deliberately use
// RemoteAddr rather than X-Forwarded-For: a spoofable header would let an
// attacker rotate the rate-limit key trivially. Deployments behind a trusted
// proxy should rate-limit at the proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

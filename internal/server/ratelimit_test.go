package server

// White-box tests for the auth rate limiter (independent-review P2): credential
// endpoints must throttle per-client brute force. Uses an injected clock so the
// refill behavior is deterministic (no sleeps).

import (
	"testing"
	"time"
)

func TestAuthRateLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newAuthRateLimiter(3, 1) // capacity 3, refill 1 token/sec
	rl.now = func() time.Time { return now }

	// Burst of 3 is allowed, the 4th is denied.
	for i := 0; i < 3; i++ {
		if !rl.allow("1.2.3.4") {
			t.Fatalf("attempt %d should be allowed within burst", i+1)
		}
	}
	if rl.allow("1.2.3.4") {
		t.Fatal("4th attempt should be denied (bucket empty)")
	}

	// After 1 second one token refills → one more allowed, then denied again.
	now = now.Add(1 * time.Second)
	if !rl.allow("1.2.3.4") {
		t.Fatal("attempt after 1s refill should be allowed")
	}
	if rl.allow("1.2.3.4") {
		t.Fatal("attempt should be denied again (only one token refilled)")
	}
}

func TestAuthRateLimiterPerKeyIsolation(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newAuthRateLimiter(2, 1)
	rl.now = func() time.Time { return now }

	// Assign to vars (evaluated left-to-right) so each call's effect is
	// distinct — staticcheck flags textually identical `x || x` operands.
	a1, a2, a3 := rl.allow("a"), rl.allow("a"), rl.allow("a")
	if !a1 || !a2 || a3 {
		t.Fatal("key a should allow exactly 2 then deny")
	}
	// A different key has its own independent bucket.
	b1, b2 := rl.allow("b"), rl.allow("b")
	if !b1 || !b2 {
		t.Fatal("key b should have its own full bucket")
	}
}

func TestAuthRateLimiterCapsAtCapacity(t *testing.T) {
	now := time.Unix(0, 0)
	rl := newAuthRateLimiter(3, 1)
	rl.now = func() time.Time { return now }

	rl.allow("x") // consume 1
	// Idle a long time; refill must not exceed capacity.
	now = now.Add(1 * time.Hour)
	for i := 0; i < 3; i++ {
		if !rl.allow("x") {
			t.Fatalf("attempt %d should be allowed (refilled to capacity)", i+1)
		}
	}
	if rl.allow("x") {
		t.Fatal("tokens must cap at capacity, not accumulate beyond it")
	}
}

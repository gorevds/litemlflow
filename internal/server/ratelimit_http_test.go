package server_test

// HTTP-level guard for the auth rate limiter: rapid POSTs to the login
// endpoint from one client must eventually get 429 with Retry-After.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

func TestLoginEndpointRateLimited(t *testing.T) {
	ts, _ := newTestServer(t, config.Config{}) // auth=none; limiter runs regardless
	client := &http.Client{}

	got429 := false
	allowed := 0
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"x","password":"y"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			if resp.Header.Get("Retry-After") == "" {
				t.Error("429 response missing Retry-After header")
			}
			break
		}
		allowed++
	}
	if !got429 {
		t.Fatalf("expected a 429 within 10 rapid login attempts; got %d allowed", allowed)
	}
	// The burst (5) should let the first attempts through before throttling.
	if allowed == 0 {
		t.Error("limiter rejected the very first attempt; burst not honored")
	}
}

// TestBasicAuthBruteForceRateLimited closes the scope gap where basic-auth
// credentials are checked on every request: failed Basic attempts must charge
// the same per-IP limiter (so brute force via any endpoint is throttled), while
// a correct credential is never blocked even after the budget is spent.
func TestBasicAuthBruteForceRateLimited(t *testing.T) {
	sum := sha256.Sum256([]byte("correct-horse"))
	cfg := config.Config{
		Auth:          "basic",
		BasicUser:     "admin",
		BasicPassHash: hex.EncodeToString(sum[:]),
	}
	ts, _ := newTestServer(t, cfg)
	client := &http.Client{}

	// Hammer a protected endpoint with WRONG credentials.
	got429 := false
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/whoami", nil)
		req.SetBasicAuth("admin", "wrong-guess")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong-cred attempt %d: got %d, want 401 (or eventual 429)", i, resp.StatusCode)
		}
	}
	if !got429 {
		t.Fatal("expected failed basic-auth attempts to be rate-limited (429)")
	}

	// A correct credential must still succeed despite the drained bucket:
	// successful auth never consumes a token.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/auth/whoami", nil)
	req.SetBasicAuth("admin", "correct-horse")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("correct-cred request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Error("correct credentials were rate-limited; only failures should consume tokens")
	}
}

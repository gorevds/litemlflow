package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnsureDiscoverySingleFlight guards independent-review: concurrent callers
// must trigger exactly one discovery+JWKS fetch, not one per caller.
func TestEnsureDiscoverySingleFlight(t *testing.T) {
	var discoveryHits int32
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&discoveryHits, 1)
		time.Sleep(25 * time.Millisecond) // widen the race window
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/auth",
			"token_endpoint":         base + "/token",
			"jwks_uri":               base + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	p := NewProvider(base, "cid", "", base+"/cb", nil)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.EnsureDiscovery(context.Background()); err != nil {
				t.Errorf("EnsureDiscovery: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&discoveryHits); n != 1 {
		t.Errorf("discovery fetched %d times, want 1 (single-flight)", n)
	}
}

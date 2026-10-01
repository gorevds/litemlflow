package auth

// Regression tests: loopback-HTTP allowance must match the host exactly, and
// an IdP signing-key rotation must be picked up without a restart.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRequireSecureURLExactLoopbackHost(t *testing.T) {
	ok := []string{
		"https://idp.example.com",
		"http://127.0.0.1:8080/x",
		"http://localhost/.well-known",
		"http://[::1]:9000",
	}
	bad := []string{
		"http://localhost.evil.com/",
		"http://127.0.0.1.evil.com/",
		"http://localhost@evil.com/",
		"http://localhost:80@evil.com/",
		"http://[::1].evil.com/",
		"http://idp.example.com",
		"ftp://localhost/",
		"https:///nohost",
	}
	for _, u := range ok {
		if err := requireSecureURL("x", u); err != nil {
			t.Errorf("%s: unexpected error %v", u, err)
		}
	}
	for _, u := range bad {
		if err := requireSecureURL("x", u); err == nil {
			t.Errorf("%s: must be rejected", u)
		}
	}
}

func TestExchangeRefreshesJWKSOnKeyRotation(t *testing.T) {
	const clientID = "rot-client"
	oldKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	b64 := base64.RawURLEncoding.EncodeToString

	var mu sync.Mutex
	active, activeKid := oldKey, "old"
	jwksHits := 0

	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer": base, "authorization_endpoint": base + "/auth",
			"token_endpoint": base + "/token", "jwks_uri": base + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		jwksHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": activeKid, "alg": "RS256", "use": "sig",
			"n": b64(active.N.Bytes()), "e": b64(big.NewInt(int64(active.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		key, kid := active, activeKid
		mu.Unlock()
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
		cl, _ := json.Marshal(map[string]any{"iss": base, "aud": clientID, "sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
		in := b64(hdr) + "." + b64(cl)
		d := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": in + "." + b64(sig)})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	p := NewProvider(srv.URL, clientID, "", srv.URL+"/cb", nil)
	if _, _, err := p.Exchange(t.Context(), "c", "v", ""); err != nil {
		t.Fatalf("initial exchange: %v", err)
	}

	// IdP rotates its signing key.
	mu.Lock()
	active, activeKid = newKey, "new"
	mu.Unlock()

	// Within the rate-limit window a refresh is not attempted.
	if _, _, err := p.Exchange(t.Context(), "c", "v", ""); err == nil {
		t.Fatal("expected failure inside the refresh rate-limit window")
	}
	// Once the window has elapsed, the unknown kid triggers a refetch.
	p.mu.Lock()
	p.jwksFetchedAt = time.Now().Add(-2 * jwksRefreshMinInterval)
	p.mu.Unlock()
	if _, _, err := p.Exchange(t.Context(), "c", "v", ""); err != nil {
		t.Fatalf("exchange after key rotation: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if jwksHits != 2 {
		t.Fatalf("want exactly 2 JWKS fetches (initial + one refresh), got %d", jwksHits)
	}
}

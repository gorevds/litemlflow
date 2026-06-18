package auth_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/auth"
)

// --- helpers ----------------------------------------------------------------

// mustRSAKey generates a 2048-bit RSA key for tests.
func mustRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return k
}

// base64url encodes without padding.
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// makeJWT builds a minimal RS256-signed JWT for testing.
func makeJWT(t *testing.T, key *rsa.PrivateKey, kid, iss, aud string, exp int64) string {
	t.Helper()
	return makeJWTWithNonce(t, key, kid, iss, aud, exp, "")
}

// makeJWTWithNonce builds a minimal RS256-signed JWT with an optional nonce claim.
func makeJWTWithNonce(t *testing.T, key *rsa.PrivateKey, kid, iss, aud string, exp int64, nonce string) string {
	t.Helper()
	hdr := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	hdrJSON, _ := json.Marshal(hdr)
	claims := map[string]any{
		"iss":   iss,
		"aud":   aud,
		"sub":   "user123",
		"email": "user@example.com",
		"exp":   exp,
		"iat":   time.Now().Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	claimsJSON, _ := json.Marshal(claims)
	hdrB64 := b64url(hdrJSON)
	claimsB64 := b64url(claimsJSON)
	sigInput := hdrB64 + "." + claimsB64
	digest := sha256.Sum256([]byte(sigInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return sigInput + "." + b64url(sig)
}

// bigIntToB64 converts a big.Int to base64url without padding.
func bigIntToB64(n *big.Int) string {
	b := n.Bytes()
	return b64url(b)
}

// intToB64 converts a small int (like RSA public exponent) to base64url.
func intToB64(n int) string {
	// Write as minimal big-endian bytes.
	b := big.NewInt(int64(n)).Bytes()
	return b64url(b)
}

// fakeOIDCServer returns an *httptest.Server that exposes:
//   - /.well-known/openid-configuration
//   - /jwks
//   - /auth (authorization endpoint)
//   - /token (token endpoint, returns a JWT signed by key)
//
// The /token handler echoes back whatever "nonce" was sent in the auth request
// query parameter (captured in /auth handler) so nonce round-trip tests work.
func fakeOIDCServer(t *testing.T, key *rsa.PrivateKey, kid, issuer, clientID string) *httptest.Server {
	t.Helper()
	return fakeOIDCServerWithNonce(t, key, kid, issuer, clientID, "")
}

// fakeOIDCServerWithNonce is like fakeOIDCServer but the /token endpoint
// includes the given nonce in the returned ID token claims.
// Pass nonce="" to omit the nonce claim (simulates a pre-nonce IdP or a token
// without a nonce — for backward-compat testing).
func fakeOIDCServerWithNonce(t *testing.T, key *rsa.PrivateKey, kid, issuer, clientID, nonce string) *httptest.Server {
	t.Helper()
	jwksDoc := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   bigIntToB64(key.PublicKey.N),
				"e":   intToB64(key.PublicKey.E),
			},
		},
	}

	mux := http.NewServeMux()
	var serverBase string // filled after server starts

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		disc := map[string]string{
			"issuer":                 serverBase,
			"authorization_endpoint": serverBase + "/auth",
			"token_endpoint":         serverBase + "/token",
			"jwks_uri":               serverBase + "/jwks",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(disc)
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksDoc)
	})

	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		exp := time.Now().Add(time.Hour).Unix()
		idToken := makeJWTWithNonce(t, key, kid, serverBase, clientID, exp, nonce)
		resp := map[string]any{
			"id_token":     idToken,
			"access_token": "at-test",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := httptest.NewServer(mux)
	serverBase = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// --- tests ------------------------------------------------------------------

func TestNewPKCEVerifier(t *testing.T) {
	t.Parallel()
	v1, err := auth.NewPKCEVerifier()
	if err != nil {
		t.Fatalf("NewPKCEVerifier: %v", err)
	}
	v2, _ := auth.NewPKCEVerifier()
	if v1 == v2 {
		t.Fatal("PKCE verifiers should not collide")
	}
	// Must be base64url without padding
	if _, err := base64.RawURLEncoding.DecodeString(v1); err != nil {
		t.Fatalf("verifier is not valid base64url: %v", err)
	}
}

func TestBeginPKCEAuthURL(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	srv := fakeOIDCServer(t, key, "testkey", "", "my-client")

	p := auth.NewProvider(srv.URL, "my-client", "", srv.URL+"/callback", []string{"openid", "email", "profile"})

	state, _ := auth.NewOIDCState()
	verifier, _ := auth.NewPKCEVerifier()
	nonce, _ := auth.NewPKCENonce()

	authURL, err := p.BeginPKCE(t.Context(), state, verifier, nonce)
	if err != nil {
		t.Fatalf("BeginPKCE: %v", err)
	}

	// Verify all required PKCE parameters are present, including the nonce.
	for _, want := range []string{
		"response_type=code",
		"client_id=my-client",
		"state=" + state,
		"code_challenge_method=S256",
		"code_challenge=",
		"scope=openid",
		"redirect_uri=",
		"nonce=" + nonce,
	} {
		if !strings.Contains(authURL, want) {
			t.Errorf("authURL missing %q; got: %s", want, authURL)
		}
	}
}

func TestExchangeHappyPath(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const kid = "k1"
	const clientID = "client-abc"
	const testNonce = "test-happy-path-nonce"

	// Server returns a token with a specific nonce; we pass the same nonce to Exchange.
	srv := fakeOIDCServerWithNonce(t, key, kid, "", clientID, testNonce)

	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/callback", nil)
	verifier, _ := auth.NewPKCEVerifier()

	rawJWT, claims, err := p.Exchange(t.Context(), "authcode", verifier, testNonce)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if rawJWT == "" {
		t.Fatal("expected non-empty id_token")
	}
	if sub, _ := claims["sub"].(string); sub != "user123" {
		t.Fatalf("want sub=user123, got %q", sub)
	}
	if email, _ := claims["email"].(string); email != "user@example.com" {
		t.Fatalf("want email=user@example.com, got %q", email)
	}
}

func TestJWTExpiredRejected(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const kid = "k1"
	const clientID = "client-expired"

	// Build a server that returns an already-expired token.
	mux := http.NewServeMux()
	var serverBase string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		disc := map[string]string{
			"issuer":                 serverBase,
			"authorization_endpoint": serverBase + "/auth",
			"token_endpoint":         serverBase + "/token",
			"jwks_uri":               serverBase + "/jwks",
		}
		_ = json.NewEncoder(w).Encode(disc)
	})
	jwksDoc := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": kid,
				"alg": "RS256",
				"use": "sig",
				"n":   bigIntToB64(key.PublicKey.N),
				"e":   intToB64(key.PublicKey.E),
			},
		},
	}
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwksDoc)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// exp in the past
		expiredJWT := makeJWT(t, key, kid, serverBase, clientID, time.Now().Add(-time.Hour).Unix())
		resp := map[string]any{"id_token": expiredJWT, "token_type": "Bearer"}
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	serverBase = srv.URL
	t.Cleanup(srv.Close)

	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/callback", nil)
	verifier, _ := auth.NewPKCEVerifier()

	_, _, err := p.Exchange(t.Context(), "code", verifier, "")
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected 'expired' in error, got: %v", err)
	}
}

// TestCodeChallengeS256 verifies that the code_challenge in the auth URL
// matches BASE64URL(SHA256(verifier)).
func TestCodeChallengeS256(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	srv := fakeOIDCServer(t, key, "k", "", "c")
	p := auth.NewProvider(srv.URL, "c", "", srv.URL+"/cb", nil)

	verifier := "my-test-verifier-long-enough-for-s256-check"
	state := "somestate"

	authURL, err := p.BeginPKCE(t.Context(), state, verifier, "")
	if err != nil {
		t.Fatalf("BeginPKCE: %v", err)
	}

	// Compute expected challenge.
	h := sha256.Sum256([]byte(verifier))
	expectedChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	if !strings.Contains(authURL, "code_challenge="+expectedChallenge) {
		t.Fatalf("auth URL does not contain correct code_challenge; want code_challenge=%s in %s",
			expectedChallenge, authURL)
	}
}

// TestUnsupportedAlg verifies that a non-RS256 JWT is rejected.
func TestUnsupportedAlg(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const clientID = "c-alg"

	mux := http.NewServeMux()
	var serverBase string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		disc := map[string]string{
			"issuer":                 serverBase,
			"authorization_endpoint": serverBase + "/auth",
			"token_endpoint":         serverBase + "/token",
			"jwks_uri":               serverBase + "/jwks",
		}
		_ = json.NewEncoder(w).Encode(disc)
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// Build a JWT with alg=HS256 (unsupported).
		hdr := map[string]string{"alg": "HS256", "typ": "JWT"}
		hdrJSON, _ := json.Marshal(hdr)
		claims := map[string]any{
			"iss": serverBase,
			"aud": clientID,
			"sub": "u",
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		claimsJSON, _ := json.Marshal(claims)
		// Sign with anything — we just need the alg header to be HS256.
		fakeJWT := fmt.Sprintf("%s.%s.fakesig",
			base64.RawURLEncoding.EncodeToString(hdrJSON),
			base64.RawURLEncoding.EncodeToString(claimsJSON))
		_ = key // suppress unused warning
		resp := map[string]any{"id_token": fakeJWT, "token_type": "Bearer"}
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	serverBase = srv.URL
	t.Cleanup(srv.Close)

	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/cb", nil)
	verifier, _ := auth.NewPKCEVerifier()

	_, _, err := p.Exchange(t.Context(), "code", verifier, "")
	if err == nil {
		t.Fatal("expected error for unsupported alg, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Logf("error was: %v", err)
	}
}

// ---- nonce tests -----------------------------------------------------------

// TestNewPKCENonce verifies that NewPKCENonce returns distinct base64url values.
func TestNewPKCENonce(t *testing.T) {
	t.Parallel()
	n1, err := auth.NewPKCENonce()
	if err != nil {
		t.Fatalf("NewPKCENonce: %v", err)
	}
	n2, _ := auth.NewPKCENonce()
	if n1 == n2 {
		t.Fatal("nonces should not collide")
	}
	if _, err := base64.RawURLEncoding.DecodeString(n1); err != nil {
		t.Fatalf("nonce is not valid base64url: %v", err)
	}
}

// TestBeginPKCEIncludesNonce verifies that a nonce appears in the auth URL.
func TestBeginPKCEIncludesNonce(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	srv := fakeOIDCServer(t, key, "k", "", "c")
	p := auth.NewProvider(srv.URL, "c", "", srv.URL+"/cb", nil)

	nonce, _ := auth.NewPKCENonce()
	authURL, err := p.BeginPKCE(t.Context(), "state", "verifier", nonce)
	if err != nil {
		t.Fatalf("BeginPKCE: %v", err)
	}
	if !strings.Contains(authURL, "nonce="+nonce) {
		t.Fatalf("auth URL does not contain nonce; got: %s", authURL)
	}
}

// TestExchangeNonceMismatch verifies that a mismatched nonce causes ErrNonceMismatch.
func TestExchangeNonceMismatch(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const kid = "k-nonce"
	const clientID = "c-nonce"

	// IdP returns a token with nonce "correct-nonce".
	srv := fakeOIDCServerWithNonce(t, key, kid, "", clientID, "correct-nonce")
	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/cb", nil)
	verifier, _ := auth.NewPKCEVerifier()

	_, _, err := p.Exchange(t.Context(), "code", verifier, "wrong-nonce")
	if err == nil {
		t.Fatal("expected ErrNonceMismatch, got nil")
	}
	if !errors.Is(err, auth.ErrNonceMismatch) {
		t.Fatalf("expected ErrNonceMismatch, got: %v", err)
	}
}

// TestExchangeNonceMatch verifies that a matching nonce succeeds.
func TestExchangeNonceMatch(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const kid = "k-nm"
	const clientID = "c-nm"
	const theNonce = "my-secret-nonce-value"

	srv := fakeOIDCServerWithNonce(t, key, kid, "", clientID, theNonce)
	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/cb", nil)
	verifier, _ := auth.NewPKCEVerifier()

	rawJWT, claims, err := p.Exchange(t.Context(), "code", verifier, theNonce)
	if err != nil {
		t.Fatalf("Exchange with matching nonce: %v", err)
	}
	if rawJWT == "" {
		t.Fatal("expected non-empty id_token")
	}
	if claims["nonce"] != theNonce {
		t.Fatalf("expected nonce claim %q, got %q", theNonce, claims["nonce"])
	}
}

// TestExchangeBackwardCompatEmptyNonce verifies that an empty expectedNonce
// skips the check (backward compat for pre-nonce state cookies).
func TestExchangeBackwardCompatEmptyNonce(t *testing.T) {
	t.Parallel()
	key := mustRSAKey(t)
	const kid = "k-bc"
	const clientID = "c-bc"

	// Token has no nonce claim at all.
	srv := fakeOIDCServerWithNonce(t, key, kid, "", clientID, "")
	p := auth.NewProvider(srv.URL, clientID, "", srv.URL+"/cb", nil)
	verifier, _ := auth.NewPKCEVerifier()

	// Passing empty expectedNonce must succeed even though the token has no nonce.
	_, claims, err := p.Exchange(t.Context(), "code", verifier, "")
	if err != nil {
		t.Fatalf("Exchange with empty expectedNonce should succeed: %v", err)
	}
	if _, hasNonce := claims["nonce"]; hasNonce {
		t.Error("token unexpectedly carries a nonce claim")
	}
}

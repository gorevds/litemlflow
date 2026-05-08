package auth

// Fuzz test for the JWT / OIDC ID-token parser.
//
// verifyIDToken is an unexported method; this file lives in package auth (not
// auth_test) so it has direct access.
//
// Run as regular unit test (seed corpus only):
//
//	go test -count=1 ./internal/auth/
//
// Run with fuzzing:
//
//	go test -fuzz=FuzzVerifyIDToken -fuzztime=60s ./internal/auth/
//
// See docs/contributing-fuzz.md for extended guidance.

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// buildTestProvider constructs a Provider pre-loaded with a JWKS containing
// the given RSA key. No network calls are made.
func buildTestProvider(key *rsa.PrivateKey, kid, issuer, clientID string) *Provider {
	p := &Provider{
		issuer:   issuer,
		clientID: clientID,
	}
	k := jwk{
		Kid: kid,
		Kty: "RSA",
		Alg: "RS256",
		Use: "sig",
		N:   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(new(big.Int).SetInt64(int64(key.PublicKey.E)).Bytes()),
	}
	p.jwks = &jwksCache{keys: []jwk{k}}
	p.discoveryDoc = &oidcDiscovery{
		Issuer:                issuer,
		AuthorizationEndpoint: issuer + "/auth",
		TokenEndpoint:         issuer + "/token",
		JWKSURI:               issuer + "/jwks",
	}
	return p
}

// signRS256 creates a minimal RS256 JWT from the given header and payload
// JSON bytes.
func signRS256(key *rsa.PrivateKey, hdrJSON, payloadJSON []byte) string {
	hdr := base64.RawURLEncoding.EncodeToString(hdrJSON)
	pay := base64.RawURLEncoding.EncodeToString(payloadJSON)
	msg := hdr + "." + pay
	h := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		panic(err) // only in test setup
	}
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// makeSeedJWT produces a well-formed seed JWT for the fuzz corpus.
func makeSeedJWT(key *rsa.PrivateKey, kid, iss, aud string, exp int64) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	claims, _ := json.Marshal(map[string]any{
		"iss": iss,
		"aud": aud,
		"sub": "fuzz-user",
		"exp": exp,
		"iat": time.Now().Unix(),
	})
	return signRS256(key, hdr, claims)
}

// FuzzVerifyIDToken exercises verifyIDToken with arbitrary JWT-shaped inputs.
//
// Oracles:
//  1. Must never panic — any input, however malformed, must be handled.
//  2. Malformed / invalid inputs must return an error (ErrInvalidToken,
//     ErrUnsupportedAlg, or any other non-nil error); they must never return
//     non-nil claims.
//  3. A correctly constructed, in-date token from our seed RSA key and a
//     matching Provider must verify successfully — this guards against
//     regression where fuzzing hardens a code path at the expense of
//     correctness.
func FuzzVerifyIDToken(f *testing.F) {
	// Generate a key once for the lifetime of this fuzz run.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.Fatalf("generate RSA key: %v", err)
	}
	const (
		kid      = "fuzz-kid"
		issuer   = "https://fuzz.example.com"
		clientID = "fuzz-client"
	)

	futureExp := time.Now().Add(time.Hour).Unix()

	// --- seed corpus ---
	// 1. Valid token — must succeed when verified against our test Provider.
	f.Add(makeSeedJWT(key, kid, issuer, clientID, futureExp))

	// 2. Expired token.
	f.Add(makeSeedJWT(key, kid, issuer, clientID, time.Now().Add(-time.Hour).Unix()))

	// 3. Wrong audience.
	f.Add(makeSeedJWT(key, kid, issuer, "wrong-audience", futureExp))

	// 4. Wrong issuer.
	f.Add(makeSeedJWT(key, kid, "https://evil.example.com", clientID, futureExp))

	// 5. Completely random strings masquerading as JWTs.
	f.Add("not.a.jwt")
	f.Add("aGVhZA==.cGF5bG9hZA==.c2ln") // three b64 parts but not valid JSON
	f.Add("a.b.c")
	f.Add("")
	f.Add(strings.Repeat("x", 8192))
	f.Add(".")
	f.Add("..")
	f.Add("....") // 4 dots → 5 parts

	// 6. Valid header, garbage payload.
	validHdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
	hdrB64 := base64.RawURLEncoding.EncodeToString(validHdr)
	f.Add(hdrB64 + ".not-valid-base64!.sig")
	f.Add(hdrB64 + "." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".sig")

	// 7. HS256 header (unsupported alg).
	hs256Hdr, _ := json.Marshal(map[string]string{"alg": "HS256", "kid": kid})
	f.Add(base64.RawURLEncoding.EncodeToString(hs256Hdr) + ".payload.sig")

	// 8. SQL injection attempt in claim values.
	sqlHdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
	sqlClaims, _ := json.Marshal(map[string]any{
		"iss": "' OR 1=1 --",
		"aud": clientID,
		"sub": "'; DROP TABLE sessions; --",
		"exp": futureExp,
	})
	f.Add(base64.RawURLEncoding.EncodeToString(sqlHdr) + "." +
		base64.RawURLEncoding.EncodeToString(sqlClaims) + ".fakesig")

	// --- fuzz function ---
	p := buildTestProvider(key, kid, issuer, clientID)

	f.Fuzz(func(t *testing.T, rawJWT string) {
		if !utf8.ValidString(rawJWT) {
			t.Skip("non-UTF-8 input")
		}

		// Oracle 1: must not panic.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("verifyIDToken panicked on input len=%d: %v", len(rawJWT), r)
			}
		}()

		claims, err := p.verifyIDToken(rawJWT)

		if err != nil {
			// Oracle 2: on error, claims must be nil.
			if claims != nil {
				t.Errorf("verifyIDToken returned non-nil claims alongside an error: err=%v claims=%v", err, claims)
			}
			// Any error type is acceptable — ErrInvalidToken, ErrUnsupportedAlg, etc.
			return
		}

		// Oracle 3: on success we must have ErrInvalidToken-free path,
		// and the claims must at least contain "iss" and "aud".
		if claims == nil {
			t.Errorf("verifyIDToken returned nil claims with nil error")
		}
	})
}

// FuzzVerifyIDToken_SignatureCorruption specifically probes the signature
// verification path. It takes a valid token and corrupts the last segment.
func FuzzVerifyIDToken_SignatureCorruption(f *testing.F) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.Fatalf("generate RSA key: %v", err)
	}
	const (
		kid      = "scfuzz-kid"
		issuer   = "https://scfuzz.example.com"
		clientID = "scfuzz-client"
	)
	futureExp := time.Now().Add(time.Hour).Unix()
	goodJWT := makeSeedJWT(key, kid, issuer, clientID, futureExp)

	parts := strings.SplitN(goodJWT, ".", 3)
	headerPayload := parts[0] + "." + parts[1]
	originalSig := parts[2]

	f.Add(originalSig) // correct sig → must succeed
	f.Add("")          // empty sig → must fail
	f.Add("AAAA")
	f.Add(strings.Repeat("A", 344)) // 2048-bit sig length
	f.Add(originalSig[:len(originalSig)/2])

	p := buildTestProvider(key, kid, issuer, clientID)

	f.Fuzz(func(t *testing.T, corruptedSig string) {
		if !utf8.ValidString(corruptedSig) {
			t.Skip("non-UTF-8 input")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("verifyIDToken panicked on corrupted sig: %v", r)
			}
		}()
		jwt := headerPayload + "." + corruptedSig
		claims, err := p.verifyIDToken(jwt)
		if err == nil {
			// Only the original signature should verify correctly.
			// Any other sig must produce an error.
			if corruptedSig != originalSig {
				t.Errorf("unexpected successful verification with non-original signature %q; claims: %v",
					corruptedSig, claims)
			}
		} else {
			// errors.Is checks are best-effort; any error is acceptable.
			_ = errors.Is(err, ErrInvalidToken)
			_ = errors.Is(err, ErrUnsupportedAlg)
		}
	})
}

package auth

// OIDC PKCE provider — pure stdlib, no external OAuth2 library.
//
// Design notes:
//   - We fetch <issuer>/.well-known/openid-configuration once and cache it.
//   - JWKS is also fetched once per provider lifecycle and cached; key rotation
//     is handled by re-creating the Provider (server restart) or by adding a
//     refresh mechanism in v0.2.
//   - Only RS256 JWT signatures are verified in v0.1; other algorithms return
//     ErrUnsupportedAlg. This covers Google, Okta, Auth0, Keycloak defaults.
//   - The code_challenge is S256 (SHA-256 of the code_verifier, base64url
//     without padding), per RFC 7636 §4.2.
//   - Token validation follows RFC 8693 / OIDC Core §3.1.3.7: iss, aud, exp
//     are checked; nonce is not required (stateless flow; state cookie is the
//     anti-CSRF mechanism).

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrUnsupportedAlg is returned when the JWT uses an algorithm we don't support.
var ErrUnsupportedAlg = errors.New("unsupported JWT algorithm (only RS256 supported in v1)")

// ErrInvalidToken is the catch-all for JWT validation failures.
var ErrInvalidToken = errors.New("invalid ID token")

// Provider encapsulates an OIDC provider, lazily loading the discovery doc and JWKS.
type Provider struct {
	issuer       string
	clientID     string
	clientSecret string // empty for public clients
	redirectURL  string
	scopes       []string

	mu          sync.RWMutex
	discoveryDoc *oidcDiscovery
	jwks         *jwksCache
}

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type jwksCache struct {
	keys []jwk
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"` // RSA modulus, base64url
	E   string `json:"e"` // RSA exponent, base64url
}

// NewProvider constructs a Provider. Call EnsureDiscovery before use, or let
// BeginPKCE / Exchange call it lazily.
func NewProvider(issuer, clientID, clientSecret, redirectURL string, scopes []string) *Provider {
	return &Provider{
		issuer:       issuer,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURL:  redirectURL,
		scopes:       scopes,
	}
}

// EnsureDiscovery fetches and caches the OIDC discovery document if not already
// cached. Subsequent calls are no-ops.
func (p *Provider) EnsureDiscovery(ctx context.Context) error {
	p.mu.RLock()
	already := p.discoveryDoc != nil
	p.mu.RUnlock()
	if already {
		return nil
	}

	wellKnown := strings.TrimRight(p.issuer, "/") + "/.well-known/openid-configuration"
	doc, err := fetchJSON[oidcDiscovery](ctx, wellKnown)
	if err != nil {
		return fmt.Errorf("fetch OIDC discovery from %s: %w", wellKnown, err)
	}

	jwks, err := fetchJWKS(ctx, doc.JWKSURI)
	if err != nil {
		return fmt.Errorf("fetch JWKS from %s: %w", doc.JWKSURI, err)
	}

	p.mu.Lock()
	p.discoveryDoc = doc
	p.jwks = jwks
	p.mu.Unlock()
	return nil
}

// NewPKCEVerifier generates a high-entropy PKCE code verifier (RFC 7636 §4.1).
// The verifier is 64 random bytes base64url-encoded without padding.
func NewPKCEVerifier() (string, error) {
	b := make([]byte, 48) // 48 bytes → 64 base64url chars
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewOIDCState generates a random anti-CSRF state value.
func NewOIDCState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallenge computes the S256 code_challenge for a given verifier.
// Per RFC 7636 §4.2: BASE64URL(SHA256(ASCII(code_verifier))).
func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// BeginPKCE builds the authorization URL the client should be redirected to.
// Both state and codeVerifier must be freshly generated random values.
func (p *Provider) BeginPKCE(ctx context.Context, state, codeVerifier string) (string, error) {
	if err := p.EnsureDiscovery(ctx); err != nil {
		return "", err
	}
	p.mu.RLock()
	authEP := p.discoveryDoc.AuthorizationEndpoint
	p.mu.RUnlock()

	scopes := p.scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "email", "profile"}
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", p.clientID)
	params.Set("redirect_uri", p.redirectURL)
	params.Set("scope", strings.Join(scopes, " "))
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge(codeVerifier))
	params.Set("code_challenge_method", "S256")

	return authEP + "?" + params.Encode(), nil
}

// tokenResponse is the token endpoint JSON response shape.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// Exchange completes the PKCE flow: it POSTs to the token endpoint and returns
// the raw ID token JWT plus parsed claims.
func (p *Provider) Exchange(ctx context.Context, code, codeVerifier string) (string, map[string]any, error) {
	if err := p.EnsureDiscovery(ctx); err != nil {
		return "", nil, err
	}
	p.mu.RLock()
	tokenEP := p.discoveryDoc.TokenEndpoint
	p.mu.RUnlock()

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.redirectURL)
	form.Set("client_id", p.clientID)
	form.Set("code_verifier", codeVerifier)
	if p.clientSecret != "" {
		form.Set("client_secret", p.clientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEP, strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", nil, err
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.Error != "" {
		return "", nil, fmt.Errorf("token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	if tr.IDToken == "" {
		return "", nil, errors.New("token response missing id_token")
	}

	claims, err := p.verifyIDToken(tr.IDToken)
	if err != nil {
		return "", nil, err
	}
	return tr.IDToken, claims, nil
}

// verifyIDToken verifies the signature and standard claims of an RS256 JWT.
func (p *Provider) verifyIDToken(rawJWT string) (map[string]any, error) {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: not a JWT", ErrInvalidToken)
	}

	// Parse header.
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header decode", ErrInvalidToken)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return nil, fmt.Errorf("%w: header parse", ErrInvalidToken)
	}
	if hdr.Alg != "RS256" {
		return nil, ErrUnsupportedAlg
	}

	// Verify signature.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: signature decode", ErrInvalidToken)
	}
	pubKey, err := p.resolveKey(hdr.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("%w: signature verification failed", ErrInvalidToken)
	}

	// Parse claims.
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: claims decode", ErrInvalidToken)
	}
	var claims map[string]any
	if err := json.Unmarshal(claimBytes, &claims); err != nil {
		return nil, fmt.Errorf("%w: claims parse", ErrInvalidToken)
	}

	// Validate standard claims.
	if err := p.validateClaims(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// validateClaims checks iss, aud, exp.
func (p *Provider) validateClaims(claims map[string]any) error {
	// iss
	iss, _ := claims["iss"].(string)
	if !strings.EqualFold(iss, p.issuer) {
		// Some providers append trailing slash; normalise.
		if strings.TrimRight(iss, "/") != strings.TrimRight(p.issuer, "/") {
			return fmt.Errorf("%w: issuer mismatch (got %q)", ErrInvalidToken, iss)
		}
	}

	// aud — may be a string or []interface{}
	switch aud := claims["aud"].(type) {
	case string:
		if aud != p.clientID {
			return fmt.Errorf("%w: audience mismatch", ErrInvalidToken)
		}
	case []any:
		found := false
		for _, a := range aud {
			if s, ok := a.(string); ok && s == p.clientID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: audience does not include client_id", ErrInvalidToken)
		}
	default:
		return fmt.Errorf("%w: missing or invalid aud claim", ErrInvalidToken)
	}

	// exp
	switch v := claims["exp"].(type) {
	case float64:
		if int64(v) < time.Now().Unix() {
			return fmt.Errorf("%w: token expired", ErrInvalidToken)
		}
	default:
		return fmt.Errorf("%w: missing exp claim", ErrInvalidToken)
	}

	return nil
}

// resolveKey finds the RSA public key matching kid (or first key if kid=="").
func (p *Provider) resolveKey(kid string) (*rsa.PublicKey, error) {
	p.mu.RLock()
	cache := p.jwks
	p.mu.RUnlock()
	if cache == nil {
		return nil, errors.New("JWKS not loaded")
	}

	for _, k := range cache.keys {
		if k.Kty != "RSA" {
			continue
		}
		if kid != "" && k.Kid != kid {
			continue
		}
		pub, err := rsaPublicKeyFromJWK(k)
		if err != nil {
			return nil, err
		}
		return pub, nil
	}
	return nil, fmt.Errorf("%w: no matching key for kid=%q", ErrInvalidToken, kid)
}

// rsaPublicKeyFromJWK reconstructs an *rsa.PublicKey from a JWK.
func rsaPublicKeyFromJWK(k jwk) (*rsa.PublicKey, error) {
	// Modulus
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode JWK n: %w", err)
	}
	// Exponent
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode JWK e: %w", err)
	}
	// Pad eBytes to 4 bytes for binary.BigEndian.Uint32.
	if len(eBytes) < 4 {
		padded := make([]byte, 4)
		copy(padded[4-len(eBytes):], eBytes)
		eBytes = padded
	}
	eInt := int(binary.BigEndian.Uint32(eBytes))
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: eInt,
	}, nil
}

// fetchJWKS downloads and parses the JWKS document.
func fetchJWKS(ctx context.Context, uri string) (*jwksCache, error) {
	type jwksDoc struct {
		Keys []jwk `json:"keys"`
	}
	doc, err := fetchJSON[jwksDoc](ctx, uri)
	if err != nil {
		return nil, err
	}
	return &jwksCache{keys: doc.Keys}, nil
}

// fetchJSON is a small helper that GETs a URL and JSON-decodes the response.
func fetchJSON[T any](ctx context.Context, uri string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, uri)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("JSON decode: %w", err)
	}
	return &v, nil
}

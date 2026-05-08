// Package auth contains OIDC PKCE helpers and session-cookie utilities.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const (
	// SessionCookieName is the name of the session cookie.
	SessionCookieName = "lmf_session"

	// OIDCStateCookieName is the short-lived cookie that carries PKCE state
	// across the OIDC redirect round-trip. It is cleared once the callback
	// succeeds or explicitly fails.
	OIDCStateCookieName = "lmf_oidc_state"

	// oidcStateTTL is how long the PKCE state cookie is valid. The user must
	// complete the IdP flow within this window.
	oidcStateTTL = 10 * time.Minute
)

// ErrNoCookie is returned when no session cookie is present.
var ErrNoCookie = errors.New("no session cookie")

// NewSessionID generates a cryptographically random 32-byte session ID,
// returned as a 64-character lowercase hex string.
func NewSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetSessionCookie writes the session cookie to the response. The cookie is
// HttpOnly, SameSite=Lax, and Secure. It expires at the same time as the
// server-side session row.
//
// In local dev without TLS the browser ignores the Secure attribute; use
// SetSessionCookieInsecure for plain-HTTP test environments.
func SetSessionCookie(w http.ResponseWriter, sessionID string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// IsRequestSecure returns true if the inbound request was carried over TLS
// (either directly or via a trusted reverse proxy that set X-Forwarded-Proto).
// Handlers use this to decide whether to set the Secure flag on outbound
// cookies — never assume; always check, so we don't accidentally send
// Secure cookies that browsers silently drop, and don't send insecure
// cookies when TLS is actually available.
func IsRequestSecure(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
		return true
	}
	return false
}

// SetSessionCookieAuto chooses the Secure flag based on request transport.
// Prefer this over SetSessionCookie / SetSessionCookieInsecure in handlers.
func SetSessionCookieAuto(w http.ResponseWriter, r *http.Request, sessionID string, expiresAt time.Time) {
	if IsRequestSecure(r) {
		SetSessionCookie(w, sessionID, expiresAt)
	} else {
		SetSessionCookieInsecure(w, sessionID, expiresAt)
	}
}

// SetOIDCStateCookieAuto picks Secure based on transport.
func SetOIDCStateCookieAuto(w http.ResponseWriter, r *http.Request, state PKCEState) error {
	if IsRequestSecure(r) {
		return SetOIDCStateCookie(w, state)
	}
	return SetOIDCStateCookieInsecure(w, state)
}

// SetSessionCookieInsecure is like SetSessionCookie but omits the Secure flag.
// Use in tests and plain-HTTP dev environments only.
func SetSessionCookieInsecure(w http.ResponseWriter, sessionID string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie instructs the browser to delete the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// GetSessionID extracts the session ID from the request cookie.
// Returns ("", ErrNoCookie) if no cookie is present.
func GetSessionID(r *http.Request) (string, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return "", ErrNoCookie
		}
		return "", err
	}
	if c.Value == "" {
		return "", ErrNoCookie
	}
	return c.Value, nil
}

// --- OIDC state cookie -------------------------------------------------------

// PKCEState is the round-trip payload stored in the OIDC state cookie.
// It is JSON-encoded then hex-encoded for safe transport in a cookie value.
type PKCEState struct {
	// State is the opaque anti-CSRF value sent to the IdP and echoed back.
	State string `json:"state"`
	// CodeVerifier is the PKCE plain-text verifier; only the SHA-256 hash
	// (code_challenge) is sent to the IdP.
	CodeVerifier string `json:"cv"`
	// Nonce is the OIDC nonce sent to the IdP and expected back in the
	// ID token "nonce" claim. Empty for cookies set before nonce support was
	// added (v0.2→v0.3 in-flight upgrade); Exchange skips the check when empty.
	Nonce string `json:"nonce,omitempty"`
	// ReturnTo is the original URL the user wanted to reach (optional).
	ReturnTo string `json:"r,omitempty"`
}

// SetOIDCStateCookie writes the PKCE state payload to a short-lived cookie.
// The cookie path is "/" so the callback handler can read it regardless of
// how the IdP returns the user.
func SetOIDCStateCookie(w http.ResponseWriter, state PKCEState) error {
	encoded, err := marshalState(state)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     OIDCStateCookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   int(oidcStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// SetOIDCStateCookieInsecure is like SetOIDCStateCookie without Secure for tests.
func SetOIDCStateCookieInsecure(w http.ResponseWriter, state PKCEState) error {
	encoded, err := marshalState(state)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     OIDCStateCookieName,
		Value:    encoded,
		Path:     "/",
		MaxAge:   int(oidcStateTTL.Seconds()),
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// GetOIDCState reads and decodes the PKCE state cookie. Returns ErrNoCookie if absent.
func GetOIDCState(r *http.Request) (PKCEState, error) {
	c, err := r.Cookie(OIDCStateCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return PKCEState{}, ErrNoCookie
		}
		return PKCEState{}, err
	}
	return unmarshalState(c.Value)
}

// ClearOIDCStateCookie deletes the PKCE state cookie.
func ClearOIDCStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     OIDCStateCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// marshalState JSON-encodes then hex-encodes a PKCEState.
func marshalState(s PKCEState) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// unmarshalState is the inverse of marshalState.
func unmarshalState(encoded string) (PKCEState, error) {
	b, err := hex.DecodeString(encoded)
	if err != nil {
		return PKCEState{}, errors.New("invalid state cookie encoding")
	}
	var s PKCEState
	if err := json.Unmarshal(b, &s); err != nil {
		return PKCEState{}, errors.New("invalid state cookie payload")
	}
	return s, nil
}

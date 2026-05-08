package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/litemlflow/litemlflow/internal/auth"
)

// TestNewSessionID checks length and uniqueness.
func TestNewSessionID(t *testing.T) {
	t.Parallel()
	id1, err := auth.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	id2, err := auth.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	// 32 bytes hex → 64 chars
	if len(id1) != 64 {
		t.Fatalf("want 64-char id, got %d", len(id1))
	}
	if id1 == id2 {
		t.Fatal("two NewSessionID calls returned the same value (collision?)")
	}
}

// TestSessionCookieRoundtrip writes and reads a session cookie.
func TestSessionCookieRoundtrip(t *testing.T) {
	t.Parallel()
	sessID := "abc123"
	exp := time.Now().Add(time.Hour)

	// Write
	rec := httptest.NewRecorder()
	auth.SetSessionCookieInsecure(rec, sessID, exp)

	// Build a request with the cookie from the response.
	req := &http.Request{Header: http.Header{}}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	// Read
	got, err := auth.GetSessionID(req)
	if err != nil {
		t.Fatalf("GetSessionID: %v", err)
	}
	if got != sessID {
		t.Fatalf("want %q, got %q", sessID, got)
	}
}

// TestNoCookie verifies ErrNoCookie is returned when the cookie is absent.
func TestNoCookie(t *testing.T) {
	t.Parallel()
	req := &http.Request{Header: http.Header{}}
	_, err := auth.GetSessionID(req)
	if err != auth.ErrNoCookie {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
}

// TestClearSessionCookie checks the Max-Age=-1 semantics.
func TestClearSessionCookie(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	auth.ClearSessionCookie(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.SessionCookieName {
		t.Fatalf("wrong cookie name: %q", c.Name)
	}
	if c.MaxAge != -1 {
		t.Fatalf("expected MaxAge=-1, got %d", c.MaxAge)
	}
}

// TestPKCEStateRoundtrip encodes and decodes a PKCEState.
func TestPKCEStateRoundtrip(t *testing.T) {
	t.Parallel()
	original := auth.PKCEState{
		State:        "randomstate123",
		CodeVerifier: "verifier_value_abc",
		ReturnTo:     "/ui/experiments",
	}

	rec := httptest.NewRecorder()
	if err := auth.SetOIDCStateCookieInsecure(rec, original); err != nil {
		t.Fatalf("SetOIDCStateCookieInsecure: %v", err)
	}

	req := &http.Request{Header: http.Header{}}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}

	got, err := auth.GetOIDCState(req)
	if err != nil {
		t.Fatalf("GetOIDCState: %v", err)
	}
	if got.State != original.State {
		t.Fatalf("State: want %q, got %q", original.State, got.State)
	}
	if got.CodeVerifier != original.CodeVerifier {
		t.Fatalf("CodeVerifier: want %q, got %q", original.CodeVerifier, got.CodeVerifier)
	}
	if got.ReturnTo != original.ReturnTo {
		t.Fatalf("ReturnTo: want %q, got %q", original.ReturnTo, got.ReturnTo)
	}
}

// TestOIDCStateMissingCookie verifies error when state cookie is absent.
func TestOIDCStateMissingCookie(t *testing.T) {
	t.Parallel()
	req := &http.Request{Header: http.Header{}}
	_, err := auth.GetOIDCState(req)
	if err != auth.ErrNoCookie {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
}

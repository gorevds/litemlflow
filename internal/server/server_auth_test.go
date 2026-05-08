package server_test

// AUTH-OIDC: end-to-end auth tests for the session-based login flow.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/litemlflow/litemlflow/internal/auth"
	"github.com/litemlflow/litemlflow/internal/config"
)

// hashPass returns the hex SHA-256 of a password (matches verifyBasic in middleware).
func hashPass(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// doJSONAuth sends a request with an optional JSON body and optional cookies.
func doJSONAuth(t *testing.T, client *http.Client, method, url string, body any, cookies []*http.Cookie) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// drainBody reads and closes the response body, returning the bytes.
func drainBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

// TestBasicLoginCookieLogout tests the full basic-auth session flow:
//  1. POST /api/v1/auth/login → 200 + session cookie
//  2. GET /api/v1/auth/whoami with the cookie → logged-in user
//  3. POST /api/v1/auth/logout → 200, session invalidated
//  4. GET /api/v1/auth/whoami with the old cookie → 401 (session gone)
func TestBasicLoginCookieLogout(t *testing.T) {
	t.Parallel()
	const user = "authuser"
	const pass = "authpass"

	ts, _ := newTestServer(t, config.Config{
		Auth:          "basic",
		BasicUser:     user,
		BasicPassHash: hashPass(pass),
	})
	client := ts.Client()

	// 1. Login.
	loginResp := doJSONAuth(t, client, http.MethodPost, ts.URL+"/api/v1/auth/login",
		map[string]string{"user": user, "pass": pass}, nil)
	body := drainBody(t, loginResp)
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login: want 200, got %d; body: %s", loginResp.StatusCode, body)
	}

	// Verify the response body contains session_expires_at.
	var loginResult map[string]any
	if err := json.Unmarshal(body, &loginResult); err != nil {
		t.Fatalf("parse login body: %v; body: %s", err, body)
	}
	if _, ok := loginResult["session_expires_at"]; !ok {
		t.Fatalf("login response missing session_expires_at; body: %s", body)
	}

	// Extract the session cookie.
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("no session cookie in login response; cookies: %v", loginResp.Cookies())
	}

	// 2. Whoami with the session cookie should return the authenticated user.
	whoamiResp := doJSONAuth(t, client, http.MethodGet, ts.URL+"/api/v1/auth/whoami",
		nil, []*http.Cookie{sessionCookie})
	whoamiBody := drainBody(t, whoamiResp)
	if whoamiResp.StatusCode != http.StatusOK {
		t.Fatalf("whoami: want 200, got %d; body: %s", whoamiResp.StatusCode, whoamiBody)
	}
	var whoami map[string]string
	if err := json.Unmarshal(whoamiBody, &whoami); err != nil {
		t.Fatalf("parse whoami body: %v; body: %s", err, whoamiBody)
	}
	if whoami["user"] != user {
		t.Fatalf("whoami user: want %q, got %q", user, whoami["user"])
	}
	if whoami["auth_method"] != "basic" {
		t.Fatalf("whoami auth_method: want %q, got %q", "basic", whoami["auth_method"])
	}

	// 3. Logout with the session cookie.
	logoutResp := doJSONAuth(t, client, http.MethodPost, ts.URL+"/api/v1/auth/logout",
		nil, []*http.Cookie{sessionCookie})
	drainBody(t, logoutResp)
	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout: want 200, got %d", logoutResp.StatusCode)
	}

	// 4. Using the invalidated session cookie should fail. The middleware will
	// try the session, find it deleted, and fall back to basic-auth check —
	// which also fails (no Basic header) → 401.
	postLogoutResp := doJSONAuth(t, client, http.MethodGet, ts.URL+"/api/v1/auth/whoami",
		nil, []*http.Cookie{sessionCookie})
	drainBody(t, postLogoutResp)
	if postLogoutResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout whoami: want 401, got %d", postLogoutResp.StatusCode)
	}
}

// TestWhoamiReturnsAnonymousWithNoAuth verifies auth=none returns "anonymous".
func TestWhoamiReturnsAnonymousWithNoAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{Auth: "none"})
	client := ts.Client()

	resp := doJSONAuth(t, client, http.MethodGet, ts.URL+"/api/v1/auth/whoami", nil, nil)
	body := drainBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", resp.StatusCode, body)
	}
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if result["user"] != "anonymous" {
		t.Fatalf("want user=anonymous, got %q", result["user"])
	}
}

// TestLoginWrongPasswordReturns401 verifies incorrect credentials are rejected.
func TestLoginWrongPasswordReturns401(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{
		Auth:          "basic",
		BasicUser:     "admin",
		BasicPassHash: hashPass("correct-password"),
	})
	client := ts.Client()

	resp := doJSONAuth(t, client, http.MethodPost, ts.URL+"/api/v1/auth/login",
		map[string]string{"user": "admin", "pass": "wrong-password"}, nil)
	drainBody(t, resp)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

// TestLoginWithOIDCModeReturns400 verifies that direct credential login is
// rejected when auth=oidc (user must go through /oidc/start).
func TestLoginWithOIDCModeReturns400(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{
		Auth:            "oidc",
		OIDCIssuer:      "https://fake.example.com",
		OIDCClientID:    "fake-client",
		OIDCRedirectURL: "http://localhost/cb",
	})
	client := ts.Client()

	resp := doJSONAuth(t, client, http.MethodPost, ts.URL+"/api/v1/auth/login",
		map[string]string{"user": "x", "pass": "y"}, nil)
	drainBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestMLflowCompatNotBroken verifies auth=none does not break MLflow compat.
// This is the critical regression check required by the acceptance criteria.
func TestMLflowCompatNotBroken(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{Auth: "none"})
	client := ts.Client()

	resp := doJSONAuth(t, client, http.MethodPost,
		ts.URL+"/api/2.0/mlflow/experiments/create",
		map[string]string{"name": "compat-test-experiment"}, nil)
	body := drainBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("MLflow experiments/create: want 200, got %d; body: %s", resp.StatusCode, body)
	}
}

// TestLogoutIsAlwaysOK verifies that POST /logout is a no-op (200) even
// when no session cookie is present.
func TestLogoutIsAlwaysOK(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{Auth: "none"})
	client := ts.Client()

	resp := doJSONAuth(t, client, http.MethodPost, ts.URL+"/api/v1/auth/logout", nil, nil)
	drainBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

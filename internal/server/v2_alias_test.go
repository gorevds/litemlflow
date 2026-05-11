// Tests for the v2.0 LTS surface: /api/v2/... must alias to /api/v1/...
// and routes wrapped by the mlflow `deprecated()` helper must emit the
// RFC 7231 Sunset date set in ADR 0003.
package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// TestV2AliasResolvesToV1 verifies that the /api/v2/... namespace
// returns the same response as /api/v1/... for a basic GET, and that
// the X-API-Version response header is set to "2" only when v2 was used.
func TestV2AliasResolvesToV1(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	for _, path := range []string{"/api/v1/federate/peers", "/api/v2/federate/peers"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d body=%s", path, resp.StatusCode, raw)
		}
		v := resp.Header.Get("X-API-Version")
		if path == "/api/v2/federate/peers" && v != "2" {
			t.Errorf("v2 request should have X-API-Version: 2, got %q", v)
		}
		if path == "/api/v1/federate/peers" && v != "" {
			t.Errorf("v1 request should NOT have X-API-Version, got %q", v)
		}
	}
}

// TestMlflowDeprecatedRoutesEmitSunset verifies that every route flagged
// in ADR 0003 as deprecated-at-v2.0 carries the RFC 8594 deprecation
// triad including the concrete Sunset date (2027-05-11, a Tuesday).
func TestMlflowDeprecatedRoutesEmitSunset(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	probe := func(method, path string) *http.Response {
		req, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	cases := []struct{ method, path string }{
		{"GET", "/api/2.0/mlflow/experiments/list"},
		{"POST", "/api/2.0/mlflow/experiments/list"},
		{"DELETE", "/api/2.0/mlflow/registered-models/delete"},
		{"DELETE", "/api/2.0/mlflow/model-versions/delete"},
	}
	for _, c := range cases {
		resp := probe(c.method, c.path)
		if got := resp.Header.Get("Deprecation"); got != "true" {
			t.Errorf("%s %s Deprecation: got %q want true", c.method, c.path, got)
		}
		if got := resp.Header.Get("Sunset"); got != "Tue, 11 May 2027 00:00:00 GMT" {
			t.Errorf("%s %s Sunset: got %q want 'Tue, 11 May 2027 00:00:00 GMT' (RFC 7231 IMF-fixdate)",
				c.method, c.path, got)
		}
		if got := resp.Header.Get("Link"); got == "" {
			t.Errorf("%s %s Link header missing", c.method, c.path)
		}
		_ = resp.Body.Close()
	}
}

// TestV2AliasPreservesRouteVariables verifies that chi route patterns with
// {var} segments work the same through /api/v2/ as /api/v1/. Independent
// review item L15.
func TestV2AliasPreservesRouteVariables(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	const fakeID = "abcdef0123456789abcdef0123456789"
	for _, base := range []string{"/api/v1", "/api/v2"} {
		resp, err := http.Get(ts.URL + base + "/runs/" + fakeID + "/data")
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d body=%s", base, resp.StatusCode, raw)
		}
		if !strings.Contains(string(raw), "RESOURCE_DOES_NOT_EXIST") {
			t.Errorf("%s: expected RESOURCE_DOES_NOT_EXIST, got %s", base, raw)
		}
	}
}

// TestV2AliasPreservesPercentEncoding verifies the H6 fix: a
// percent-encoded segment in /api/v2/... must not be re-decoded into a
// different path. The bug: a slice on r.URL.Path (already decoded)
// would split `/api/v2/prompts/my%2Fname` into two path segments
// (`prompts/my/name`) — wrong prompt name.
func TestV2AliasPreservesPercentEncoding(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// "my%2Fname" decodes to "my/name" if mis-handled; the fixed
	// middleware should treat it as a single-segment prompt name.
	// We can't easily verify the exact downstream behavior without a
	// matching prompt, but we can verify the v2 path produces the same
	// outcome as the equivalent v1 path (both 404 with the same error
	// shape on a non-existent prompt).
	encoded := "/prompts/my%2Fname/versions/1"
	v1, err := http.Get(ts.URL + "/api/v1" + encoded)
	if err != nil {
		t.Fatal(err)
	}
	v1Body, _ := io.ReadAll(v1.Body)
	_ = v1.Body.Close()

	v2, err := http.Get(ts.URL + "/api/v2" + encoded)
	if err != nil {
		t.Fatal(err)
	}
	v2Body, _ := io.ReadAll(v2.Body)
	_ = v2.Body.Close()

	if v1.StatusCode != v2.StatusCode {
		t.Errorf("status mismatch: v1=%d v2=%d", v1.StatusCode, v2.StatusCode)
	}
	if string(v1Body) != string(v2Body) {
		t.Errorf("body mismatch:\n  v1=%s\n  v2=%s", v1Body, v2Body)
	}
}

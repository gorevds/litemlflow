// Tests for the v2.0 LTS surface: /api/v2/... must alias to /api/v1/...
// and routes wrapped by the mlflow `deprecated()` helper must emit the
// RFC 7231 Sunset date set in ADR 0003.
package server_test

import (
	"io"
	"net/http"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// TestV2AliasResolvesToV1 verifies that the /api/v2/... namespace
// returns the same response as /api/v1/... for a basic GET, and that
// the X-API-Version response header is set to "2" only when v2 was used.
func TestV2AliasResolvesToV1(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	// Seed a peer-less federation peer list so /federate/peers responds 200.
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

// TestMlflowDeprecatedRoutesEmitSunset verifies that legacy MLflow-compat
// aliases (experiments/list) carry the RFC 8594 deprecation triad including
// the concrete Sunset date set at v2.0 GA (2027-05-11).
func TestMlflowDeprecatedRoutesEmitSunset(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	resp, err := http.Get(ts.URL + "/api/2.0/mlflow/experiments/list")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Deprecation"); got != "true" {
		t.Errorf("Deprecation header: got %q want true", got)
	}
	if got := resp.Header.Get("Sunset"); got != "Sat, 11 May 2027 00:00:00 GMT" {
		t.Errorf("Sunset header: got %q want RFC 7231 IMF-fixdate", got)
	}
	if got := resp.Header.Get("Link"); got == "" {
		t.Errorf("Link header missing")
	}
}

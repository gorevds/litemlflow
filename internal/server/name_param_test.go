package server_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// Prompt names containing reserved characters ("/", "%") must round-trip:
// the UI and SDK percent-encode them in the path, and the handler has to
// decode the chi route param before looking the name up.
func TestPromptNameWithReservedCharsRoundTrips(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{})

	for _, name := range []string{"team/summarizer", "100%-recall", "plain"} {
		resp, err := http.Post(ts.URL+"/api/v1/prompts", "application/json",
			strings.NewReader(`{"name":"`+name+`","content":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create %q: %d", name, resp.StatusCode)
		}
		escaped := strings.NewReplacer("/", "%2F", "%", "%25").Replace(name)
		resp, err = http.Get(ts.URL + "/api/v1/prompts/" + escaped)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hi") {
			t.Errorf("get %q: %d %s", name, resp.StatusCode, body)
		}
	}
}

package server_test

// Guards independent-review finding 2.1: the Model Registry and Prompts were
// global (no workspace_id), so tenant A's "fraud-v1" collided with / was
// readable by tenant B. After migration 015 + workspace-scoped store methods,
// registry entries and prompts must be isolated per workspace, and the same
// name must be usable independently in different workspaces.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

func TestRegistryAndPromptsAreWorkspaceScoped(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{}) // auth=none

	do := func(method, path, ws, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if ws != "" {
			req.Header.Set("X-Workspace", ws)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw := new(strings.Builder)
		buf := make([]byte, 2048)
		for {
			n, e := resp.Body.Read(buf)
			raw.Write(buf[:n])
			if e != nil {
				break
			}
		}
		return resp.StatusCode, raw.String()
	}

	for _, ws := range []string{"ws-a", "ws-b"} {
		if st, body := do(http.MethodPost, "/api/v1/workspaces", "", `{"id":"`+ws+`","name":"`+ws+`"}`); st != http.StatusCreated {
			t.Fatalf("create workspace %s: %d %s", ws, st, body)
		}
	}

	// --- Registered model "shared" created in ws-a ---
	if st, body := do(http.MethodPost, "/api/2.0/mlflow/registered-models/create", "ws-a", `{"name":"shared"}`); st != http.StatusOK {
		t.Fatalf("create model in ws-a: %d %s", st, body)
	}
	// ws-a can read it.
	if st, _ := do(http.MethodGet, "/api/2.0/mlflow/registered-models/get?name=shared", "ws-a", ""); st != http.StatusOK {
		t.Errorf("get model in owning ws-a: want 200, got %d", st)
	}
	// ws-b must NOT see it (cross-tenant isolation).
	if st, _ := do(http.MethodGet, "/api/2.0/mlflow/registered-models/get?name=shared", "ws-b", ""); st != http.StatusNotFound {
		t.Errorf("get model from foreign ws-b: want 404, got %d", st)
	}
	// ws-b can create the SAME name independently (no global namespace collision).
	if st, body := do(http.MethodPost, "/api/2.0/mlflow/registered-models/create", "ws-b", `{"name":"shared"}`); st != http.StatusOK {
		t.Errorf("create same-name model in ws-b: want 200 (independent namespace), got %d %s", st, body)
	}

	// --- Prompt "p1" created in ws-a ---
	if st, body := do(http.MethodPost, "/api/v1/prompts", "ws-a", `{"name":"p1","content":"hello"}`); st != http.StatusOK {
		t.Fatalf("create prompt in ws-a: %d %s", st, body)
	}
	if st, _ := do(http.MethodGet, "/api/v1/prompts/p1", "ws-a", ""); st != http.StatusOK {
		t.Errorf("get prompt in owning ws-a: want 200, got %d", st)
	}
	if st, _ := do(http.MethodGet, "/api/v1/prompts/p1", "ws-b", ""); st != http.StatusNotFound {
		t.Errorf("get prompt from foreign ws-b: want 404, got %d", st)
	}
	// ws-b can create the same prompt name independently.
	if st, body := do(http.MethodPost, "/api/v1/prompts", "ws-b", `{"name":"p1","content":"different"}`); st != http.StatusOK {
		t.Errorf("create same-name prompt in ws-b: want 200, got %d %s", st, body)
	}
	// And ws-b's prompt content is its own, not ws-a's.
	if st, body := do(http.MethodGet, "/api/v1/prompts/p1", "ws-b", ""); st != http.StatusOK || !strings.Contains(body, "different") {
		t.Errorf("ws-b prompt should have its own content; got %d %s", st, body)
	}
}

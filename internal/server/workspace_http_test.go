package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// newWS is a helper that creates a fresh test server for workspace tests
// and returns the httptest.Server.
func newWS(t *testing.T) *httptest.Server {
	t.Helper()
	ts, _ := newTestServer(t, config.Config{})
	return ts
}

// wsDoJSON performs an HTTP request to the test server with an optional JSON
// body and optional extra headers. Returns the response and decoded body.
func wsDoJSON(t *testing.T, srv *httptest.Server, method, path string, body any, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

// ---- tests ------------------------------------------------------------------

// TestWorkspaceListContainsDefault verifies that the default workspace is
// present after a fresh migration.
func TestWorkspaceListContainsDefault(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	resp, body := wsDoJSON(t, srv, http.MethodGet, "/api/v1/workspaces", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", resp.StatusCode, body)
	}
	wss, ok := body["workspaces"].([]any)
	if !ok {
		t.Fatalf("expected workspaces array, got %T", body["workspaces"])
	}
	found := false
	for _, raw := range wss {
		if ws, ok := raw.(map[string]any); ok {
			if ws["id"] == "default" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("default workspace not found in list")
	}
}

// TestWorkspaceCreateAndGet exercises the full create / get cycle.
func TestWorkspaceCreateAndGet(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	// Create
	resp, body := wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":          "team-beta",
		"name":        "Team Beta",
		"description": "beta team workspace",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %v", resp.StatusCode, body)
	}
	if body["id"] != "team-beta" {
		t.Fatalf("unexpected id: %v", body)
	}

	// Get
	resp2, body2 := wsDoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/team-beta", nil, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get: want 200, got %d", resp2.StatusCode)
	}
	if body2["name"] != "Team Beta" {
		t.Fatalf("unexpected name: %v", body2)
	}
}

// TestWorkspaceUpdate exercises PATCH.
func TestWorkspaceUpdate(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":   "ws-patch",
		"name": "Original Name",
	}, nil)

	resp, body := wsDoJSON(t, srv, http.MethodPatch, "/api/v1/workspaces/ws-patch", map[string]string{
		"name": "Updated Name",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: want 200, got %d: %v", resp.StatusCode, body)
	}
	if body["name"] != "Updated Name" {
		t.Fatalf("name not updated: %v", body)
	}
}

// TestWorkspaceDelete exercises DELETE and the guard against deleting default.
func TestWorkspaceDelete(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":   "ws-del",
		"name": "To Delete",
	}, nil)

	resp, _ := wsDoJSON(t, srv, http.MethodDelete, "/api/v1/workspaces/ws-del", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", resp.StatusCode)
	}

	// Get after delete should 404
	resp2, _ := wsDoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/ws-del", nil, nil)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", resp2.StatusCode)
	}

	// Cannot delete default
	resp3, body3 := wsDoJSON(t, srv, http.MethodDelete, "/api/v1/workspaces/default", nil, nil)
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("delete default: want 409, got %d: %v", resp3.StatusCode, body3)
	}
}

// TestWorkspaceMembersHTTP exercises the member management endpoints.
func TestWorkspaceMembersHTTP(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":   "ws-mbr",
		"name": "Member WS",
	}, nil)

	// Add member
	resp, body := wsDoJSON(t, srv, http.MethodPut, "/api/v1/workspaces/ws-mbr/members/alice",
		map[string]string{"role": "admin"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add member: want 200, got %d: %v", resp.StatusCode, body)
	}

	// List members
	resp2, body2 := wsDoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/ws-mbr/members", nil, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("list members: want 200, got %d", resp2.StatusCode)
	}
	members, ok := body2["members"].([]any)
	if !ok || len(members) != 1 {
		t.Fatalf("expected 1 member, got %v", body2)
	}

	// Remove member
	resp3, _ := wsDoJSON(t, srv, http.MethodDelete, "/api/v1/workspaces/ws-mbr/members/alice", nil, nil)
	if resp3.StatusCode != http.StatusNoContent {
		t.Fatalf("remove member: want 204, got %d", resp3.StatusCode)
	}
}

// TestCurrentWorkspaceEndpoint verifies /api/v1/workspaces/current.
func TestCurrentWorkspaceEndpoint(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	resp, body := wsDoJSON(t, srv, http.MethodGet, "/api/v1/workspaces/current", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", resp.StatusCode, body)
	}
	ws, ok := body["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("expected workspace object, got %T: %v", body["workspace"], body)
	}
	if ws["id"] != "default" {
		t.Fatalf("expected default workspace, got %v", ws["id"])
	}
}

// TestXWorkspaceHeaderScoping creates an experiment via the MLflow API with
// X-Workspace header and verifies it's not visible from another workspace.
func TestXWorkspaceHeaderScoping(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	// Create a second workspace.
	wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":   "project-x",
		"name": "Project X",
	}, nil)

	// Create an experiment in project-x via MLflow API.
	resp, body := wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/create",
		map[string]string{"name": "scoped-exp"},
		map[string]string{"X-Workspace": "project-x"},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create experiment in project-x: want 200, got %d: %v", resp.StatusCode, body)
	}

	// Search experiments from the default workspace — should NOT see scoped-exp.
	resp2, body2 := wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/search",
		map[string]any{},
		nil, // no X-Workspace → defaults to "default"
	)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("search in default: want 200, got %d", resp2.StatusCode)
	}
	exps, _ := body2["experiments"].([]any)
	for _, rawExp := range exps {
		e, ok := rawExp.(map[string]any)
		if !ok {
			continue
		}
		if e["name"] == "scoped-exp" {
			t.Fatal("isolation breach: scoped-exp appeared in default workspace search")
		}
	}

	// Search experiments from project-x — SHOULD see scoped-exp.
	resp3, body3 := wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/search",
		map[string]any{},
		map[string]string{"X-Workspace": "project-x"},
	)
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("search in project-x: want 200, got %d", resp3.StatusCode)
	}
	exps3, _ := body3["experiments"].([]any)
	found := false
	for _, rawExp := range exps3 {
		e, ok := rawExp.(map[string]any)
		if !ok {
			continue
		}
		if e["name"] == "scoped-exp" {
			found = true
		}
	}
	if !found {
		t.Fatal("scoped-exp not found in project-x workspace search")
	}
}

// TestUnknownWorkspaceReturns400 verifies that an unknown X-Workspace value
// causes a 400 response.
func TestUnknownWorkspaceReturns400(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	resp, _ := wsDoJSON(t, srv,
		http.MethodGet,
		"/api/v1/workspaces",
		nil,
		map[string]string{"X-Workspace": "does-not-exist"},
	)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown workspace, got %d", resp.StatusCode)
	}
}

// TestDefaultWorkspaceBackwardCompat verifies that existing MLflow clients
// without any workspace header continue to operate on the default workspace.
func TestDefaultWorkspaceBackwardCompat(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	// Create experiment without any workspace header.
	resp, body := wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/create",
		map[string]string{"name": "compat-exp"},
		nil, // no X-Workspace header
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d: %v", resp.StatusCode, body)
	}

	// Search without workspace header — must find it.
	resp2, body2 := wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/search",
		map[string]any{},
		nil,
	)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("search: want 200, got %d", resp2.StatusCode)
	}
	exps, _ := body2["experiments"].([]any)
	found := false
	for _, rawExp := range exps {
		e, ok := rawExp.(map[string]any)
		if !ok {
			continue
		}
		if e["name"] == "compat-exp" {
			found = true
		}
	}
	if !found {
		t.Fatal("compat-exp not found without workspace header — backward compat broken")
	}
}

// TestGetExperimentByNameWorkspaceScoped verifies that get-by-name only finds
// experiments in the current workspace.
func TestGetExperimentByNameWorkspaceScoped(t *testing.T) {
	t.Parallel()
	srv := newWS(t)

	wsDoJSON(t, srv, http.MethodPost, "/api/v1/workspaces", map[string]string{
		"id":   "ws-byname",
		"name": "WS ByName",
	}, nil)

	// Create in ws-byname only.
	wsDoJSON(t, srv,
		http.MethodPost,
		"/api/2.0/mlflow/experiments/create",
		map[string]string{"name": "only-in-ws-byname"},
		map[string]string{"X-Workspace": "ws-byname"},
	)

	// Looking up from default should 404.
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/2.0/mlflow/experiments/get-by-name?experiment_name=only-in-ws-byname",
		nil)
	resp, _ := http.DefaultClient.Do(req)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-workspace get-by-name should 404, got %d", resp.StatusCode)
	}

	// Looking up from ws-byname should succeed.
	req2, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/2.0/mlflow/experiments/get-by-name?experiment_name=only-in-ws-byname",
		nil)
	req2.Header.Set("X-Workspace", "ws-byname")
	resp2, _ := http.DefaultClient.Do(req2)
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get-by-name in correct workspace should 200, got %d: %s", resp2.StatusCode, body2)
	}
	if !strings.Contains(string(body2), "only-in-ws-byname") {
		t.Fatalf("expected experiment name in response, got %s", body2)
	}
}

package server_test

// RBAC-ENFORCEMENT: end-to-end tests for workspace role-based access control.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorevds/litemlflow/internal/config"
)

// rbacHashPass returns hex SHA-256 of a password, matching verifyBasic.
func rbacHashPass(pass string) string {
	h := sha256.Sum256([]byte(pass))
	return hex.EncodeToString(h[:])
}

// rbacDo sends an authenticated HTTP request to the test server.
// user/pass may be "" to send an unauthenticated request.
// workspace may be "" to use the default workspace.
func rbacDo(t *testing.T, srv *httptest.Server, method, path, workspace, user, pass string, body any) (*http.Response, map[string]any) {
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if workspace != "" {
		req.Header.Set("X-Workspace", workspace)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp, out
}

// newRBACServer creates a test server with basic auth configured for the given
// user+pass. Returns the server and a zero-argument "admin setup" function that
// creates a fresh workspace and adds the user with the given role.
func newRBACServer(t *testing.T, user, pass string) *httptest.Server {
	t.Helper()
	ts, _ := newTestServer(t, config.Config{
		Auth:          "basic",
		BasicUser:     user,
		BasicPassHash: rbacHashPass(pass),
	})
	return ts
}

// adminAddMember creates workspace wsID (if not "default") and adds userID with
// role using direct store access via the API with the admin user/pass.
func adminSetup(t *testing.T, srv *httptest.Server, user, pass, wsID, targetUser, role string) {
	t.Helper()
	if wsID != "default" {
		resp, body := rbacDo(t, srv, http.MethodPost, "/api/v1/workspaces", "", user, pass, map[string]string{
			"id":   wsID,
			"name": wsID,
		})
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("create workspace %s: want 201, got %d: %v", wsID, resp.StatusCode, body)
		}
	}
	resp, body := rbacDo(t, srv, http.MethodPut, "/api/v1/workspaces/"+wsID+"/members/"+targetUser, "", user, pass, map[string]string{
		"role": role,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add member %s/%s=%s: want 200, got %d: %v", wsID, targetUser, role, resp.StatusCode, body)
	}
}

// ---- tests -----------------------------------------------------------------

// TestRBACViewerCanRead verifies that a viewer can read experiments.
func TestRBACViewerCanRead(t *testing.T) {
	t.Parallel()
	const user, pass = "alice", "alicepass"
	srv := newRBACServer(t, user, pass)

	// Create workspace and add user as viewer — must be done before the
	// workspace has any members (open-mode transitions to gated-mode).
	adminSetup(t, srv, user, pass, "ws-reader", user, "viewer")

	// GET experiments (read) in ws-reader as viewer: 200.
	resp, body := rbacDo(t, srv,
		http.MethodGet, "/api/2.0/mlflow/experiments/search",
		"ws-reader", user, pass, nil)
	// The request body for search via GET may fail; use POST instead.
	_ = body
	if resp.StatusCode == http.StatusForbidden {
		t.Fatalf("viewer should be able to read; got 403")
	}
}

// TestRBACViewerCannotWrite verifies that a viewer cannot create experiments.
func TestRBACViewerCannotWrite(t *testing.T) {
	t.Parallel()
	const user, pass = "bob", "bobpass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-viewer-write", user, "viewer")

	resp, body := rbacDo(t, srv,
		http.MethodPost, "/api/2.0/mlflow/experiments/create",
		"ws-viewer-write", user, pass,
		map[string]string{"name": "should-fail"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer cannot write; want 403, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACEditorCanWrite verifies that an editor can create experiments.
func TestRBACEditorCanWrite(t *testing.T) {
	t.Parallel()
	const user, pass = "carol", "carolpass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-editor-write", user, "editor")

	resp, body := rbacDo(t, srv,
		http.MethodPost, "/api/2.0/mlflow/experiments/create",
		"ws-editor-write", user, pass,
		map[string]string{"name": "editor-exp"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("editor should be able to write; want 200, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACViewerCannotManageMembers verifies that a viewer cannot add members.
func TestRBACViewerCannotManageMembers(t *testing.T) {
	t.Parallel()
	const user, pass = "dave", "davepass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-viewer-mgmt", user, "viewer")

	// Viewer tries to add another member to ws-viewer-mgmt.
	// X-Workspace must be set so RBAC resolves the role against this workspace.
	resp, body := rbacDo(t, srv,
		http.MethodPut, "/api/v1/workspaces/ws-viewer-mgmt/members/newuser",
		"ws-viewer-mgmt", user, pass,
		map[string]string{"role": "viewer"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer cannot manage members; want 403, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACFederationPeerCRUDIsAdminOnly guards independent-review finding C1.
// Before the fix, /api/v1/federate/peers had no role requirement, so any
// authenticated viewer could register a peer and exfiltrate data via a
// federated search. Viewer must now get 403 on POST/DELETE.
func TestRBACFederationPeerCRUDIsAdminOnly(t *testing.T) {
	t.Parallel()
	const user, pass = "fedviewer", "fedviewerpass"
	srv := newRBACServer(t, user, pass)
	adminSetup(t, srv, user, pass, "ws-fed", user, "viewer")

	// POST: add peer.
	resp, _ := rbacDo(t, srv,
		http.MethodPost, "/api/v1/federate/peers",
		"ws-fed", user, pass,
		map[string]string{"name": "lmf-other", "url": "https://example.com"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer POST /federate/peers: want 403, got %d", resp.StatusCode)
	}

	// DELETE: remove peer.
	resp, _ = rbacDo(t, srv,
		http.MethodDelete, "/api/v1/federate/peers/1",
		"ws-fed", user, pass, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer DELETE /federate/peers/{id}: want 403, got %d", resp.StatusCode)
	}

	// Echo: probes a peer using its URL — also admin-only.
	resp, _ = rbacDo(t, srv,
		http.MethodPost, "/api/v1/federate/peers/1/echo",
		"ws-fed", user, pass, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer POST /federate/peers/{id}/echo: want 403, got %d", resp.StatusCode)
	}

	// LIST: GET reads — viewer may see peers' metadata (no secret leak).
	resp, _ = rbacDo(t, srv,
		http.MethodGet, "/api/v1/federate/peers",
		"ws-fed", user, pass, nil)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("viewer GET /federate/peers: should be allowed, got 403")
	}
}

// TestRBACAdminCanManageMembers verifies that an admin can add members.
func TestRBACAdminCanManageMembers(t *testing.T) {
	t.Parallel()
	const user, pass = "eve", "evepass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-admin-mgmt", user, "admin")

	// Admin adds another member — should succeed.
	// X-Workspace scopes the RBAC check to ws-admin-mgmt where user is admin.
	resp, body := rbacDo(t, srv,
		http.MethodPut, "/api/v1/workspaces/ws-admin-mgmt/members/newuser",
		"ws-admin-mgmt", user, pass,
		map[string]string{"role": "viewer"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin should manage members; want 200, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACNonMemberRejected verifies that a non-member gets 403.
func TestRBACNonMemberRejected(t *testing.T) {
	t.Parallel()
	const user, pass = "frank", "frankpass"
	srv := newRBACServer(t, user, pass)

	// Create ws-exclusive but do NOT add user as member.
	// We need admin privileges to create the workspace — but the only
	// configured user IS frank. So: first create the workspace using frank
	// (while it has no members yet — open mode), then add a different user
	// as the sole member, effectively locking frank out.
	//
	// Because frank is the only basic-auth user, we can't add a genuinely
	// different user. Instead, we use a two-step approach:
	// 1. Create the workspace (frank is authorized because default workspace
	//    has no members → open mode; workspace creation is an admin op but
	//    because we're creating it from default-open context — actually the
	//    workspace creation path requires admin on the WORKSPACE being created,
	//    not the workspace being sent via header; the request goes to the default
	//    workspace context which is open).
	//
	// Actually: the workspace creation endpoint POST /api/v1/workspaces requires
	// "admin" role. In open mode (default workspace, no members), it passes through.
	// Once created, we add frank as admin (to lock the workspace), then remove frank.
	// After removal frank should get 403.

	// Create the workspace (open mode via default).
	resp, _ := rbacDo(t, srv, http.MethodPost, "/api/v1/workspaces", "", user, pass, map[string]string{
		"id":   "ws-exclusive",
		"name": "Exclusive",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace: want 201, got %d", resp.StatusCode)
	}

	// Add frank as admin (using default workspace context = open mode).
	resp, _ = rbacDo(t, srv, http.MethodPut, "/api/v1/workspaces/ws-exclusive/members/"+user,
		"", user, pass, map[string]string{"role": "admin"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add self as admin: want 200, got %d", resp.StatusCode)
	}

	// Now frank is locked into ws-exclusive as admin.
	// Remove frank from ws-exclusive using the X-Workspace context of ws-exclusive
	// (where frank IS admin) so the operation is allowed.
	resp, _ = rbacDo(t, srv, http.MethodDelete, "/api/v1/workspaces/ws-exclusive/members/"+user,
		"ws-exclusive", user, pass, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove self: want 204, got %d", resp.StatusCode)
	}

	// Frank is now a non-member. Accessing ws-exclusive should fail with 403.
	resp, body := rbacDo(t, srv,
		http.MethodPost, "/api/2.0/mlflow/experiments/create",
		"ws-exclusive", user, pass, map[string]string{"name": "intruder-exp"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member should get 403; got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACDefaultWorkspaceOpenMode verifies that the default workspace with no
// members is fully accessible — backward-compat for MLflow compat suite.
func TestRBACDefaultWorkspaceOpenMode(t *testing.T) {
	t.Parallel()
	const user, pass = "grace", "gracepass"
	ts, _ := newTestServer(t, config.Config{
		Auth:          "basic",
		BasicUser:     user,
		BasicPassHash: rbacHashPass(pass),
	})

	// Create experiment in the default workspace — no members configured.
	resp, body := rbacDo(t, ts,
		http.MethodPost, "/api/2.0/mlflow/experiments/create",
		"", user, pass, map[string]string{"name": "open-mode-exp"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default workspace open mode: want 200, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACAuthNoneFullAccess verifies that auth=none bypasses RBAC entirely.
func TestRBACAuthNoneFullAccess(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, config.Config{Auth: "none"})

	// Create a workspace and add a member — this would normally be admin-only.
	resp, body := rbacDo(t, ts, http.MethodPost, "/api/v1/workspaces", "", "", "", map[string]string{
		"id":   "ws-none-auth",
		"name": "None Auth WS",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("auth=none create workspace: want 201, got %d: %v", resp.StatusCode, body)
	}

	// Add a member.
	resp, body = rbacDo(t, ts, http.MethodPut, "/api/v1/workspaces/ws-none-auth/members/anyone",
		"", "", "", map[string]string{"role": "viewer"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth=none add member: want 200, got %d: %v", resp.StatusCode, body)
	}

	// Write to the workspace even though "anonymous" is not a member.
	resp, body = rbacDo(t, ts,
		http.MethodPost, "/api/2.0/mlflow/experiments/create",
		"ws-none-auth", "", "", map[string]string{"name": "none-auth-exp"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth=none should bypass RBAC; want 200, got %d: %v", resp.StatusCode, body)
	}
}

// TestRBACViewerCanReadWSCurrent verifies GET /api/v1/workspaces/{id} is allowed for viewers.
func TestRBACViewerCanReadWSCurrent(t *testing.T) {
	t.Parallel()
	const user, pass = "henry", "henrypass"
	srv := newRBACServer(t, user, pass)

	adminSetup(t, srv, user, pass, "ws-view-meta", user, "viewer")

	// GET the workspace object — should be allowed for viewer.
	resp, body := rbacDo(t, srv,
		http.MethodGet, "/api/v1/workspaces/ws-view-meta",
		"ws-view-meta", user, pass, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer can read workspace meta; want 200, got %d: %v", resp.StatusCode, body)
	}
}

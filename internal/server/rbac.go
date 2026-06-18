package server

import (
	"net/http"
	"strings"
)

// requiredRole returns the minimum role a user must hold in the current
// workspace to perform the given HTTP (method, path) operation.
//
// Return values:
//   - "viewer"  — read-only access (any authenticated member)
//   - "editor"  — write access (editor or admin)
//   - "admin"   — workspace management (admin only)
//   - ""        — no role requirement (public / non-workspace paths)
//
// Path-prefix matching rules (first match wins):
//
//  1. Admin paths — workspace settings and member management:
//     PATCH/DELETE /api/v1/workspaces/{id}
//     PUT/DELETE   /api/v1/workspaces/{id}/members/...
//     POST         /api/v1/workspaces          (create a new workspace)
//
//  2. Editor paths — mutating experiment/run/metric data:
//     POST/PUT/DELETE /api/2.0/mlflow/...
//     POST /api/v1/traces, /api/v1/prompts, /api/v1/evals, /v1/traces
//
//  3. Viewer paths — all remaining reads within the API surface:
//     GET /api/2.0/mlflow/...
//     GET /api/v1/...   (non-public, non-workspace-management)
//
//  4. No requirement — health / meta / OIDC / UI / /metrics:
//     /healthz /readyz /version /metrics /ui /api/v1/auth/...
func requiredRole(method, path string) string {
	// --- Admin operations ---

	// Workspace lifecycle: POST create, PATCH update, DELETE delete.
	if path == "/api/v1/workspaces" && method == http.MethodPost {
		return "admin"
	}
	if strings.HasPrefix(path, "/api/v1/workspaces/") {
		// Member management: PUT and DELETE on /api/v1/workspaces/{id}/members/...
		if strings.Contains(path, "/members") &&
			(method == http.MethodPut || method == http.MethodDelete) {
			return "admin"
		}
		// Workspace update / delete.
		if method == http.MethodPatch || method == http.MethodDelete {
			return "admin"
		}
	}

	// Federation peer CRUD is admin-only — operators register, probe, or
	// remove peers; viewers can only LIST (GET) for transparency. The
	// peer-to-peer endpoints /api/v1/federate/echo and /federate/search
	// authenticate via mutual HMAC and are exempted in middleware.go's
	// isPublicPath, not here.
	if strings.HasPrefix(path, "/api/v1/federate/peers") && method != http.MethodGet {
		return "admin"
	}

	// --- Editor operations (mutating MLflow and native write endpoints) ---

	if strings.HasPrefix(path, "/api/2.0/mlflow/") &&
		(method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete) {
		return "editor"
	}
	if method == http.MethodPost {
		switch {
		case strings.HasPrefix(path, "/api/v1/traces"),
			strings.HasPrefix(path, "/api/v1/prompts"),
			strings.HasPrefix(path, "/api/v1/evals"),
			strings.HasPrefix(path, "/v1/traces"):
			return "editor"
		}
	}

	// --- Reads exposed as POST require only viewer ---
	//
	// Analytics queries are read-only but use POST to carry a JSON DSL body.
	// Without this explicit rule the default-deny safety net below would
	// over-gate them to editor, locking viewers out of dashboards.
	if path == "/api/v1/analytics/query" {
		return "viewer"
	}

	// --- No role requirement for public / meta paths ---

	switch path {
	case "/healthz", "/readyz", "/version", "/metrics":
		return ""
	}
	if strings.HasPrefix(path, "/ui/") || path == "/ui" || path == "/" {
		return ""
	}
	if strings.HasPrefix(path, "/api/v1/auth/") {
		return ""
	}
	// Peer-to-peer federation endpoints authenticate via mutual HMAC, not a
	// workspace role. They are already auth-exempt in isPublicPath
	// (middleware.go); this entry is defense-in-depth so the default-deny net
	// below cannot accidentally demand editor if the layers are reordered.
	switch path {
	case "/api/v1/federate/echo", "/api/v1/federate/search":
		return ""
	}

	// --- Default-deny: any other mutating request within the API surface
	// requires at least editor. This is a safety net so that newly added
	// write endpoints (or ones not explicitly enumerated above) are gated by
	// default instead of silently falling through to public access. The
	// mlflow-artifacts/ prefix is distinct from mlflow/ and carries the
	// largest write surface (PUT/DELETE artifact upload), so it is listed
	// explicitly. ---
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		if strings.HasPrefix(path, "/api/v1/") ||
			strings.HasPrefix(path, "/api/2.0/mlflow/") ||
			strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/") ||
			strings.HasPrefix(path, "/v1/") {
			return "editor"
		}
	}

	// --- Viewer for all remaining reads (GET /api/...) ---

	if method == http.MethodGet {
		if strings.HasPrefix(path, "/api/2.0/mlflow/") ||
			strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/") ||
			strings.HasPrefix(path, "/api/v1/") {
			return "viewer"
		}
	}

	// Anything else (unrecognised path, OPTIONS, etc.) — no requirement.
	return ""
}

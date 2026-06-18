package server

// RBAC-ENFORCEMENT: table-driven unit test for requiredRole. Guards independent
// review finding 2.2 — before the default-deny safety net, most mutating native
// endpoints (run note, dataset versions, webhooks, dashboards, experiment clone)
// fell through to no role requirement, letting a workspace viewer perform writes.
//
// This test pins the (method, path) -> minimum-role contract for every mounted
// route so a newly added write endpoint cannot silently regress to public.

import (
	"net/http"
	"testing"
)

func TestRequiredRoleTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		method string
		path   string
		want   string
	}{
		// --- Admin: workspace + federation peer management ---
		{"create workspace", http.MethodPost, "/api/v1/workspaces", "admin"},
		{"update workspace", http.MethodPatch, "/api/v1/workspaces/ws1", "admin"},
		{"delete workspace", http.MethodDelete, "/api/v1/workspaces/ws1", "admin"},
		{"add member", http.MethodPut, "/api/v1/workspaces/ws1/members/u1", "admin"},
		{"remove member", http.MethodDelete, "/api/v1/workspaces/ws1/members/u1", "admin"},
		{"add peer", http.MethodPost, "/api/v1/federate/peers", "admin"},
		{"delete peer", http.MethodDelete, "/api/v1/federate/peers/1", "admin"},
		{"echo peer", http.MethodPost, "/api/v1/federate/peers/1/echo", "admin"},

		// --- Editor: explicitly enumerated mutating endpoints ---
		{"mlflow create experiment", http.MethodPost, "/api/2.0/mlflow/experiments/create", "editor"},
		{"mlflow delete run", http.MethodDelete, "/api/2.0/mlflow/runs/delete", "editor"},
		{"ingest traces", http.MethodPost, "/api/v1/traces", "editor"},
		{"otlp traces", http.MethodPost, "/v1/traces", "editor"},
		{"create prompt", http.MethodPost, "/api/v1/prompts", "editor"},
		{"set prompt alias", http.MethodPost, "/api/v1/prompts/p1/aliases", "editor"},
		{"create eval", http.MethodPost, "/api/v1/evals", "editor"},

		// --- Editor: default-deny safety net for the routes that used to be open ---
		{"set run note", http.MethodPut, "/api/v1/runs/r1/note", "editor"},
		{"create dataset version", http.MethodPost, "/api/v1/datasets/d1/versions", "editor"},
		{"delete dataset version", http.MethodDelete, "/api/v1/datasets/d1/versions/1", "editor"},
		{"create webhook", http.MethodPost, "/api/v1/webhooks", "editor"},
		{"update webhook", http.MethodPatch, "/api/v1/webhooks/1", "editor"},
		{"delete webhook", http.MethodDelete, "/api/v1/webhooks/1", "editor"},
		{"test webhook", http.MethodPost, "/api/v1/webhooks/1/test", "editor"},
		{"save dashboard", http.MethodPut, "/api/v1/dashboards/proj1", "editor"},
		{"clone experiment", http.MethodPost, "/api/v1/experiments/e1/clone", "editor"},
		{"upload artifact", http.MethodPut, "/api/2.0/mlflow-artifacts/artifacts/r1/model.pkl", "editor"},
		{"delete artifact", http.MethodDelete, "/api/2.0/mlflow-artifacts/artifacts/r1/model.pkl", "editor"},

		// --- Viewer: reads, including the analytics read-as-POST ---
		{"analytics query (read-as-POST)", http.MethodPost, "/api/v1/analytics/query", "viewer"},
		{"global search", http.MethodGet, "/api/v1/search", "viewer"},
		{"list datasets", http.MethodGet, "/api/v1/datasets", "viewer"},
		{"get run note", http.MethodGet, "/api/v1/runs/r1/note", "viewer"},
		{"list peers", http.MethodGet, "/api/v1/federate/peers", "viewer"},
		{"mlflow get run", http.MethodGet, "/api/2.0/mlflow/runs/get", "viewer"},
		{"download artifact", http.MethodGet, "/api/2.0/mlflow-artifacts/artifacts/r1/model.pkl", "viewer"},
		{"read workspace meta", http.MethodGet, "/api/v1/workspaces/ws1", "viewer"},

		// --- No requirement: public / meta / HMAC peer-to-peer ---
		{"healthz", http.MethodGet, "/healthz", ""},
		{"readyz", http.MethodGet, "/readyz", ""},
		{"version", http.MethodGet, "/version", ""},
		{"metrics", http.MethodGet, "/metrics", ""},
		{"ui", http.MethodGet, "/ui/index.html", ""},
		{"login", http.MethodPost, "/api/v1/auth/login", ""},
		{"oidc callback", http.MethodGet, "/api/v1/auth/oidc/callback", ""},
		{"federate echo (HMAC)", http.MethodPost, "/api/v1/federate/echo", ""},
		{"federate search (HMAC)", http.MethodPost, "/api/v1/federate/search", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requiredRole(tc.method, tc.path); got != tc.want {
				t.Errorf("requiredRole(%q, %q) = %q, want %q", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

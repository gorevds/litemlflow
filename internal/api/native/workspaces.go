package native

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/litemlflow/litemlflow/internal/model"
)

// mountWorkspaceRoutes registers all workspace endpoints on the router.
// Called from Handler.Mount.
//
// TENANCY: workspace endpoints
func (h *Handler) mountWorkspaceRoutes(r chi.Router) {
	r.Get("/api/v1/workspaces", h.ListWorkspaces)
	r.Post("/api/v1/workspaces", h.CreateWorkspace)
	// /current must be registered before /{id} to avoid shadowing.
	r.Get("/api/v1/workspaces/current", h.GetCurrentWorkspace)
	r.Get("/api/v1/workspaces/{id}", h.GetWorkspace)
	r.Patch("/api/v1/workspaces/{id}", h.UpdateWorkspace)
	r.Delete("/api/v1/workspaces/{id}", h.DeleteWorkspace)
	r.Get("/api/v1/workspaces/{id}/members", h.ListMembers)
	r.Put("/api/v1/workspaces/{id}/members/{userID}", h.AddMember)
	r.Delete("/api/v1/workspaces/{id}/members/{userID}", h.RemoveMember)
}

// ---- workspace CRUD ---------------------------------------------------------

// ListWorkspaces handles GET /api/v1/workspaces.
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	ws, err := h.Store.ListWorkspaces(r.Context())
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if ws == nil {
		ws = []*model.Workspace{}
	}
	writeJSON(w, map[string]any{"workspaces": ws})
}

type createWorkspaceReq struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateWorkspace handles POST /api/v1/workspaces.
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createWorkspaceReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "id and name are required")
		return
	}
	ws := &model.Workspace{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
	}
	if err := h.Store.CreateWorkspace(r.Context(), ws); err != nil {
		writeStoreErr(w, err)
		return
	}
	// Reload so we return the server-set timestamps.
	created, err := h.Store.GetWorkspace(r.Context(), ws.ID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

// GetCurrentWorkspace handles GET /api/v1/workspaces/current.
// Returns the workspace currently selected for this request and the caller's
// role (if any).
//
// The workspace id is read from the X-LiteMLflow-Workspace header, which is
// set by workspaceMiddleware (in internal/server). This avoids a circular
// import between the native and server packages.
func (h *Handler) GetCurrentWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID := r.Header.Get("X-LiteMLflow-Workspace")
	if wsID == "" {
		wsID = "default"
	}
	ws, err := h.Store.GetWorkspace(r.Context(), wsID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Best-effort role lookup — auth is opaque to this layer.
	user := r.Header.Get("X-LiteMLflow-User")
	role := ""
	if user != "" && user != "anonymous" {
		role, _ = h.Store.GetMemberRole(r.Context(), wsID, user)
	}
	writeJSON(w, map[string]any{
		"workspace": ws,
		"user":      user,
		"role":      role,
	})
}

// GetWorkspace handles GET /api/v1/workspaces/{id}.
func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ws, err := h.Store.GetWorkspace(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, ws)
}

type updateWorkspaceReq struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// UpdateWorkspace handles PATCH /api/v1/workspaces/{id}.
func (h *Handler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateWorkspaceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := h.Store.UpdateWorkspace(r.Context(), id, req.Name, req.Description); err != nil {
		writeStoreErr(w, err)
		return
	}
	ws, err := h.Store.GetWorkspace(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, ws)
}

// DeleteWorkspace handles DELETE /api/v1/workspaces/{id}.
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Store.DeleteWorkspace(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- workspace members ------------------------------------------------------

// ListMembers handles GET /api/v1/workspaces/{id}/members.
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	// Verify workspace exists.
	if _, err := h.Store.GetWorkspace(r.Context(), wsID); err != nil {
		writeStoreErr(w, err)
		return
	}
	members, err := h.Store.ListMembers(r.Context(), wsID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if members == nil {
		members = []*model.WorkspaceMember{}
	}
	writeJSON(w, map[string]any{"members": members})
}

type setMemberRoleReq struct {
	Role string `json:"role"`
}

// AddMember handles PUT /api/v1/workspaces/{id}/members/{userID}.
func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userID")
	var req setMemberRoleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Role == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "role is required")
		return
	}
	if err := h.Store.AddMember(r.Context(), wsID, userID, req.Role); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"workspace_id": wsID, "user_id": userID, "role": req.Role})
}

// RemoveMember handles DELETE /api/v1/workspaces/{id}/members/{userID}.
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	wsID := chi.URLParam(r, "id")
	userID := chi.URLParam(r, "userID")
	if err := h.Store.RemoveMember(r.Context(), wsID, userID); err != nil {
		writeStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package native

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/webhooks"
)

// mountWebhookRoutes registers webhook CRUD routes on the router.
// Called from Handler.Mount.
func (h *Handler) mountWebhookRoutes(r chi.Router) {
	r.Get("/api/v1/webhooks", h.ListWebhooks)
	r.Post("/api/v1/webhooks", h.CreateWebhook)
	r.Patch("/api/v1/webhooks/{id}", h.UpdateWebhook)
	r.Delete("/api/v1/webhooks/{id}", h.DeleteWebhook)
	r.Post("/api/v1/webhooks/{id}/test", h.TestWebhook)

	// Run lineage.
	r.Get("/api/v1/runs/{runID}/lineage", h.GetRunLineage)

	// Experiment clone.
	r.Post("/api/v1/experiments/{id}/clone", h.CloneExperiment)
}

// ---- webhook CRUD -----------------------------------------------------------

type createWebhookReq struct {
	Name         string  `json:"name"`
	URL          string  `json:"url"`
	Events       string  `json:"events"`
	ExperimentID *int64  `json:"experiment_id,omitempty"`
	Secret       string  `json:"secret,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
}

func workspaceFromReq(r *http.Request) string {
	if ws := r.Header.Get("X-LiteMLflow-Workspace"); ws != "" {
		return ws
	}
	return "default"
}

// ListWebhooks handles GET /api/v1/webhooks.
func (h *Handler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromReq(r)
	whs, err := h.Store.ListWebhooks(r.Context(), ws, nil)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if whs == nil {
		whs = []*model.Webhook{}
	}
	writeJSON(w, map[string]any{"webhooks": whs})
}

// CreateWebhook handles POST /api/v1/webhooks.
func (h *Handler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req createWebhookReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.URL == "" || req.Events == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name, url, and events are required")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	wh := &model.Webhook{
		Name:         req.Name,
		URL:          req.URL,
		Events:       req.Events,
		ExperimentID: req.ExperimentID,
		WorkspaceID:  workspaceFromReq(r),
		Secret:       req.Secret,
		CreatedAt:    time.Now().UnixMilli(),
		Enabled:      enabled,
	}
	id, err := h.Store.CreateWebhook(r.Context(), wh)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	wh.ID = id
	writeJSON(w, wh)
}

// UpdateWebhook handles PATCH /api/v1/webhooks/{id}.
func (h *Handler) UpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "id must be an integer")
		return
	}
	existing, err := h.Store.GetWebhook(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var req createWebhookReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.URL != "" {
		existing.URL = req.URL
	}
	if req.Events != "" {
		existing.Events = req.Events
	}
	if req.ExperimentID != nil {
		existing.ExperimentID = req.ExperimentID
	}
	if req.Secret != "" {
		existing.Secret = req.Secret
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := h.Store.UpdateWebhook(r.Context(), existing); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, existing)
}

// DeleteWebhook handles DELETE /api/v1/webhooks/{id}.
func (h *Handler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "id must be an integer")
		return
	}
	if err := h.Store.DeleteWebhook(r.Context(), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// TestWebhook handles POST /api/v1/webhooks/{id}/test.
// Delivers a synthetic run_finished event immediately, bypassing the queue.
func (h *Handler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "id must be an integer")
		return
	}
	wh, err := h.Store.GetWebhook(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}

	// Build a synthetic run.
	now := time.Now().UnixMilli()
	syntheticRun := &model.Run{
		ID:           "test-synthetic-run",
		ExperimentID: 0,
		Name:         "synthetic test run",
		Status:       model.StatusFinished,
		StartTime:    now - 1000,
		ArtifactURI:  "mlflow-artifacts:/test",
		LifecycleStage: model.LifecycleActive,
		Kind:         model.KindClassic,
	}
	if wh.ExperimentID != nil {
		syntheticRun.ExperimentID = *wh.ExperimentID
	}
	endTime := now
	syntheticRun.EndTime = &endTime

	// Deliver synchronously for the test endpoint.
	dispatcher := &webhooks.SyncDelivery{}
	status, deliveryErr := dispatcher.Deliver(wh, "run_finished", syntheticRun)
	if deliveryErr != nil {
		_ = h.Store.RecordWebhookAttempt(r.Context(), id, 0, now)
		writeError(w, http.StatusBadGateway, "DELIVERY_FAILED", deliveryErr.Error())
		return
	}
	_ = h.Store.RecordWebhookAttempt(r.Context(), id, status, now)
	writeJSON(w, map[string]any{"ok": true, "status": status})
}

// ---- run lineage ------------------------------------------------------------

// GetRunLineage handles GET /api/v1/runs/{runID}/lineage.
func (h *Handler) GetRunLineage(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	lineage, err := h.Store.GetRunLineage(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, lineage)
}

// ---- experiment clone -------------------------------------------------------

type cloneExperimentReq struct {
	Name string `json:"name,omitempty"`
}

// CloneExperiment handles POST /api/v1/experiments/{id}/clone.
func (h *Handler) CloneExperiment(w http.ResponseWriter, r *http.Request) {
	srcID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "id must be an integer")
		return
	}
	var req cloneExperimentReq
	_ = decodeJSON(r, &req) // body is optional

	var newName string
	if req.Name != "" {
		newName = req.Name
	} else {
		// Auto-suffix: <source>-clone-<ts>
		src, err := h.Store.GetExperiment(r.Context(), srcID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		newName = src.Name + "-clone-" + strconv.FormatInt(time.Now().Unix(), 10)
	}

	ws := workspaceFromReq(r)
	newExp, err := h.Store.CloneExperiment(r.Context(), srcID, newName, ws)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, newExp)
}


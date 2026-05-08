package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/webhooks"
)

// validateWebhookURL rejects URLs that target private/loopback/link-local
// addresses, defending against SSRF — a webhook URL is operator-controlled
// data, but in semi-trusted deployments (multi-tenant, hosted) it would let
// an attacker probe internal services or reach the cloud-metadata endpoint.
//
// Operators who genuinely need a webhook to a private address can set
// LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE=1 to disable this check at server start.
func validateWebhookURL(rawURL string) error {
	// Special-case the in-process echo target (used by the live demo).
	if webhooks.IsEchoURL(rawURL) {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https; got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("url is missing host")
	}
	// Allow override at server level for legitimate intra-cluster delivery.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE")), "1") {
		return nil
	}
	// Resolve the host to one or more IPs and reject if any is private/loopback.
	// (We resolve here so a name like "localhost" is caught even though it
	// isn't a literal IP. This is best-effort — DNS rebinding could still
	// bypass it, which is why operators behind multi-tenant deployments
	// should put a network egress filter in front.)
	// Use a background context for the lookup; we cap implicitly via DNS
	// timeout in the resolver.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		// If we can't resolve, allow but log — strict-blocking on resolve
		// failure would break offline / split-DNS deployments.
		return nil
	}
	for _, ipa := range ips {
		if isBlockedIP(ipa.IP) {
			return fmt.Errorf("webhook url resolves to a blocked address (%s); set LITEMLFLOW_WEBHOOK_ALLOW_PRIVATE=1 to override", ipa.IP)
		}
	}
	return nil
}


// isBlockedIP returns true for loopback (127.0.0.0/8, ::1), link-local
// (169.254.0.0/16, fe80::/10 — including AWS metadata 169.254.169.254),
// RFC1918 private ranges (10/8, 172.16/12, 192.168/16), and unique-local
// IPv6 (fc00::/7).
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	return false
}

// mountWebhookRoutes registers webhook CRUD routes on the router.
// Called from Handler.Mount.
func (h *Handler) mountWebhookRoutes(r chi.Router) {
	r.Get("/api/v1/webhooks", h.ListWebhooks)
	r.Post("/api/v1/webhooks", h.CreateWebhook)
	r.Patch("/api/v1/webhooks/{id}", h.UpdateWebhook)
	r.Delete("/api/v1/webhooks/{id}", h.DeleteWebhook)
	r.Post("/api/v1/webhooks/{id}/test", h.TestWebhook)

	// Echo log: in-process ring buffer of deliveries to lmf://echo URLs.
	// Provides a zero-setup demo target on the public deploy.
	r.Get("/api/v1/webhooks/echo", h.ListEchoDeliveries)

	// Run lineage.
	r.Get("/api/v1/runs/{runID}/lineage", h.GetRunLineage)

	// Experiment clone.
	r.Post("/api/v1/experiments/{id}/clone", h.CloneExperiment)

	// Dashboards (W8.5).
	r.Get("/api/v1/dashboards/{project}", h.GetDashboard)
	r.Put("/api/v1/dashboards/{project}", h.SaveDashboard)
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
	out := make([]webhookDTO, 0, len(whs))
	for _, wh := range whs {
		out = append(out, webhookToDTO(wh))
	}
	writeJSON(w, map[string]any{"webhooks": out})
}

// ---- webhookDTO (T4.21) -----------------------------------------------------
//
// Why an explicit DTO: the previous code returned `*model.Webhook` directly
// via writeJSON, leaking internal field names + future schema changes onto
// the API surface. Locking down the wire shape before v2.0 freeze.

type webhookDTO struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Events        string `json:"events"`
	ExperimentID  *int64 `json:"experiment_id,omitempty"`
	WorkspaceID   string `json:"workspace_id"`
	HasSecret     bool   `json:"has_secret"`
	CreatedAt     int64  `json:"created_at"`
	LastStatus    *int   `json:"last_status,omitempty"`
	LastAttempt   *int64 `json:"last_attempt,omitempty"`
	Enabled       bool   `json:"enabled"`
}

func webhookToDTO(wh *model.Webhook) webhookDTO {
	return webhookDTO{
		ID: wh.ID, Name: wh.Name, URL: wh.URL, Events: wh.Events,
		ExperimentID: wh.ExperimentID, WorkspaceID: wh.WorkspaceID,
		HasSecret:   wh.Secret != "",
		CreatedAt:   wh.CreatedAt,
		LastStatus:  wh.LastStatus,
		LastAttempt: wh.LastAttempt,
		Enabled:     wh.Enabled,
	}
}

// CreateWebhook handles POST /api/v1/webhooks.
func (h *Handler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req createWebhookReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.URL == "" || req.Events == "" {
		writeMissingField(w, "name+url+events")
		return
	}
	if err := validateWebhookURL(req.URL); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
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
	writeJSON(w, webhookToDTO(wh))
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
		if err := validateWebhookURL(req.URL); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
			return
		}
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
	writeJSON(w, webhookToDTO(existing))
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

	// Deliver synchronously for the test endpoint. Wire EchoLog so lmf://echo
	// targets get recorded in the in-process ring buffer.
	dispatcher := &webhooks.SyncDelivery{Echo: h.EchoLog}
	status, deliveryErr := dispatcher.Deliver(wh, "run_finished", syntheticRun)
	if deliveryErr != nil {
		_ = h.Store.RecordWebhookAttempt(r.Context(), id, 0, now)
		writeError(w, http.StatusBadGateway, "DELIVERY_FAILED", deliveryErr.Error())
		return
	}
	_ = h.Store.RecordWebhookAttempt(r.Context(), id, status, now)
	writeJSON(w, map[string]any{"ok": true, "status": status})
}

// ---- echo log ---------------------------------------------------------------

// ListEchoDeliveries handles GET /api/v1/webhooks/echo?max=N.
// Returns recent webhook deliveries that targeted the in-process lmf://echo
// URL. Used by the UI to demonstrate webhook firing on the public demo.
func (h *Handler) ListEchoDeliveries(w http.ResponseWriter, r *http.Request) {
	max := 50
	if v := r.URL.Query().Get("max"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			max = n
		}
	}
	if h.EchoLog == nil {
		writeJSON(w, map[string]any{"entries": []any{}})
		return
	}
	entries := h.EchoLog.List(max)
	writeJSON(w, map[string]any{"entries": entries})
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

// ---- dashboards -------------------------------------------------------------

// GetDashboard handles GET /api/v1/dashboards/{project}.
// Returns the dashboard row or, if none exists, an empty dashboard so the
// UI can render the "+ Add widget" empty state without a separate code path.
func (h *Handler) GetDashboard(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	ws := workspaceFromReq(r)
	d, err := h.Store.GetDashboard(r.Context(), ws, project)
	if err != nil {
		// Synthesize an empty dashboard rather than returning 404.
		writeJSON(w, &model.Dashboard{
			WorkspaceID: ws,
			Project:     project,
			Widgets:     "[]",
		})
		return
	}
	writeJSON(w, d)
}

// SaveDashboard handles PUT /api/v1/dashboards/{project}.
// Body: { "widgets": "[{...}]" } — widgets is the JSON array as a string,
// because storing as a column we want to preserve client-controlled key order.
type saveDashboardReq struct {
	Widgets json.RawMessage `json:"widgets"`
}

func (h *Handler) SaveDashboard(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	ws := workspaceFromReq(r)
	var req saveDashboardReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	widgets := string(req.Widgets)
	if widgets == "" || widgets == "null" {
		widgets = "[]"
	}
	// Cap size at 64 KiB to prevent storage abuse via the dashboard endpoint.
	const maxWidgetsBytes = 64 * 1024
	if len(widgets) > maxWidgetsBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
			fmt.Sprintf("widgets payload exceeds %d bytes", maxWidgetsBytes))
		return
	}
	// Quick sanity check: must be valid JSON array.
	var sanity []any
	if err := json.Unmarshal([]byte(widgets), &sanity); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"widgets must be a JSON array")
		return
	}
	d, err := h.Store.SaveDashboard(r.Context(), ws, project, widgets)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, d)
}


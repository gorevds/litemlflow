// Package native implements the LiteMLflow-native REST API.
//
// This API exposes capabilities outside MLflow's protocol: traces, prompts,
// evals, OTLP ingest. Versioned independently of MLflow compat.
package native

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/auth"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
	"github.com/gorevds/litemlflow/internal/webhooks"
	"github.com/gorevds/litemlflow/pkg/version"
)

// Handler bundles dependencies for the native API.
// AUTH-OIDC: Config and SessionStore added to support auth endpoints.
type Handler struct {
	Store        store.Store
	Cfg          config.Config
	SessionStore SessionStore
	OIDCProvider OIDCProvider // nil when auth != "oidc"
	EchoLog      *webhooks.EchoLog
}

// SessionStore is the session-persistence interface used by auth handlers.
// *store.SQLiteStore satisfies this interface (methods in store/sessions.go).
type SessionStore interface {
	CreateSession(ctx context.Context, sess *model.Session) error
	GetSession(ctx context.Context, id string) (*model.Session, error)
	DeleteSession(ctx context.Context, id string) error
	TouchSession(ctx context.Context, id string, lastSeen int64) error
}

// OIDCProvider is the minimal interface the handler needs from auth.Provider.
type OIDCProvider interface {
	BeginPKCE(ctx context.Context, state, verifier, nonce string) (string, error)
	Exchange(ctx context.Context, code, verifier, expectedNonce string) (string, map[string]any, error)
}

// Mount registers the native API on the given router.
func (h *Handler) Mount(r chi.Router) {
	// Health/meta
	r.Get("/healthz", h.Healthz)
	r.Get("/readyz", h.Readyz)
	r.Get("/version", h.Version)

	// Traces
	r.Post("/api/v1/traces", h.IngestTraces)
	r.Get("/api/v1/runs/{runID}/traces", h.GetRunTraces)
	r.Get("/api/v1/runs/{runID}/data", h.GetRunData)
	r.Post("/v1/traces", h.IngestOTLP)

	// Prompts
	r.Get("/api/v1/prompts", h.ListPrompts)
	r.Post("/api/v1/prompts", h.CreatePrompt)
	r.Get("/api/v1/prompts/{name}", h.GetLatestPrompt)
	r.Get("/api/v1/prompts/{name}/versions", h.ListPromptVersions)
	r.Get("/api/v1/prompts/{name}/versions/{version}", h.GetPromptVersion)
	r.Post("/api/v1/prompts/{name}/aliases", h.SetPromptAlias)
	r.Get("/api/v1/prompts/{name}/aliases/{alias}", h.GetPromptByAlias)

	// Evals
	r.Post("/api/v1/evals", h.CreateEval)
	r.Get("/api/v1/evals/{runID}", h.GetEval)

	// AUTH-OIDC: real auth endpoints (were placeholder stubs in v0.1)
	r.Get("/api/v1/auth/whoami", h.Whoami)
	r.Post("/api/v1/auth/login", h.Login)
	r.Post("/api/v1/auth/logout", h.Logout)
	r.Get("/api/v1/auth/oidc/start", h.OIDCStart)
	r.Get("/api/v1/auth/oidc/callback", h.OIDCCallback)

	// TENANCY: workspace endpoints
	h.mountWorkspaceRoutes(r)

	// PROJECTS: list distinct lmf.project tag values in the current workspace.
	r.Get("/api/v1/projects", h.ListProjects)

	// Run notes (markdown).
	r.Get("/api/v1/runs/{runID}/note", h.GetRunNote)
	r.Put("/api/v1/runs/{runID}/note", h.SetRunNote)

	// SEARCH: cross-experiment search — runs by name, experiments by name, prompts by name.
	r.Get("/api/v1/search", h.GlobalSearch)

	// Webhooks, lineage, and experiment clone (W7.C).
	h.mountWebhookRoutes(r)
}

// projectDTO is one row in the projects-list response.
type projectDTO struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ListProjects handles GET /api/v1/projects.
//
// Returns the set of distinct values of the experiment tag `lmf.project` in
// the workspace resolved from the request (X-Workspace header or default).
// The empty-string bucket counts experiments that have no project assigned.
//
// Why a tag and not a separate table: keeps the data model minimal, makes
// every existing MLflow client able to set/clear projects via the standard
// `set-experiment-tag` endpoint, and avoids a schema migration. The UI
// presents the result as if it were first-class.
func (h *Handler) ListProjects(w http.ResponseWriter, r *http.Request) {
	ws := r.Header.Get("X-LiteMLflow-Workspace")
	if ws == "" {
		ws = "default"
	}
	projects, err := h.Store.ListProjects(r.Context(), ws)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]projectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectDTO{Name: p.Name, Count: p.Count})
	}
	writeJSON(w, map[string]any{"projects": out, "tag_key": "lmf.project"})
}

// ---- run notes --------------------------------------------------------------

type runNoteResp struct {
	Content   string `json:"content"`
	UpdatedAt int64  `json:"updated_at"`
	UpdatedBy string `json:"updated_by,omitempty"`
}

// GetRunNote handles GET /api/v1/runs/{runID}/note.
// Returns 404 when no note has been set.
func (h *Handler) GetRunNote(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	content, by, at, err := h.Store.GetRunNote(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, runNoteResp{Content: content, UpdatedAt: at, UpdatedBy: by})
}

type setRunNoteReq struct {
	Content string `json:"content"`
}

// SetRunNote handles PUT /api/v1/runs/{runID}/note.
// Body: {"content": "..."} — empty content deletes the note.
func (h *Handler) SetRunNote(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	var req setRunNoteReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	user := r.Header.Get("X-LiteMLflow-User")
	if err := h.Store.SetRunNote(r.Context(), runID, req.Content, user); err != nil {
		writeStoreErr(w, err)
		return
	}
	if req.Content == "" {
		writeJSON(w, map[string]bool{"deleted": true})
		return
	}
	// Return the stored note so the UI can immediately refresh.
	content, by, at, err := h.Store.GetRunNote(r.Context(), runID)
	if err != nil {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, runNoteResp{Content: content, UpdatedAt: at, UpdatedBy: by})
}

// ---- global search ----------------------------------------------------------

// searchResultItem is one hit in the /api/v1/search response.
type searchResultItem struct {
	Kind         string `json:"kind"`                    // "run" | "experiment" | "prompt"
	ID           string `json:"id"`                      // run_id / experiment_id / prompt name
	Name         string `json:"name"`                    // display name
	SubTitle     string `json:"subtitle,omitempty"`      // experiment name for runs, etc.
	Status       string `json:"status,omitempty"`        // run status
	URL          string `json:"url,omitempty"`           // deep-link hash fragment
	ExperimentID string `json:"experiment_id,omitempty"` // for runs
}

type globalSearchResp struct {
	Items []searchResultItem `json:"items"`
	Query string             `json:"query"`
}

// GlobalSearch handles GET /api/v1/search?q=...&kind=all|runs|experiments|prompts
//
// It performs a workspace-scoped search across runs (by name/id prefix),
// experiments (by name), and prompts (by name prefix). The workspace is
// resolved from X-Workspace header, exactly as every other workspace-scoped
// endpoint in this service. Results are capped: 4 experiments + 4 runs + 2
// prompts = 10 total.
func (h *Handler) GlobalSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := strings.ToLower(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "all"
	}

	ws := r.Header.Get("X-LiteMLflow-Workspace")
	if ws == "" {
		ws = "default"
	}

	ctx := r.Context()
	var items []searchResultItem

	// --- experiments ---
	if kind == "all" || kind == "experiments" {
		filter := ""
		if q != "" {
			filter = "name LIKE '%" + strings.ReplaceAll(q, "'", "''") + "%'"
		}
		res, err := h.Store.SearchExperiments(ctx, store.SearchOptions{
			Filter:         filter,
			MaxResults:     4,
			LifecycleStage: "active",
			WorkspaceID:    ws,
		})
		if err == nil {
			for _, e := range res.Items {
				items = append(items, searchResultItem{
					Kind: "experiment",
					ID:   strconv.FormatInt(e.ID, 10),
					Name: e.Name,
					URL:  "#/experiments/" + strconv.FormatInt(e.ID, 10),
				})
			}
		}
	}

	// --- runs ---
	if kind == "all" || kind == "runs" {
		runs, err := h.Store.SearchRunsByName(ctx, ws, q, 4)
		if err == nil {
			for _, run := range runs {
				items = append(items, searchResultItem{
					Kind:         "run",
					ID:           run.ID,
					Name:         run.Name,
					SubTitle:     "exp " + strconv.FormatInt(run.ExperimentID, 10),
					Status:       run.Status,
					URL:          "#/experiments/" + strconv.FormatInt(run.ExperimentID, 10) + "/runs/" + run.ID,
					ExperimentID: strconv.FormatInt(run.ExperimentID, 10),
				})
			}
		}
	}

	// --- prompts ---
	// Prompts don't have a list-all endpoint in the store yet; we probe
	// localStorage-known names client-side. Server returns 0 items for prompts
	// unless the kind is explicitly "prompts", in which case we fall through to
	// a best-effort attempt using known names passed in `names` query param.
	if kind == "prompts" {
		names := strings.Split(r.URL.Query().Get("names"), ",")
		ql := strings.ToLower(q)
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if ql != "" && !strings.Contains(strings.ToLower(name), ql) {
				continue
			}
			p, err := h.Store.GetLatestPrompt(ctx, name)
			if err != nil {
				continue
			}
			items = append(items, searchResultItem{
				Kind: "prompt",
				ID:   name,
				Name: p.Name,
				URL:  "#/prompts/" + name,
			})
			if len(items) >= 2 {
				break
			}
		}
	}

	// Cap total at 10.
	if len(items) > 10 {
		items = items[:10]
	}
	if items == nil {
		items = []searchResultItem{}
	}
	writeJSON(w, globalSearchResp{Items: items, Query: q})
}

// ---- health -----------------------------------------------------------------

// Healthz returns a 200 if the process is live.
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
}

// Readyz returns 200 if the database is reachable, 503 otherwise.
func (h *Handler) Readyz(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Store.SearchExperiments(r.Context(), store.SearchOptions{MaxResults: 1, LifecycleStage: "all"}); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// Version returns build information.
func (h *Handler) Version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
	})
}

// ---- traces -----------------------------------------------------------------

type spanDTO struct {
	ID            string         `json:"id,omitempty"`
	TraceID       string         `json:"trace_id,omitempty"`
	ParentID      string         `json:"parent_id,omitempty"`
	RunID         string         `json:"run_id,omitempty"`
	Name          string         `json:"name"`
	Kind          string         `json:"span_kind,omitempty"`
	StartTimeNS   int64          `json:"start_time_ns"`
	EndTimeNS     *int64         `json:"end_time_ns,omitempty"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Events        []map[string]any `json:"events,omitempty"`
	StatusCode    string         `json:"status_code,omitempty"`
	StatusMessage string         `json:"status_message,omitempty"`
}

type ingestTracesReq struct {
	TraceID string    `json:"trace_id,omitempty"`
	Spans   []spanDTO `json:"spans"`
}

// IngestTraces handles POST /api/v1/traces.
func (h *Handler) IngestTraces(w http.ResponseWriter, r *http.Request) {
	var req ingestTracesReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if len(req.Spans) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "spans is required")
		return
	}
	if req.TraceID == "" {
		req.TraceID = model.NewTraceID()
	}
	spans := make([]model.Span, 0, len(req.Spans))
	for _, s := range req.Spans {
		traceID := s.TraceID
		if traceID == "" {
			traceID = req.TraceID
		}
		attrJSON, _ := jsonOrEmpty(s.Attributes)
		evJSON, _ := jsonOrEmpty(s.Events)
		spans = append(spans, model.Span{
			ID:             s.ID,
			TraceID:        traceID,
			ParentID:       s.ParentID,
			RunID:          s.RunID,
			Name:           s.Name,
			Kind:           s.Kind,
			StartTimeNS:    s.StartTimeNS,
			EndTimeNS:      s.EndTimeNS,
			AttributesJSON: attrJSON,
			EventsJSON:     evJSON,
			StatusCode:     s.StatusCode,
			StatusMessage:  s.StatusMessage,
		})
	}
	if err := h.Store.InsertSpans(r.Context(), spans); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "trace_id": req.TraceID, "count": len(spans)})
}

// GetRunTraces handles GET /api/v1/runs/{runID}/traces.
func (h *Handler) GetRunTraces(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	spans, err := h.Store.GetSpansByRun(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]spanDTO, 0, len(spans))
	for _, s := range spans {
		out = append(out, spanToDTO(s))
	}
	writeJSON(w, map[string]any{"spans": out})
}

// GetRunData returns a unified bundle of run + metrics + params + tags + spans.
// This is what the UI uses to render a run page in one round-trip.
//
// Sub-fetch errors (metrics/params/tags/spans) are reported back to the
// client. We don't treat any of them as optional — a partially-populated
// response would be more confusing than a clear failure.
func (h *Handler) GetRunData(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	run, err := h.Store.GetRun(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	metrics, err := h.Store.GetLatestMetrics(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	params, err := h.Store.GetParams(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	tags, err := h.Store.GetTags(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	spans, err := h.Store.GetSpansByRun(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := map[string]any{
		"id":              run.ID,
		"experiment_id":   run.ExperimentID,
		"name":            run.Name,
		"status":          run.Status,
		"start_time":      run.StartTime,
		"end_time":        run.EndTime,
		"artifact_uri":    run.ArtifactURI,
		"lifecycle_stage": run.LifecycleStage,
		"kind":            run.Kind,
		"metrics":         metrics,
		"params":          params,
		"tags":            tags,
		"spans":           spansSlice(spans),
	}
	writeJSON(w, out)
}

func spansSlice(in []model.Span) []spanDTO {
	out := make([]spanDTO, 0, len(in))
	for _, s := range in {
		out = append(out, spanToDTO(s))
	}
	return out
}

func spanToDTO(s model.Span) spanDTO {
	dto := spanDTO{
		ID: s.ID, TraceID: s.TraceID, ParentID: s.ParentID, RunID: s.RunID,
		Name: s.Name, Kind: s.Kind, StartTimeNS: s.StartTimeNS, EndTimeNS: s.EndTimeNS,
		StatusCode: s.StatusCode, StatusMessage: s.StatusMessage,
	}
	if s.AttributesJSON != "" {
		var attr map[string]any
		_ = json.Unmarshal([]byte(s.AttributesJSON), &attr)
		dto.Attributes = attr
	}
	if s.EventsJSON != "" {
		var ev []map[string]any
		_ = json.Unmarshal([]byte(s.EventsJSON), &ev)
		dto.Events = ev
	}
	return dto
}

// IngestOTLP accepts OpenTelemetry trace data in OTLP/JSON format.
//
// We support the resourceSpans → scopeSpans → spans hierarchy and map
// OTel attributes to our schema. v0.1 is a minimal implementation: gRPC OTLP
// and the full OTel attribute-value variants land in v0.2.
func (h *Handler) IngestOTLP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []otlpKV `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID           string   `json:"traceId"`
					SpanID            string   `json:"spanId"`
					ParentSpanID      string   `json:"parentSpanId,omitempty"`
					Name              string   `json:"name"`
					Kind              int      `json:"kind"`
					StartTimeUnixNano string   `json:"startTimeUnixNano"`
					EndTimeUnixNano   string   `json:"endTimeUnixNano"`
					Attributes        []otlpKV `json:"attributes"`
					Status            struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	var spans []model.Span
	for _, rs := range req.ResourceSpans {
		runID := otlpAttr(rs.Resource.Attributes, "litemlflow.run_id")
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				start := parseUnixNano(sp.StartTimeUnixNano)
				end := parseUnixNanoPtr(sp.EndTimeUnixNano)
				attrMap := otlpAttrMap(sp.Attributes)
				attrJSON, _ := jsonOrEmpty(attrMap)
				spans = append(spans, model.Span{
					ID:             sp.SpanID,
					TraceID:        sp.TraceID,
					ParentID:       sp.ParentSpanID,
					RunID:          runID,
					Name:           sp.Name,
					Kind:           otlpKindToString(sp.Kind),
					StartTimeNS:    start,
					EndTimeNS:      end,
					AttributesJSON: attrJSON,
					StatusCode:     otlpStatusToString(sp.Status.Code),
					StatusMessage:  sp.Status.Message,
				})
			}
		}
	}
	if err := h.Store.InsertSpans(r.Context(), spans); err != nil {
		writeStoreErr(w, err)
		return
	}
	// OTLP partialSuccess shape.
	writeJSON(w, map[string]any{"partialSuccess": map[string]int{"rejectedSpans": 0}})
}

type otlpKV struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string  `json:"stringValue,omitempty"`
		IntValue    string  `json:"intValue,omitempty"`
		DoubleValue float64 `json:"doubleValue,omitempty"`
		BoolValue   *bool   `json:"boolValue,omitempty"`
	} `json:"value"`
}

func otlpAttrMap(in []otlpKV) map[string]any {
	out := make(map[string]any, len(in))
	for _, kv := range in {
		switch {
		case kv.Value.StringValue != "":
			out[kv.Key] = kv.Value.StringValue
		case kv.Value.IntValue != "":
			n, _ := strconv.ParseInt(kv.Value.IntValue, 10, 64)
			out[kv.Key] = n
		case kv.Value.BoolValue != nil:
			out[kv.Key] = *kv.Value.BoolValue
		default:
			out[kv.Key] = kv.Value.DoubleValue
		}
	}
	return out
}

func otlpAttr(in []otlpKV, key string) string {
	for _, kv := range in {
		if kv.Key == key && kv.Value.StringValue != "" {
			return kv.Value.StringValue
		}
	}
	return ""
}

func otlpKindToString(k int) string {
	// 1=INTERNAL, 2=SERVER, 3=CLIENT, 4=PRODUCER, 5=CONSUMER
	switch k {
	case 1:
		return "INTERNAL"
	case 2:
		return "SERVER"
	case 3:
		return "CLIENT"
	case 4:
		return "PRODUCER"
	case 5:
		return "CONSUMER"
	default:
		return ""
	}
}

func otlpStatusToString(c int) string {
	switch c {
	case 1:
		return "OK"
	case 2:
		return "ERROR"
	default:
		return "UNSET"
	}
}

func parseUnixNano(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func parseUnixNanoPtr(s string) *int64 {
	if s == "" || s == "0" {
		return nil
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return &n
}

// ---- prompts ----------------------------------------------------------------

type createPromptReq struct {
	Name        string `json:"name"`
	Content     string `json:"content"`
	Description string `json:"description,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
}

// ListPrompts handles GET /api/v1/prompts (latest version per name).
func (h *Handler) ListPrompts(w http.ResponseWriter, r *http.Request) {
	prompts, err := h.Store.ListPrompts(r.Context())
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if prompts == nil {
		prompts = []*model.Prompt{}
	}
	writeJSON(w, map[string]any{"prompts": prompts})
}

// CreatePrompt handles POST /api/v1/prompts.
func (h *Handler) CreatePrompt(w http.ResponseWriter, r *http.Request) {
	var req createPromptReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Content == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and content are required")
		return
	}
	p := &model.Prompt{
		Name: req.Name, Content: req.Content,
		Description: req.Description, CreatedBy: req.CreatedBy,
	}
	if _, err := h.Store.CreatePrompt(r.Context(), p); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, p)
}

// GetLatestPrompt handles GET /api/v1/prompts/{name}.
func (h *Handler) GetLatestPrompt(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := h.Store.GetLatestPrompt(r.Context(), name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, p)
}

// ListPromptVersions handles GET /api/v1/prompts/{name}/versions.
func (h *Handler) ListPromptVersions(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	versions, err := h.Store.ListPromptVersions(r.Context(), name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"versions": versions})
}

// GetPromptVersion handles GET /api/v1/prompts/{name}/versions/{version}.
func (h *Handler) GetPromptVersion(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be a positive integer")
		return
	}
	p, err := h.Store.GetPromptVersion(r.Context(), name, v)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, p)
}

type setPromptAliasReq struct {
	Alias   string `json:"alias"`
	Version int64  `json:"version"`
}

// SetPromptAlias handles POST /api/v1/prompts/{name}/aliases.
func (h *Handler) SetPromptAlias(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req setPromptAliasReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := h.Store.SetPromptAlias(r.Context(), name, req.Alias, req.Version); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// GetPromptByAlias handles GET /api/v1/prompts/{name}/aliases/{alias}.
func (h *Handler) GetPromptByAlias(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	alias := chi.URLParam(r, "alias")
	p, err := h.Store.GetPromptByAlias(r.Context(), name, alias)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, p)
}

// ---- evals ------------------------------------------------------------------

type createEvalReq struct {
	RunID        string         `json:"run_id"`
	TargetRunIDs []string       `json:"target_run_ids"`
	DatasetRef   string         `json:"dataset_ref,omitempty"`
	Score        *float64       `json:"score,omitempty"`
	Metrics      map[string]any `json:"metrics,omitempty"`
}

// CreateEval handles POST /api/v1/evals.
func (h *Handler) CreateEval(w http.ResponseWriter, r *http.Request) {
	var req createEvalReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	mJSON, _ := jsonOrEmpty(req.Metrics)
	e := &model.Eval{
		RunID:        req.RunID,
		TargetRunIDs: req.TargetRunIDs,
		DatasetRef:   req.DatasetRef,
		Score:        req.Score,
		MetricsJSON:  mJSON,
	}
	if err := h.Store.CreateEval(r.Context(), e); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, e)
}

// GetEval handles GET /api/v1/evals/{runID}.
func (h *Handler) GetEval(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	e, err := h.Store.GetEval(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, e)
}

// ---- auth -------------------------------------------------------------------

// Whoami returns information about the calling user.
//
// AUTH-OIDC: now also reports auth_method from the session cookie (when
// present). We read the user from X-LiteMLflow-User set by authMiddleware,
// keeping the native package free of a dependency on server's ctxKey types.
// safeReturnTo validates an OIDC return_to query parameter against open
// redirect. Reject anything that isn't a single-leading-slash absolute path,
// and explicitly reject protocol-relative ("//evil.com/...") URLs that
// browsers treat as cross-origin. Empty / unsafe values fall back to the UI
// root. We also clamp the length to keep the redirect header small.
func safeReturnTo(s string) string {
	const fallback = "/ui/"
	if s == "" {
		return fallback
	}
	if len(s) > 2048 {
		return fallback
	}
	// Must start with a single "/" (absolute path) — never "//" (host) or
	// scheme-prefixed ("http:", "javascript:", etc.).
	if !strings.HasPrefix(s, "/") {
		return fallback
	}
	if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") {
		return fallback
	}
	// Reject control characters that could smuggle headers / break the
	// Location header (\r, \n, NUL).
	for _, r := range s {
		if r == '\r' || r == '\n' || r == 0x00 {
			return fallback
		}
	}
	return s
}

func (h *Handler) Whoami(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-LiteMLflow-User")
	if user == "" {
		user = "anonymous"
	}
	authMethod := r.Header.Get("X-LiteMLflow-Auth-Method")
	if authMethod == "" {
		authMethod = "none"
	}
	writeJSON(w, map[string]string{
		"user":        user,
		"auth_method": authMethod,
	})
}

// loginReq is the body of POST /api/v1/auth/login.
type loginReq struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// Login handles POST /api/v1/auth/login.
//
// When auth=basic it validates user+pass and mints a session cookie.
// When auth=oidc it returns 400 (use the /oidc/start flow).
// When auth=none it still mints a cookie for "anonymous" — useful for
// testing / scenarios where the UI wants to persist identity.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if h.SessionStore == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "session store not configured")
		return
	}

	switch h.Cfg.Auth {
	case "oidc":
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"auth=oidc: use GET /api/v1/auth/oidc/start to log in")
		return
	case "basic":
		var req loginReq
		if err := decodeJSON(r, &req); err != nil {
			writeBadRequest(w, err)
			return
		}
		if !verifyBasicCreds(h.Cfg, req.User, req.Pass) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid credentials")
			return
		}
		sess, err := mintSession(r.Context(), h.SessionStore, req.User, req.User, "", "basic", h.Cfg.SessionTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
		auth.SetSessionCookieAuto(w, r, sess.ID, time.UnixMilli(sess.ExpiresAt))
		writeJSON(w, map[string]any{"ok": true, "session_expires_at": sess.ExpiresAt})
	case "none":
		// In "none" mode we still support sessions so the UI can carry identity.
		sess, err := mintSession(r.Context(), h.SessionStore, "anonymous", "anonymous", "", "none", h.Cfg.SessionTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
			return
		}
		auth.SetSessionCookieAuto(w, r, sess.ID, time.UnixMilli(sess.ExpiresAt))
		writeJSON(w, map[string]any{"ok": true, "session_expires_at": sess.ExpiresAt})
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unknown auth mode")
	}
}

// Logout handles POST /api/v1/auth/logout. Always returns 200.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	sessID, err := auth.GetSessionID(r)
	if err == nil && h.SessionStore != nil {
		// Best-effort delete; ignore ErrNotFound.
		_ = h.SessionStore.DeleteSession(r.Context(), sessID)
	}
	auth.ClearSessionCookie(w)
	writeJSON(w, map[string]bool{"ok": true})
}

// OIDCStart handles GET /api/v1/auth/oidc/start.
// It generates a PKCE verifier + anti-CSRF state + nonce, stashes them in a
// short-lived cookie, and redirects the browser to the IdP.
func (h *Handler) OIDCStart(w http.ResponseWriter, r *http.Request) {
	if h.OIDCProvider == nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"OIDC is not configured; set auth=oidc and provide oidc-issuer/client-id")
		return
	}

	state, err := auth.NewOIDCState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate state")
		return
	}
	verifier, err := auth.NewPKCEVerifier()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate PKCE verifier")
		return
	}
	nonce, err := auth.NewPKCENonce()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate nonce")
		return
	}

	returnTo := r.URL.Query().Get("return_to")
	pkceState := auth.PKCEState{State: state, CodeVerifier: verifier, Nonce: nonce, ReturnTo: returnTo}
	if err := auth.SetOIDCStateCookieAuto(w, r, pkceState); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to set state cookie")
		return
	}

	authURL, err := h.OIDCProvider.BeginPKCE(r.Context(), state, verifier, nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "OIDC discovery failed: "+err.Error())
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// OIDCCallback handles GET /api/v1/auth/oidc/callback.
// It validates the CSRF state, exchanges the code for tokens, verifies the ID
// token, mints a session cookie, and redirects the user back to the UI.
func (h *Handler) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if h.OIDCProvider == nil || h.SessionStore == nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "OIDC not configured")
		return
	}

	// Validate error from IdP first.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		writeError(w, http.StatusBadRequest, "OIDC_ERROR", errParam+": "+desc)
		return
	}

	// Read PKCE state cookie.
	pkceState, err := auth.GetOIDCState(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "missing or invalid OIDC state cookie")
		return
	}

	// Validate anti-CSRF state.
	incomingState := r.URL.Query().Get("state")
	if incomingState != pkceState.State {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "CSRF state mismatch")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "missing code parameter")
		return
	}

	_, claims, err := h.OIDCProvider.Exchange(r.Context(), code, pkceState.CodeVerifier, pkceState.Nonce)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OIDC_ERROR", "token exchange failed: "+err.Error())
		return
	}

	// Extract identity from claims.
	sub, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)
	userID := sub
	if userID == "" {
		userID = email
	}

	sess, err := mintSession(r.Context(), h.SessionStore, userID, email, name, "oidc", h.Cfg.SessionTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	// Clear the PKCE state cookie — it's single-use.
	auth.ClearOIDCStateCookie(w)
	auth.SetSessionCookieAuto(w, r, sess.ID, time.UnixMilli(sess.ExpiresAt))

	returnTo := safeReturnTo(pkceState.ReturnTo)
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// mintSession creates a new session in the store and returns it.
func mintSession(ctx context.Context, ss SessionStore, userID, email, name, method string, ttl time.Duration) (*model.Session, error) {
	id, err := auth.NewSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	sess := &model.Session{
		ID:         id,
		UserID:     userID,
		UserEmail:  email,
		UserName:   name,
		AuthMethod: method,
		CreatedAt:  now,
		ExpiresAt:  now + ttl.Milliseconds(),
		LastSeen:   now,
	}
	if err := ss.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// verifyBasicCreds validates user/pass against the config.
//
// AUTH-OIDC: this duplicates the logic in server/middleware.go by design —
// keeping the native API package independent of internal/server avoids an
// import cycle. Both use crypto/sha256 + subtle compare; the canonical
// implementation is the one in middleware.go.
func verifyBasicCreds(cfg config.Config, user, pass string) bool {
	return verifyPassHash(cfg.BasicUser, cfg.BasicPassHash, user, pass)
}

// verifyPassHash checks that user==wantUser and SHA-256(pass)==hex(wantHash).
// Uses constant-time comparison to resist timing side-channels.
func verifyPassHash(wantUser, wantHashHex, user, pass string) bool {
	if subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 {
		return false
	}
	got := sha256.Sum256([]byte(pass))
	want, err := hex.DecodeString(wantHashHex)
	if err != nil || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// ---- shared helpers --------------------------------------------------------

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeBadRequest(w http.ResponseWriter, err error) {
	writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "RESOURCE_DOES_NOT_EXIST", err.Error())
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusBadRequest, "RESOURCE_ALREADY_EXISTS", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "RESOURCE_CONFLICT", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error_code": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonOrEmpty(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" || string(b) == "{}" || string(b) == "[]" {
		return "", nil
	}
	return string(b), nil
}

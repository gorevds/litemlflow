// Package native implements the LiteMLflow-native REST API.
//
// This API exposes capabilities outside MLflow's protocol: traces, prompts,
// evals, OTLP ingest. Versioned independently of MLflow compat.
package native

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
	"github.com/litemlflow/litemlflow/pkg/version"
)

// Handler bundles dependencies for the native API.
type Handler struct {
	Store store.Store
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
	r.Post("/api/v1/prompts", h.CreatePrompt)
	r.Get("/api/v1/prompts/{name}", h.GetLatestPrompt)
	r.Get("/api/v1/prompts/{name}/versions", h.ListPromptVersions)
	r.Get("/api/v1/prompts/{name}/versions/{version}", h.GetPromptVersion)
	r.Post("/api/v1/prompts/{name}/aliases", h.SetPromptAlias)
	r.Get("/api/v1/prompts/{name}/aliases/{alias}", h.GetPromptByAlias)

	// Evals
	r.Post("/api/v1/evals", h.CreateEval)
	r.Get("/api/v1/evals/{runID}", h.GetEval)

	// Auth introspection (placeholder for v0.2 OIDC)
	r.Get("/api/v1/auth/whoami", h.Whoami)
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

// ---- whoami -----------------------------------------------------------------

// Whoami returns information about the calling user. v0.1 returns "anonymous"
// when auth is "none".
//
// We read the user from the standard X-LiteMLflow-User header that the auth
// middleware sets on every authenticated request, rather than poking into
// context with a duplicated typed key. This keeps the API package free of a
// circular dependency on internal/server's middleware key types.
func (h *Handler) Whoami(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-LiteMLflow-User")
	if user == "" {
		user = "anonymous"
	}
	writeJSON(w, map[string]string{"user": user})
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

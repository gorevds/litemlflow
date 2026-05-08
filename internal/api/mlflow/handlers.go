package mlflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// EventNotifier is the minimal interface the mlflow handler needs from
// the webhook dispatcher to fire run-status events.
type EventNotifier interface {
	Notify(ctx context.Context, event string, run *model.Run)
}

// Handler bundles dependencies for the MLflow REST API.
type Handler struct {
	Store      store.Store
	Artifacts  artifact.Store
	Dispatcher EventNotifier // nil when webhooks are disabled
}

// Mount attaches the MLflow REST API to the given router.
func (h *Handler) Mount(r chi.Router) {
	// Experiments
	r.Post("/api/2.0/mlflow/experiments/create", h.CreateExperiment)
	r.Get("/api/2.0/mlflow/experiments/get", h.GetExperiment)
	r.Get("/api/2.0/mlflow/experiments/get-by-name", h.GetExperimentByName)
	// Legacy aliases — to be removed in v2.0. Wrapped with deprecation
	// headers (RFC 8594) so clients that still call them get a heads-up.
	r.Post("/api/2.0/mlflow/experiments/list", deprecated(h.SearchExperiments, "v2.0"))
	r.Get("/api/2.0/mlflow/experiments/list", deprecated(h.SearchExperiments, "v2.0"))
	r.Post("/api/2.0/mlflow/experiments/search", h.SearchExperiments)
	r.Get("/api/2.0/mlflow/experiments/search", h.SearchExperiments)
	r.Post("/api/2.0/mlflow/experiments/delete", h.DeleteExperiment)
	r.Post("/api/2.0/mlflow/experiments/restore", h.RestoreExperiment)
	r.Post("/api/2.0/mlflow/experiments/update", h.UpdateExperiment)
	r.Post("/api/2.0/mlflow/experiments/set-experiment-tag", h.SetExperimentTag)

	// Runs
	r.Post("/api/2.0/mlflow/runs/create", h.CreateRun)
	r.Get("/api/2.0/mlflow/runs/get", h.GetRun)
	r.Post("/api/2.0/mlflow/runs/update", h.UpdateRun)
	r.Post("/api/2.0/mlflow/runs/delete", h.DeleteRun)
	r.Post("/api/2.0/mlflow/runs/restore", h.RestoreRun)
	r.Post("/api/2.0/mlflow/runs/search", h.SearchRuns)
	r.Get("/api/2.0/mlflow/runs/search", h.SearchRuns)
	r.Post("/api/2.0/mlflow/runs/log-metric", h.LogMetric)
	r.Post("/api/2.0/mlflow/runs/log-parameter", h.LogParameter)
	r.Post("/api/2.0/mlflow/runs/log-batch", h.LogBatch)
	r.Post("/api/2.0/mlflow/runs/set-tag", h.SetTag)
	r.Post("/api/2.0/mlflow/runs/delete-tag", h.DeleteTag)
	r.Post("/api/2.0/mlflow/runs/log-inputs", h.LogInputs)
	r.Get("/api/2.0/mlflow/metrics/get-history", h.GetMetricHistory)

	// Artifacts
	r.Get("/api/2.0/mlflow/artifacts/list", h.ListArtifacts)
	r.Mount("/api/2.0/mlflow-artifacts/artifacts", artifactsRouter(h))

	// COMPAT-REGISTRY: Model Registry endpoints added in v0.2.
	h.mountRegistry(r)
}

// currentWorkspace returns the workspace id resolved by workspaceMiddleware
// (exposed via the X-LiteMLflow-Workspace request header). Falls back to
// "default" when the middleware has not run (e.g., in unit tests).
func currentWorkspace(r *http.Request) string {
	if ws := r.Header.Get("X-LiteMLflow-Workspace"); ws != "" {
		return ws
	}
	return "default"
}

// ---- experiments ------------------------------------------------------------

type createExperimentReq struct {
	Name             string             `json:"name"`
	ArtifactLocation string             `json:"artifact_location,omitempty"`
	Tags             []experimentTagDTO `json:"tags,omitempty"`
}

type createExperimentResp struct {
	ExperimentID string `json:"experiment_id"`
}

// CreateExperiment handles POST /api/2.0/mlflow/experiments/create.
func (h *Handler) CreateExperiment(w http.ResponseWriter, r *http.Request) {
	var req createExperimentReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	tags := make([]model.KV, 0, len(req.Tags))
	for _, t := range req.Tags {
		tags = append(tags, model.KV{Key: t.Key, Value: t.Value})
	}
	// TENANCY: scope to workspace
	id, err := h.Store.CreateExperiment(r.Context(), &model.Experiment{
		Name:             req.Name,
		ArtifactLocation: req.ArtifactLocation,
		Tags:             tags,
		WorkspaceID:      currentWorkspace(r),
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, createExperimentResp{ExperimentID: strconv.FormatInt(id, 10)})
}

type getExperimentResp struct {
	Experiment experimentDTO `json:"experiment"`
}

// GetExperiment handles GET /api/2.0/mlflow/experiments/get.
func (h *Handler) GetExperiment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("experiment_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	e, err := h.Store.GetExperiment(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getExperimentResp{Experiment: experimentToDTO(e)})
}

// GetExperimentByName handles GET /api/2.0/mlflow/experiments/get-by-name.
func (h *Handler) GetExperimentByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("experiment_name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_name is required")
		return
	}
	// TENANCY: scope to workspace
	e, err := h.Store.GetExperimentByNameInWorkspace(r.Context(), currentWorkspace(r), name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getExperimentResp{Experiment: experimentToDTO(e)})
}

type searchExperimentsReq struct {
	MaxResults int    `json:"max_results,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
	Filter     string `json:"filter,omitempty"`
	ViewType   string `json:"view_type,omitempty"` // ACTIVE_ONLY/DELETED_ONLY/ALL
}

type searchExperimentsResp struct {
	Experiments   []experimentDTO `json:"experiments"`
	NextPageToken string          `json:"next_page_token,omitempty"`
}

// SearchExperiments handles POST /api/2.0/mlflow/experiments/search.
func (h *Handler) SearchExperiments(w http.ResponseWriter, r *http.Request) {
	var req searchExperimentsReq
	_ = decodeJSON(r, &req)
	// Allow query-string variant too.
	if req.MaxResults == 0 {
		if v := r.URL.Query().Get("max_results"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.MaxResults = n
			}
		}
	}
	if req.Filter == "" {
		req.Filter = r.URL.Query().Get("filter")
	}
	if req.ViewType == "" {
		req.ViewType = r.URL.Query().Get("view_type")
	}
	if req.PageToken == "" {
		req.PageToken = r.URL.Query().Get("page_token")
	}
	stage := mapViewType(req.ViewType)
	// TENANCY: scope to workspace
	res, err := h.Store.SearchExperiments(r.Context(), store.SearchOptions{
		MaxResults:     req.MaxResults,
		PageToken:      req.PageToken,
		Filter:         req.Filter,
		LifecycleStage: stage,
		WorkspaceID:    currentWorkspace(r),
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]experimentDTO, 0, len(res.Items))
	for _, e := range res.Items {
		out = append(out, experimentToDTO(e))
	}
	writeJSON(w, searchExperimentsResp{Experiments: out, NextPageToken: res.NextPageToken})
}

func mapViewType(v string) string {
	switch strings.ToUpper(v) {
	case "DELETED_ONLY":
		return model.LifecycleDeleted
	case "ALL":
		return "all"
	case "ACTIVE_ONLY", "":
		return model.LifecycleActive
	}
	return model.LifecycleActive
}

type idReq struct {
	ExperimentID string `json:"experiment_id"`
}

// DeleteExperiment handles POST .../experiments/delete.
func (h *Handler) DeleteExperiment(w http.ResponseWriter, r *http.Request) {
	var req idReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	id, err := strconv.ParseInt(req.ExperimentID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	if err := h.Store.SetExperimentLifecycle(r.Context(), id, model.LifecycleDeleted); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// RestoreExperiment handles POST .../experiments/restore.
func (h *Handler) RestoreExperiment(w http.ResponseWriter, r *http.Request) {
	var req idReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	id, err := strconv.ParseInt(req.ExperimentID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	if err := h.Store.SetExperimentLifecycle(r.Context(), id, model.LifecycleActive); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type updateExperimentReq struct {
	ExperimentID string `json:"experiment_id"`
	NewName      string `json:"new_name"`
}

// UpdateExperiment handles POST .../experiments/update (rename).
func (h *Handler) UpdateExperiment(w http.ResponseWriter, r *http.Request) {
	var req updateExperimentReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	id, err := strconv.ParseInt(req.ExperimentID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	if req.NewName == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "new_name is required")
		return
	}
	if err := h.Store.UpdateExperiment(r.Context(), id, &req.NewName); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type setExperimentTagReq struct {
	ExperimentID string `json:"experiment_id"`
	Key          string `json:"key"`
	Value        string `json:"value"`
}

// SetExperimentTag handles POST .../experiments/set-experiment-tag.
func (h *Handler) SetExperimentTag(w http.ResponseWriter, r *http.Request) {
	var req setExperimentTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	id, err := strconv.ParseInt(req.ExperimentID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	if err := h.Store.SetExperimentTag(r.Context(), id, req.Key, req.Value); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// ---- runs -------------------------------------------------------------------

type createRunReq struct {
	ExperimentID string    `json:"experiment_id"`
	UserID       string    `json:"user_id,omitempty"`
	StartTime    int64     `json:"start_time,omitempty"`
	Tags         []tagDTO  `json:"tags,omitempty"`
	RunName      string    `json:"run_name,omitempty"`
}

type runResp struct {
	Run runDTO `json:"run"`
}

// CreateRun handles POST /api/2.0/mlflow/runs/create.
func (h *Handler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var req createRunReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	expID, err := strconv.ParseInt(req.ExperimentID, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "experiment_id is required")
		return
	}
	run := &model.Run{
		ExperimentID: expID,
		UserID:       req.UserID,
		StartTime:    req.StartTime,
		Name:         req.RunName,
	}
	// Extract parent_run_id from tags if the MLflow client set it.
	for _, t := range req.Tags {
		if t.Key == "mlflow.parentRunId" {
			run.ParentRunID = t.Value
			break
		}
	}
	if err := h.Store.CreateRun(r.Context(), run); err != nil {
		writeStoreErr(w, err)
		return
	}
	for _, t := range req.Tags {
		_ = h.Store.SetTag(r.Context(), run.ID, model.KV{Key: t.Key, Value: t.Value})
	}
	// Fire webhook for run creation.
	if h.Dispatcher != nil {
		h.Dispatcher.Notify(r.Context(), "run_started", run)
	}
	writeJSON(w, runResp{Run: runDTO{
		Info: runInfoToDTO(run),
		Data: runDataDTO{Tags: req.Tags},
	}})
}

// GetRun handles GET /api/2.0/mlflow/runs/get.
func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("run_id")
	if id == "" {
		id = r.URL.Query().Get("run_uuid")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	run, err := h.Store.GetRun(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	data, err := h.collectRunData(r, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	inputs, err := h.collectRunInputs(r, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, runResp{Run: runDTO{Info: runInfoToDTO(run), Data: data, Inputs: inputs}})
}

// collectRunData loads metrics, params, and tags for a run.
func (h *Handler) collectRunData(r *http.Request, runID string) (runDataDTO, error) {
	metrics, err := h.Store.GetLatestMetrics(r.Context(), runID)
	if err != nil {
		return runDataDTO{}, err
	}
	params, err := h.Store.GetParams(r.Context(), runID)
	if err != nil {
		return runDataDTO{}, err
	}
	tags, err := h.Store.GetTags(r.Context(), runID)
	if err != nil {
		return runDataDTO{}, err
	}
	return runDataDTO{
		Metrics: metricsToDTO(metrics),
		Params:  paramsToDTO(params),
		Tags:    tagsToDTO(tags),
	}, nil
}

// collectRunInputs loads dataset inputs for a run.
func (h *Handler) collectRunInputs(r *http.Request, runID string) (*runInputsDTO, error) {
	datasets, err := h.Store.GetRunDatasets(r.Context(), runID)
	if err != nil {
		return nil, err
	}
	return datasetInputsToDTO(datasets), nil
}

type updateRunReq struct {
	RunID   string `json:"run_id"`
	RunUUID string `json:"run_uuid,omitempty"`
	Status  string `json:"status,omitempty"`
	EndTime int64  `json:"end_time,omitempty"`
	RunName string `json:"run_name,omitempty"`
}

// UpdateRun handles POST .../runs/update.
func (h *Handler) UpdateRun(w http.ResponseWriter, r *http.Request) {
	var req updateRunReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	id := req.RunID
	if id == "" {
		id = req.RunUUID
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	var status *string
	if req.Status != "" {
		s := req.Status
		status = &s
	}
	var end *int64
	if req.EndTime != 0 {
		end = &req.EndTime
	}
	var name *string
	if req.RunName != "" {
		name = &req.RunName
	}
	if err := h.Store.UpdateRun(r.Context(), id, status, end, name); err != nil {
		writeStoreErr(w, err)
		return
	}
	run, err := h.Store.GetRun(r.Context(), id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Fire webhook when status transitions to a terminal state.
	if h.Dispatcher != nil && status != nil {
		if event := statusToWebhookEvent(*status); event != "" {
			h.Dispatcher.Notify(r.Context(), event, run)
		}
	}
	writeJSON(w, struct {
		RunInfo runInfoDTO `json:"run_info"`
	}{RunInfo: runInfoToDTO(run)})
}

// DeleteRun handles POST .../runs/delete.
func (h *Handler) DeleteRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := h.Store.SetRunLifecycle(r.Context(), req.RunID, model.LifecycleDeleted); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// RestoreRun handles POST .../runs/restore.
func (h *Handler) RestoreRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := h.Store.SetRunLifecycle(r.Context(), req.RunID, model.LifecycleActive); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type searchRunsReq struct {
	ExperimentIDs []string `json:"experiment_ids"`
	Filter        string   `json:"filter"`
	ViewType      string   `json:"run_view_type"`
	MaxResults    int      `json:"max_results"`
	OrderBy       []string `json:"order_by"`
	PageToken     string   `json:"page_token"`
}

type searchRunsResp struct {
	Runs          []runDTO `json:"runs"`
	NextPageToken string   `json:"next_page_token,omitempty"`
}

// SearchRuns handles POST .../runs/search.
func (h *Handler) SearchRuns(w http.ResponseWriter, r *http.Request) {
	var req searchRunsReq
	_ = decodeJSON(r, &req)
	expIDs := make([]int64, 0, len(req.ExperimentIDs))
	for _, s := range req.ExperimentIDs {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "invalid experiment_id "+s)
			return
		}
		expIDs = append(expIDs, n)
	}
	res, err := h.Store.SearchRuns(r.Context(), store.SearchOptions{
		ExperimentIDs:  expIDs,
		Filter:         req.Filter,
		LifecycleStage: mapViewType(req.ViewType),
		MaxResults:     req.MaxResults,
		OrderBy:        req.OrderBy,
		PageToken:      req.PageToken,
	})
	if err != nil {
		// Filter parse failures are client errors, not 500s. Caught by
		// TestMlflowSearchRuns_BadFilter.
		if strings.Contains(err.Error(), "unable to parse predicate") ||
			strings.Contains(err.Error(), "invalid order by") ||
			strings.Contains(err.Error(), "invalid filter") {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
			return
		}
		writeStoreErr(w, err)
		return
	}
	runs := make([]runDTO, 0, len(res.Items))
	for _, run := range res.Items {
		data, err := h.collectRunData(r, run.ID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		inputs, err := h.collectRunInputs(r, run.ID)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		runs = append(runs, runDTO{Info: runInfoToDTO(run), Data: data, Inputs: inputs})
	}
	writeJSON(w, searchRunsResp{Runs: runs, NextPageToken: res.NextPageToken})
}

type logMetricReq struct {
	RunID     string  `json:"run_id"`
	RunUUID   string  `json:"run_uuid,omitempty"`
	Key       string  `json:"key"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	Step      int64   `json:"step,omitempty"`
}

// LogMetric handles POST .../runs/log-metric.
func (h *Handler) LogMetric(w http.ResponseWriter, r *http.Request) {
	var req logMetricReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	runID := req.RunID
	if runID == "" {
		runID = req.RunUUID
	}
	if runID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	if err := h.Store.LogMetric(r.Context(), runID, model.Metric{
		Key: req.Key, Value: req.Value, Timestamp: req.Timestamp, Step: req.Step,
	}); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type logParameterReq struct {
	RunID   string `json:"run_id"`
	RunUUID string `json:"run_uuid,omitempty"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// LogParameter handles POST .../runs/log-parameter.
func (h *Handler) LogParameter(w http.ResponseWriter, r *http.Request) {
	var req logParameterReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	runID := req.RunID
	if runID == "" {
		runID = req.RunUUID
	}
	if err := h.Store.LogParam(r.Context(), runID, model.Param{Key: req.Key, Value: req.Value}); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type logBatchReq struct {
	RunID   string      `json:"run_id"`
	Metrics []metricDTO `json:"metrics,omitempty"`
	Params  []paramDTO  `json:"params,omitempty"`
	Tags    []tagDTO    `json:"tags,omitempty"`
}

// LogBatch handles POST .../runs/log-batch (canonical batch entry point).
func (h *Handler) LogBatch(w http.ResponseWriter, r *http.Request) {
	var req logBatchReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	// Caps matching MLflow.
	if len(req.Metrics) > 1000 || len(req.Params) > 100 || len(req.Tags) > 100 {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"batch caps exceeded (metrics<=1000, params<=100, tags<=100)")
		return
	}
	metrics := make([]model.Metric, 0, len(req.Metrics))
	for _, m := range req.Metrics {
		metrics = append(metrics, model.Metric{Key: m.Key, Value: m.Value, Timestamp: m.Timestamp, Step: m.Step})
	}
	if err := h.Store.LogMetrics(r.Context(), req.RunID, metrics); err != nil {
		writeStoreErr(w, err)
		return
	}
	for _, p := range req.Params {
		if err := h.Store.LogParam(r.Context(), req.RunID, model.Param{Key: p.Key, Value: p.Value}); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	tags := make([]model.KV, 0, len(req.Tags))
	for _, t := range req.Tags {
		tags = append(tags, model.KV{Key: t.Key, Value: t.Value})
	}
	if err := h.Store.SetTags(r.Context(), req.RunID, tags); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type setTagReq struct {
	RunID   string `json:"run_id"`
	RunUUID string `json:"run_uuid,omitempty"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// SetTag handles POST .../runs/set-tag.
func (h *Handler) SetTag(w http.ResponseWriter, r *http.Request) {
	var req setTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	runID := req.RunID
	if runID == "" {
		runID = req.RunUUID
	}
	if err := h.Store.SetTag(r.Context(), runID, model.KV{Key: req.Key, Value: req.Value}); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type deleteTagReq struct {
	RunID string `json:"run_id"`
	Key   string `json:"key"`
}

// DeleteTag handles POST .../runs/delete-tag.
func (h *Handler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	var req deleteTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.RunID == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id and key are required")
		return
	}
	if err := h.Store.DeleteTag(r.Context(), req.RunID, req.Key); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// ---- log-inputs (datasets) --------------------------------------------------

type logInputsDatasetReq struct {
	Dataset datasetDTO `json:"dataset"`
	Tags    []tagDTO   `json:"tags,omitempty"`
}

type logInputsReq struct {
	RunID    string                `json:"run_id"`
	Datasets []logInputsDatasetReq `json:"datasets,omitempty"`
	// Models is accepted for wire compatibility but ignored (model registry out of scope).
	Models []json.RawMessage `json:"models,omitempty"`
}

// LogInputs handles POST /api/2.0/mlflow/runs/log-inputs.
func (h *Handler) LogInputs(w http.ResponseWriter, r *http.Request) {
	var req logInputsReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	inputs := make([]model.DatasetInput, 0, len(req.Datasets))
	for _, d := range req.Datasets {
		tags := make([]model.KV, 0, len(d.Tags))
		for _, t := range d.Tags {
			tags = append(tags, model.KV{Key: t.Key, Value: t.Value})
		}
		inputs = append(inputs, model.DatasetInput{
			Dataset: model.Dataset{
				Name:       d.Dataset.Name,
				Digest:     d.Dataset.Digest,
				SourceType: d.Dataset.SourceType,
				Source:     d.Dataset.Source,
				Schema:     d.Dataset.Schema,
				Profile:    d.Dataset.Profile,
			},
			Tags: tags,
		})
	}
	if err := h.Store.LogInputs(r.Context(), req.RunID, inputs); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type getMetricHistoryResp struct {
	Metrics          []metricDTO `json:"metrics"`
	NextPageToken    string      `json:"next_page_token,omitempty"`
	DownsampledFrom  *int64      `json:"downsampled_from,omitempty"`
}

// GetMetricHistory handles GET .../metrics/get-history.
// Supports optional ?max_results=N and ?page_token=... query params for
// paginated access, and ?downsample=N for server-side LTTB downsampling.
//
// When ?downsample=N is supplied, the server reduces the full history to at
// most N representative points using the LTTB algorithm and includes
// "downsampled_from" in the response with the total raw point count.
// The paginated path (?max_results / ?page_token) is unchanged.
func (h *Handler) GetMetricHistory(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		runID = r.URL.Query().Get("run_uuid")
	}
	key := r.URL.Query().Get("metric_key")
	if runID == "" || key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id and metric_key are required")
		return
	}

	// ?downsample=N — LTTB path; mutually exclusive with pagination.
	if ds := r.URL.Query().Get("downsample"); ds != "" {
		target, err := strconv.Atoi(ds)
		if err != nil || target < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "downsample must be a non-negative integer")
			return
		}
		pts, total, err := h.Store.GetMetricHistoryDownsampled(r.Context(), runID, key, target)
		if err != nil {
			writeStoreErr(w, err)
			return
		}
		resp := getMetricHistoryResp{Metrics: metricsToDTO(pts)}
		resp.DownsampledFrom = &total
		writeJSON(w, resp)
		return
	}

	// Standard paginated path.
	var maxResults int
	if v := r.URL.Query().Get("max_results"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "max_results must be a non-negative integer")
			return
		}
		maxResults = n
	}
	pageToken := r.URL.Query().Get("page_token")
	hist, nextToken, err := h.Store.GetMetricHistory(r.Context(), runID, key, store.MetricHistoryOptions{
		MaxResults: maxResults,
		PageToken:  pageToken,
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getMetricHistoryResp{Metrics: metricsToDTO(hist), NextPageToken: nextToken})
}

// ---- artifacts --------------------------------------------------------------

type artifactFile struct {
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	FileSize int64  `json:"file_size,omitempty"`
}

type listArtifactsResp struct {
	RootURI string         `json:"root_uri"`
	Files   []artifactFile `json:"files"`
}

// ListArtifacts handles GET /api/2.0/mlflow/artifacts/list.
func (h *Handler) ListArtifacts(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run_id")
	if runID == "" {
		runID = r.URL.Query().Get("run_uuid")
	}
	dir := r.URL.Query().Get("path")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "run_id is required")
		return
	}
	run, err := h.Store.GetRun(r.Context(), runID)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	entries, err := h.Artifacts.List(runID, dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	files := make([]artifactFile, 0, len(entries))
	for _, e := range entries {
		f := artifactFile{Path: e.Path, IsDir: e.IsDir}
		if !e.IsDir {
			f.FileSize = e.Size
		}
		files = append(files, f)
	}
	writeJSON(w, listArtifactsResp{RootURI: run.ArtifactURI, Files: files})
}

// artifactsRouter handles upload/download/delete under
// /api/2.0/mlflow-artifacts/artifacts/<run_id>/<path...>.
//
// It also handles MLflow's proxy-list shape:
//
//	GET /api/2.0/mlflow-artifacts/artifacts?path=<run_id>[/sub/dir]
//
// which lists immediate children of the given path. Used by the
// MlflowArtifactsRepository client when artifact_uri is mlflow-artifacts:/...
func artifactsRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	handle := func(w http.ResponseWriter, req *http.Request) {
		// Proxy-list shape: GET with no path component, ?path= carries it.
		if req.Method == http.MethodGet && strings.TrimPrefix(chi.URLParam(req, "*"), "/") == "" {
			runID, rel := splitQueryPath(req.URL.Query().Get("path"))
			if runID == "" {
				writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "path is required")
				return
			}
			if _, err := h.Store.GetRun(req.Context(), runID); err != nil {
				writeStoreErr(w, err)
				return
			}
			entries, err := h.Artifacts.List(runID, rel)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
				return
			}
			out := make([]artifactFile, 0, len(entries))
			for _, e := range entries {
				af := artifactFile{Path: e.Path, IsDir: e.IsDir}
				if !e.IsDir {
					af.FileSize = e.Size
				}
				out = append(out, af)
			}
			// MLflow's proxy list response shape is {"files": [...]}.
			writeJSON(w, struct {
				Files []artifactFile `json:"files"`
			}{Files: out})
			return
		}

		runID, rel, err := splitArtifactPath(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
			return
		}
		// Every artifact operation must reference a real run. Without this
		// check, a client could create orphaned files outside any run that
		// will never be garbage-collected.
		if _, err := h.Store.GetRun(req.Context(), runID); err != nil {
			writeStoreErr(w, err)
			return
		}
		switch req.Method {
		case http.MethodGet:
			rc, size, err := h.Artifacts.Open(runID, rel)
			if err != nil {
				if errors.Is(err, artifact.ErrNotFound) {
					writeError(w, http.StatusNotFound, "RESOURCE_DOES_NOT_EXIST", "artifact not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			// Quote-escape the filename to defeat header-injection via crafted
			// artifact names (defense in depth — paths are already validated).
			safeName := strings.ReplaceAll(path.Base(rel), `"`, `\"`)
			w.Header().Set("Content-Disposition", `attachment; filename="`+safeName+`"`)
			_, _ = io.Copy(w, rc)
		case http.MethodPut:
			if err := h.Artifacts.Upload(runID, rel, req.Body, 0); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
				return
			}
			writeJSON(w, struct{}{})
		case http.MethodDelete:
			if err := h.Artifacts.Delete(runID, rel); err != nil {
				if errors.Is(err, artifact.ErrNotFound) {
					writeError(w, http.StatusNotFound, "RESOURCE_DOES_NOT_EXIST", "artifact not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
				return
			}
			writeJSON(w, struct{}{})
		default:
			writeError(w, http.StatusMethodNotAllowed, "INVALID_PARAMETER_VALUE", "method not allowed")
		}
	}
	r.HandleFunc("/*", handle)
	return r
}

// splitArtifactPath parses /<run_id>/<rel> from chi.URLParam.
func splitArtifactPath(r *http.Request) (string, string, error) {
	rest := chi.URLParam(r, "*")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", "", errors.New("path is required")
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 1 {
		return parts[0], "", nil
	}
	return parts[0], parts[1], nil
}

// splitQueryPath splits a "?path=<run_id>[/sub/dir]" query value into the
// run id and the relative subdirectory.
func splitQueryPath(p string) (string, string) {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", ""
	}
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

// ---- shared helpers --------------------------------------------------------

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
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
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error_code": code,
		"message":    msg,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// deprecated wraps a handler with RFC 8594 deprecation headers. Clients
// that still call the legacy alias get a heads-up via:
//
//	Deprecation: true
//	Sunset: <RFC 7231 date — set to v2.0 GA>
//	Link: <docs URL>; rel="deprecation"
//
// We don't change the response body so the wire contract is unchanged
// for the client — only headers are added.
func deprecated(next http.HandlerFunc, removeAt string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		// Sunset is the RFC 7231 IMF-fixdate when the endpoint will be
		// removed. We don't have a hard date for v2.0 yet; per RFC 8594
		// it's optional. We omit Sunset and use a free-form Link header.
		w.Header().Set("Link",
			`</docs/upgrade-to-v2.md>; rel="deprecation"; type="text/markdown"`)
		w.Header().Set("X-LiteMLflow-Removed-At", removeAt)
		next.ServeHTTP(w, r)
	}
}

// statusToWebhookEvent maps a run status to the webhook event name for terminal
// transitions. Returns "" for non-terminal statuses (e.g. RUNNING, SCHEDULED).
func statusToWebhookEvent(status string) string {
	switch status {
	case model.StatusFinished:
		return "run_finished"
	case model.StatusFailed:
		return "run_failed"
	case model.StatusKilled:
		return "run_killed"
	default:
		return ""
	}
}

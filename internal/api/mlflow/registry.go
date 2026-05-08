package mlflow

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/litemlflow/litemlflow/internal/model"
)

// mountRegistry wires all Model Registry endpoints onto r.
// Called by Mount() — see the COMPAT-REGISTRY marker there.
func (h *Handler) mountRegistry(r chi.Router) {
	// Registered Models
	r.Post("/api/2.0/mlflow/registered-models/create", h.CreateRegisteredModel)
	r.Get("/api/2.0/mlflow/registered-models/get", h.GetRegisteredModel)
	r.Post("/api/2.0/mlflow/registered-models/rename", h.RenameRegisteredModel)
	r.Post("/api/2.0/mlflow/registered-models/update", h.UpdateRegisteredModel)
	r.Post("/api/2.0/mlflow/registered-models/delete", h.DeleteRegisteredModel)
	r.Post("/api/2.0/mlflow/registered-models/search", h.SearchRegisteredModels)
	r.Get("/api/2.0/mlflow/registered-models/search", h.SearchRegisteredModels)
	r.Post("/api/2.0/mlflow/registered-models/get-latest-versions", h.GetLatestModelVersions)
	r.Get("/api/2.0/mlflow/registered-models/get-latest-versions", h.GetLatestModelVersions)
	r.Post("/api/2.0/mlflow/registered-models/set-tag", h.SetRegisteredModelTag)
	r.Post("/api/2.0/mlflow/registered-models/delete-tag", h.DeleteRegisteredModelTag)
	r.Post("/api/2.0/mlflow/registered-models/alias", h.SetModelAlias)
	r.Delete("/api/2.0/mlflow/registered-models/alias", h.DeleteModelAlias)
	r.Get("/api/2.0/mlflow/registered-models/alias", h.GetModelByAlias)

	// Model Versions
	r.Post("/api/2.0/mlflow/model-versions/create", h.CreateModelVersion)
	r.Get("/api/2.0/mlflow/model-versions/get", h.GetModelVersion)
	r.Post("/api/2.0/mlflow/model-versions/update", h.UpdateModelVersion)
	r.Post("/api/2.0/mlflow/model-versions/delete", h.DeleteModelVersion)
	r.Post("/api/2.0/mlflow/model-versions/search", h.SearchModelVersions)
	r.Get("/api/2.0/mlflow/model-versions/search", h.SearchModelVersions)
	r.Get("/api/2.0/mlflow/model-versions/get-download-uri", h.GetModelVersionDownloadURI)
	r.Post("/api/2.0/mlflow/model-versions/transition-stage", h.TransitionModelStage)
	r.Post("/api/2.0/mlflow/model-versions/set-tag", h.SetModelVersionTag)
	r.Post("/api/2.0/mlflow/model-versions/delete-tag", h.DeleteModelVersionTag)
}

// ---- Registered Models -------------------------------------------------------

type createRegisteredModelReq struct {
	Name        string     `json:"name"`
	Tags        []mvTagDTO `json:"tags,omitempty"`
	Description string     `json:"description,omitempty"`
}

type createRegisteredModelResp struct {
	RegisteredModel registeredModelDTO `json:"registered_model"`
}

// CreateRegisteredModel handles POST /api/2.0/mlflow/registered-models/create.
func (h *Handler) CreateRegisteredModel(w http.ResponseWriter, r *http.Request) {
	var req createRegisteredModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	m := &model.RegisteredModel{Name: req.Name, Description: req.Description}
	if err := h.Store.CreateRegisteredModel(r.Context(), m); err != nil {
		writeStoreErr(w, err)
		return
	}
	// Apply tags.
	for _, t := range req.Tags {
		if err := h.Store.SetRegisteredModelTag(r.Context(), req.Name, t.Key, t.Value); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	got, err := h.Store.GetRegisteredModel(r.Context(), req.Name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, createRegisteredModelResp{RegisteredModel: registeredModelToDTO(got)})
}

type getRegisteredModelResp struct {
	RegisteredModel registeredModelDTO `json:"registered_model"`
}

// GetRegisteredModel handles GET /api/2.0/mlflow/registered-models/get.
func (h *Handler) GetRegisteredModel(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	m, err := h.Store.GetRegisteredModel(r.Context(), name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getRegisteredModelResp{RegisteredModel: registeredModelToDTO(m)})
}

type renameRegisteredModelReq struct {
	Name    string `json:"name"`
	NewName string `json:"new_name"`
}

// RenameRegisteredModel handles POST /api/2.0/mlflow/registered-models/rename.
func (h *Handler) RenameRegisteredModel(w http.ResponseWriter, r *http.Request) {
	var req renameRegisteredModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.NewName == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and new_name are required")
		return
	}
	m, err := h.Store.RenameRegisteredModel(r.Context(), req.Name, req.NewName)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getRegisteredModelResp{RegisteredModel: registeredModelToDTO(m)})
}

type updateRegisteredModelReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// UpdateRegisteredModel handles POST /api/2.0/mlflow/registered-models/update.
func (h *Handler) UpdateRegisteredModel(w http.ResponseWriter, r *http.Request) {
	var req updateRegisteredModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	m, err := h.Store.UpdateRegisteredModel(r.Context(), req.Name, req.Description)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getRegisteredModelResp{RegisteredModel: registeredModelToDTO(m)})
}

type deleteRegisteredModelReq struct {
	Name string `json:"name"`
}

// DeleteRegisteredModel handles POST /api/2.0/mlflow/registered-models/delete.
func (h *Handler) DeleteRegisteredModel(w http.ResponseWriter, r *http.Request) {
	var req deleteRegisteredModelReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	if err := h.Store.DeleteRegisteredModel(r.Context(), req.Name); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type searchRegisteredModelsReq struct {
	Filter     string `json:"filter,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
}

type searchRegisteredModelsResp struct {
	RegisteredModels []registeredModelDTO `json:"registered_models"`
	NextPageToken    string               `json:"next_page_token,omitempty"`
}

// SearchRegisteredModels handles POST/GET /api/2.0/mlflow/registered-models/search.
func (h *Handler) SearchRegisteredModels(w http.ResponseWriter, r *http.Request) {
	var req searchRegisteredModelsReq
	_ = decodeJSON(r, &req)
	// Accept query string fallback for GET.
	if req.Filter == "" {
		req.Filter = r.URL.Query().Get("filter")
	}
	if req.MaxResults == 0 {
		if v := r.URL.Query().Get("max_results"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.MaxResults = n
			}
		}
	}
	if req.PageToken == "" {
		req.PageToken = r.URL.Query().Get("page_token")
	}

	res, err := h.Store.SearchRegisteredModels(r.Context(), req.Filter, req.MaxResults, req.PageToken)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]registeredModelDTO, 0, len(res.Items))
	for _, m := range res.Items {
		out = append(out, registeredModelToDTO(m))
	}
	writeJSON(w, searchRegisteredModelsResp{
		RegisteredModels: out,
		NextPageToken:    res.NextPageToken,
	})
}

type getLatestVersionsReq struct {
	Name   string   `json:"name"`
	Stages []string `json:"stages,omitempty"`
}

type getLatestVersionsResp struct {
	ModelVersions []mvDTO `json:"model_versions"`
}

// GetLatestModelVersions handles POST/GET .../registered-models/get-latest-versions.
func (h *Handler) GetLatestModelVersions(w http.ResponseWriter, r *http.Request) {
	var req getLatestVersionsReq
	_ = decodeJSON(r, &req)
	if req.Name == "" {
		req.Name = r.URL.Query().Get("name")
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	versions, err := h.Store.GetLatestModelVersions(r.Context(), req.Name, req.Stages)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]mvDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, modelVersionToDTO(v))
	}
	writeJSON(w, getLatestVersionsResp{ModelVersions: out})
}

type setRegisteredModelTagReq struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// SetRegisteredModelTag handles POST .../registered-models/set-tag.
func (h *Handler) SetRegisteredModelTag(w http.ResponseWriter, r *http.Request) {
	var req setRegisteredModelTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and key are required")
		return
	}
	if err := h.Store.SetRegisteredModelTag(r.Context(), req.Name, req.Key, req.Value); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type deleteRegisteredModelTagReq struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// DeleteRegisteredModelTag handles POST .../registered-models/delete-tag.
func (h *Handler) DeleteRegisteredModelTag(w http.ResponseWriter, r *http.Request) {
	var req deleteRegisteredModelTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and key are required")
		return
	}
	if err := h.Store.DeleteRegisteredModelTag(r.Context(), req.Name, req.Key); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type setModelAliasReq struct {
	Name    string `json:"name"`
	Alias   string `json:"alias"`
	Version string `json:"version"` // MLflow sends version as string
}

// SetModelAlias handles POST .../registered-models/alias.
func (h *Handler) SetModelAlias(w http.ResponseWriter, r *http.Request) {
	var req setModelAliasReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Alias == "" || req.Version == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name, alias, and version are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	if err := h.Store.SetModelAlias(r.Context(), req.Name, req.Alias, ver); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// DeleteModelAlias handles DELETE .../registered-models/alias.
func (h *Handler) DeleteModelAlias(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	alias := r.URL.Query().Get("alias")
	if name == "" || alias == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and alias are required")
		return
	}
	if err := h.Store.DeleteModelAlias(r.Context(), name, alias); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type getModelByAliasResp struct {
	ModelVersion mvDTO `json:"model_version"`
}

// GetModelByAlias handles GET .../registered-models/alias.
func (h *Handler) GetModelByAlias(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	alias := r.URL.Query().Get("alias")
	if name == "" || alias == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and alias are required")
		return
	}
	mv, err := h.Store.GetModelByAlias(r.Context(), name, alias)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getModelByAliasResp{ModelVersion: modelVersionToDTO(mv)})
}

// ---- Model Versions ----------------------------------------------------------

type createModelVersionReq struct {
	Name        string     `json:"name"`
	Source      string     `json:"source"`
	RunID       string     `json:"run_id,omitempty"`
	Tags        []mvTagDTO `json:"tags,omitempty"`
	Description string     `json:"description,omitempty"`
}

type createModelVersionResp struct {
	ModelVersion mvDTO `json:"model_version"`
}

// CreateModelVersion handles POST /api/2.0/mlflow/model-versions/create.
func (h *Handler) CreateModelVersion(w http.ResponseWriter, r *http.Request) {
	var req createModelVersionReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "source is required")
		return
	}
	mv, err := h.Store.CreateModelVersion(r.Context(), &model.ModelVersion{
		Name:        req.Name,
		Source:      req.Source,
		RunID:       req.RunID,
		Description: req.Description,
	})
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	// Apply tags.
	for _, t := range req.Tags {
		if err := h.Store.SetModelVersionTag(r.Context(), mv.Name, mv.Version, t.Key, t.Value); err != nil {
			writeStoreErr(w, err)
			return
		}
	}
	// Reload to include tags.
	mv, err = h.Store.GetModelVersion(r.Context(), mv.Name, mv.Version)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, createModelVersionResp{ModelVersion: modelVersionToDTO(mv)})
}

type getModelVersionResp struct {
	ModelVersion mvDTO `json:"model_version"`
}

// GetModelVersion handles GET /api/2.0/mlflow/model-versions/get.
func (h *Handler) GetModelVersion(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	verStr := r.URL.Query().Get("version")
	if name == "" || verStr == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and version are required")
		return
	}
	ver, err := strconv.ParseInt(verStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	mv, err := h.Store.GetModelVersion(r.Context(), name, ver)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getModelVersionResp{ModelVersion: modelVersionToDTO(mv)})
}

type updateModelVersionReq struct {
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	Description *string `json:"description,omitempty"`
}

// UpdateModelVersion handles POST /api/2.0/mlflow/model-versions/update.
func (h *Handler) UpdateModelVersion(w http.ResponseWriter, r *http.Request) {
	var req updateModelVersionReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Version == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and version are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	mv, err := h.Store.UpdateModelVersion(r.Context(), req.Name, ver, req.Description)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getModelVersionResp{ModelVersion: modelVersionToDTO(mv)})
}

type deleteModelVersionReq struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DeleteModelVersion handles POST /api/2.0/mlflow/model-versions/delete.
func (h *Handler) DeleteModelVersion(w http.ResponseWriter, r *http.Request) {
	var req deleteModelVersionReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Version == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and version are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	if err := h.Store.DeleteModelVersion(r.Context(), req.Name, ver); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type searchModelVersionsReq struct {
	Filter     string `json:"filter,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
}

type searchModelVersionsResp struct {
	ModelVersions []mvDTO `json:"model_versions"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}

// SearchModelVersions handles POST/GET /api/2.0/mlflow/model-versions/search.
func (h *Handler) SearchModelVersions(w http.ResponseWriter, r *http.Request) {
	var req searchModelVersionsReq
	_ = decodeJSON(r, &req)
	if req.Filter == "" {
		req.Filter = r.URL.Query().Get("filter")
	}
	if req.MaxResults == 0 {
		if v := r.URL.Query().Get("max_results"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				req.MaxResults = n
			}
		}
	}
	if req.PageToken == "" {
		req.PageToken = r.URL.Query().Get("page_token")
	}
	res, err := h.Store.SearchModelVersions(r.Context(), req.Filter, req.MaxResults, req.PageToken)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]mvDTO, 0, len(res.Items))
	for _, mv := range res.Items {
		out = append(out, modelVersionToDTO(mv))
	}
	writeJSON(w, searchModelVersionsResp{ModelVersions: out, NextPageToken: res.NextPageToken})
}

type getDownloadURIResp struct {
	ArtifactURI string `json:"artifact_uri"`
}

// GetModelVersionDownloadURI handles GET .../model-versions/get-download-uri.
// Returns the source URI that was registered with the model version.
func (h *Handler) GetModelVersionDownloadURI(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	verStr := r.URL.Query().Get("version")
	if name == "" || verStr == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name and version are required")
		return
	}
	ver, err := strconv.ParseInt(verStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	mv, err := h.Store.GetModelVersion(r.Context(), name, ver)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getDownloadURIResp{ArtifactURI: mv.Source})
}

type transitionStageReq struct {
	Name                  string `json:"name"`
	Version               string `json:"version"`
	Stage                 string `json:"stage"`
	ArchiveExistingVersions bool   `json:"archive_existing_versions"`
}

// TransitionModelStage handles POST .../model-versions/transition-stage.
func (h *Handler) TransitionModelStage(w http.ResponseWriter, r *http.Request) {
	var req transitionStageReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Version == "" || req.Stage == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name, version, and stage are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	mv, err := h.Store.TransitionModelStage(r.Context(), req.Name, ver, req.Stage, req.ArchiveExistingVersions)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, getModelVersionResp{ModelVersion: modelVersionToDTO(mv)})
}

type setModelVersionTagReq struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// SetModelVersionTag handles POST .../model-versions/set-tag.
func (h *Handler) SetModelVersionTag(w http.ResponseWriter, r *http.Request) {
	var req setModelVersionTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Version == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name, version, and key are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	if err := h.Store.SetModelVersionTag(r.Context(), req.Name, ver, req.Key, req.Value); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

type deleteModelVersionTagReq struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Key     string `json:"key"`
}

// DeleteModelVersionTag handles POST .../model-versions/delete-tag.
func (h *Handler) DeleteModelVersionTag(w http.ResponseWriter, r *http.Request) {
	var req deleteModelVersionTagReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.Version == "" || req.Key == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name, version, and key are required")
		return
	}
	ver, err := strconv.ParseInt(req.Version, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "version must be an integer")
		return
	}
	if err := h.Store.DeleteModelVersionTag(r.Context(), req.Name, ver, req.Key); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

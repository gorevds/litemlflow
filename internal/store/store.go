// Package store is the persistence layer for LiteMLflow.
//
// The Store interface defines the contract; SQLiteStore is the concrete
// implementation. Higher layers (api/mlflow, api/native) depend only on
// Store, which makes alternate stores possible (in-memory test stores,
// future Postgres adapter for very large deployments, etc.).
package store

import (
	"context"
	"errors"

	"github.com/gorevds/litemlflow/internal/model"
)

// Sentinel errors returned by Store implementations. Higher layers translate
// these into domain-specific responses (HTTP 404, MLflow ErrorCode, etc.).
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrConflict      = errors.New("conflict")

	// Validation sentinels — surfaced as 400 INVALID_PARAMETER_VALUE by
	// the HTTP handlers via errors.Is. Replaces the brittle
	// strings.Contains(err.Error(), "...") guards introduced in T2.5.
	ErrInvalidFilter = errors.New("invalid filter")
	ErrInvalidStage  = errors.New("invalid stage")
)

// SearchOptions controls listing endpoints.
type SearchOptions struct {
	ExperimentIDs  []int64  // empty means all
	LifecycleStage string   // "active" (default), "deleted", "all"
	Filter         string   // MLflow-style filter; subset supported
	OrderBy        []string // e.g., ["attributes.start_time DESC"]
	MaxResults     int      // capped by impl (default 100, max 50000)
	PageToken      string   // opaque cursor from previous response
	WorkspaceID    string   // if set, scope experiments to this workspace (default "default")
}

// SearchResult is a generic paginated result.
type SearchResult[T any] struct {
	Items         []T
	NextPageToken string
}

// ProjectTagKey is the canonical experiment-tag key that groups experiments
// into "projects" in the UI. It is namespaced (`lmf.` prefix) so it never
// collides with user-supplied tag keys. The MLflow client sets it via the
// standard `set_experiment_tag` endpoint, so no custom client is needed.
const ProjectTagKey = "lmf.project"

// ProjectSummary is one row in the projects-list response.
type ProjectSummary struct {
	// Name is the project name (the value of the lmf.project tag); empty
	// string represents experiments with no project assigned.
	Name string
	// Count is the number of (active) experiments in this project, in the
	// requested workspace.
	Count int
}

// MetricHistoryOptions controls the get-history endpoint.
// MaxResults=0 means return all points.
type MetricHistoryOptions struct {
	MaxResults int
	PageToken  string // opaque: "timestamp:step" encoded as base64
}

// RunLineage is the response type for GET /api/v1/runs/{id}/lineage.
//
// Direction semantics: ancestors walk up the parent_run_id chain;
// descendants are a BFS over child runs limited by DescendantDepth in
// LineageOptions. Datasets are the v1.2 dataset versions the run logged
// as inputs, joined through dataset_inputs.
type RunLineage struct {
	Run         *model.Run    `json:"run"`
	Ancestors   []*model.Run  `json:"ancestors"`
	Descendants []*model.Run  `json:"descendants"`
	Datasets    []DatasetEdge `json:"datasets"`
	// Truncated reports whether the descendant walk hit the depth cap or
	// the per-level fan-out limit. UI uses this to render "load more".
	Truncated bool `json:"truncated"`
}

// DatasetEdge links a run to a dataset version it consumed.
//
// Version and DatasetID are 0 when the run logged a dataset via the
// legacy v0.3 dataset_inputs path and there is no v1.2 datasets_v2 mirror
// in the run's workspace. Callers should treat 0 as "unmirrored" and
// route to the dataset-name index instead of the versioned detail page.
type DatasetEdge struct {
	RunID     string `json:"run_id"`
	Name      string `json:"name"`
	Version   int64  `json:"version,omitempty"` // 0 = no v1.2 mirror in this workspace
	Digest    string `json:"digest"`
	DatasetID int64  `json:"dataset_id,omitempty"` // 0 = no v1.2 mirror in this workspace
}

// LineageDirection is the v1.4 query mode.
type LineageDirection string

const (
	LineageUpstream   LineageDirection = "upstream"
	LineageDownstream LineageDirection = "downstream"
	LineageBoth       LineageDirection = "both"
)

// LineageOptions controls GetRunLineage.
//
// Zero / negative values are normalized to defaults inside the store —
// callers that want strict validation (e.g., HTTP handlers) should
// reject zero values before invoking, since the store substitution is
// silent. This intentional asymmetry: HTTP gives clear 4xx feedback,
// while internal Go callers get useful defaults.
type LineageOptions struct {
	Direction        LineageDirection
	DescendantDepth  int // BFS depth for downstream walk; clamped 1..8 (default 4 when ≤0)
	MaxNodesPerLevel int // fan-out cap per descendant level; clamped 1..200 (default 50 when ≤0)
}

// Store is the persistence interface.
type Store interface {
	// Lifecycle.
	Migrate(ctx context.Context) error
	Close() error

	// Experiments.
	CreateExperiment(ctx context.Context, e *model.Experiment) (int64, error)
	GetExperiment(ctx context.Context, id int64) (*model.Experiment, error)
	GetExperimentByName(ctx context.Context, name string) (*model.Experiment, error)
	UpdateExperiment(ctx context.Context, id int64, newName *string) error
	SetExperimentLifecycle(ctx context.Context, id int64, stage string) error
	SearchExperiments(ctx context.Context, opt SearchOptions) (SearchResult[*model.Experiment], error)
	SetExperimentTag(ctx context.Context, id int64, key, value string) error
	// ListProjects returns distinct values of the special tag `lmf.project` in
	// the workspace, with the count of experiments under each. Empty string
	// signals experiments without any project assigned. The UI uses this to
	// render the experiments list grouped by project. See docs/spec/api-native.md.
	ListProjects(ctx context.Context, workspaceID string) ([]ProjectSummary, error)

	// Runs.
	CreateRun(ctx context.Context, r *model.Run) error
	GetRun(ctx context.Context, id string) (*model.Run, error)
	// GetRunInWorkspace returns the run only if it belongs to the given
	// workspace (via its experiment); otherwise ErrNotFound. Used by native
	// read/write-by-run-ID handlers to scope access in multi-tenant mode
	// (independent-review finding: run-ID handlers were not workspace-scoped).
	GetRunInWorkspace(ctx context.Context, id, workspaceID string) (*model.Run, error)
	UpdateRun(ctx context.Context, id string, status *string, endTime *int64, name *string) error
	SetRunLifecycle(ctx context.Context, id string, stage string) error
	SearchRuns(ctx context.Context, opt SearchOptions) (SearchResult[*model.Run], error)

	// Metrics, params, tags.
	LogMetric(ctx context.Context, runID string, m model.Metric) error
	LogMetrics(ctx context.Context, runID string, ms []model.Metric) error
	LogParam(ctx context.Context, runID string, p model.Param) error
	LogParams(ctx context.Context, runID string, ps []model.Param) error
	SetTag(ctx context.Context, runID string, t model.KV) error
	SetTags(ctx context.Context, runID string, ts []model.KV) error
	DeleteTag(ctx context.Context, runID, key string) error
	GetMetricHistory(ctx context.Context, runID, key string, opt MetricHistoryOptions) ([]model.Metric, string, error)
	// GetMetricHistoryDownsampled fetches the full metric history for one key
	// and reduces it to at most target points using the LTTB algorithm.
	// Returns (downsampled, totalRawCount, error).
	GetMetricHistoryDownsampled(ctx context.Context, runID, key string, target int) ([]model.Metric, int64, error)
	GetParams(ctx context.Context, runID string) ([]model.Param, error)
	GetTags(ctx context.Context, runID string) ([]model.KV, error)
	GetLatestMetrics(ctx context.Context, runID string) ([]model.Metric, error)

	// Run notes (markdown).
	// SetRunNote upserts the note for a run. An empty content string deletes
	// the note row entirely. user may be empty for anonymous writes.
	SetRunNote(ctx context.Context, runID, content, user string) error
	// GetRunNote returns the note for a run. Returns ErrNotFound if no note has
	// been set.
	GetRunNote(ctx context.Context, runID string) (content, updatedBy string, updatedAt int64, err error)

	// Datasets / log_inputs.
	LogInputs(ctx context.Context, runID string, inputs []model.DatasetInput) error
	GetRunDatasets(ctx context.Context, runID string) ([]model.DatasetInput, error)

	// Traces.
	InsertSpans(ctx context.Context, spans []model.Span) error
	GetSpansByRun(ctx context.Context, runID string) ([]model.Span, error)
	GetSpansByTrace(ctx context.Context, traceID string) ([]model.Span, error)

	// Prompts.
	CreatePrompt(ctx context.Context, workspaceID string, p *model.Prompt) (int64, error)
	// ListPrompts returns the latest version of each prompt name, newest first.
	// Used by the UI prompts page (no per-user list scoping yet — single-user
	// instances are the v1 hero use case).
	ListPrompts(ctx context.Context, workspaceID string) ([]*model.Prompt, error)
	GetLatestPrompt(ctx context.Context, workspaceID, name string) (*model.Prompt, error)
	GetPromptVersion(ctx context.Context, workspaceID, name string, version int64) (*model.Prompt, error)
	ListPromptVersions(ctx context.Context, workspaceID, name string) ([]*model.Prompt, error)
	SetPromptAlias(ctx context.Context, workspaceID, name, alias string, version int64) error
	GetPromptByAlias(ctx context.Context, workspaceID, name, alias string) (*model.Prompt, error)

	// Evals.
	CreateEval(ctx context.Context, e *model.Eval) error
	GetEval(ctx context.Context, runID string) (*model.Eval, error)

	// Model Registry — Registered Models.
	CreateRegisteredModel(ctx context.Context, workspaceID string, m *model.RegisteredModel) error
	GetRegisteredModel(ctx context.Context, workspaceID, name string) (*model.RegisteredModel, error)
	RenameRegisteredModel(ctx context.Context, workspaceID, name, newName string) (*model.RegisteredModel, error)
	UpdateRegisteredModel(ctx context.Context, workspaceID, name string, description *string) (*model.RegisteredModel, error)
	DeleteRegisteredModel(ctx context.Context, workspaceID, name string) error
	SearchRegisteredModels(ctx context.Context, workspaceID, filter string, maxResults int, pageToken string) (SearchResult[*model.RegisteredModel], error)
	GetLatestModelVersions(ctx context.Context, workspaceID, name string, stages []string) ([]*model.ModelVersion, error)
	SetRegisteredModelTag(ctx context.Context, workspaceID, name, key, value string) error
	DeleteRegisteredModelTag(ctx context.Context, workspaceID, name, key string) error

	// Model Registry — Aliases.
	SetModelAlias(ctx context.Context, workspaceID, name, alias string, version int64) error
	DeleteModelAlias(ctx context.Context, workspaceID, name, alias string) error
	GetModelByAlias(ctx context.Context, workspaceID, name, alias string) (*model.ModelVersion, error)

	// Model Registry — Model Versions.
	CreateModelVersion(ctx context.Context, workspaceID string, mv *model.ModelVersion) (*model.ModelVersion, error)
	GetModelVersion(ctx context.Context, workspaceID, name string, version int64) (*model.ModelVersion, error)
	UpdateModelVersion(ctx context.Context, workspaceID, name string, version int64, description *string) (*model.ModelVersion, error)
	DeleteModelVersion(ctx context.Context, workspaceID, name string, version int64) error
	SearchModelVersions(ctx context.Context, workspaceID, filter string, maxResults int, pageToken string) (SearchResult[*model.ModelVersion], error)
	TransitionModelStage(ctx context.Context, workspaceID, name string, version int64, stage string, archiveExisting bool) (*model.ModelVersion, error)
	SetModelVersionTag(ctx context.Context, workspaceID, name string, version int64, key, value string) error
	DeleteModelVersionTag(ctx context.Context, workspaceID, name string, version int64, key string) error

	// Cross-experiment search (native /api/v1/search endpoint).
	// SearchRunsByName returns runs whose name LIKE %query% within the given
	// workspace, ordered by start_time DESC, limited to max results.
	// workspaceID="" falls back to "default".
	SearchRunsByName(ctx context.Context, workspaceID, query string, max int) ([]*model.Run, error)

	// Run lineage.
	GetRunLineage(ctx context.Context, runID string) (*RunLineage, error)
	// GetRunLineageWithOptions is the v1.4 extended form: directional walk
	// (upstream/downstream/both) with a configurable descendant depth.
	GetRunLineageWithOptions(ctx context.Context, runID string, opt LineageOptions) (*RunLineage, error)
	// GetRunAsOf returns the run's state and tags reconstructed at the
	// given unix-ms timestamp via the v1.5 event log replay. Returns
	// ErrNotFound if the run did not exist at that timestamp (start_time
	// > asOfMs). Metrics + params can be filtered separately at read
	// time using their native timestamp fields.
	GetRunAsOf(ctx context.Context, runID string, asOfMs int64) (*model.Run, []model.KV, error)
	// GetRunAsOfInWorkspace is the workspace-scoped variant — preferred
	// for HTTP handlers under the workspace middleware.
	GetRunAsOfInWorkspace(ctx context.Context, runID, workspaceID string, asOfMs int64) (*model.Run, []model.KV, error)
	// GetLatestMetricsAsOf returns the latest metric per key with
	// timestamp <= asOfMs. asOfMs <= 0 disables the filter (= current).
	GetLatestMetricsAsOf(ctx context.Context, runID string, asOfMs int64) ([]model.Metric, error)

	// Janitor.
	ArchiveStaleRuns(ctx context.Context, staleBefore int64) (int, error)
	PruneEventsBefore(ctx context.Context, beforeMs int64) (int, error)

	// Webhooks.
	CreateWebhook(ctx context.Context, w *model.Webhook) (int64, error)
	ListWebhooks(ctx context.Context, workspaceID string, expID *int64) ([]*model.Webhook, error)
	GetWebhook(ctx context.Context, id int64) (*model.Webhook, error)
	UpdateWebhook(ctx context.Context, w *model.Webhook) error
	DeleteWebhook(ctx context.Context, id int64) error
	RecordWebhookAttempt(ctx context.Context, id int64, status int, attempt int64) error

	// Experiment clone.
	CloneExperiment(ctx context.Context, srcID int64, newName, workspaceID string) (*model.Experiment, error)

	// Dashboards (per-project widget config).
	GetDashboard(ctx context.Context, workspaceID, project string) (*model.Dashboard, error)
	SaveDashboard(ctx context.Context, workspaceID, project, widgetsJSON string) (*model.Dashboard, error)

	// Analytics (templated DSL — see analytics.go for the contract).
	AnalyticsQuery(ctx context.Context, q AnalyticsQuery) (*AnalyticsResult, error)

	// Federation (v1.3) — peer instance management. Mutual HMAC over
	// HTTP; secrets are stored verbatim per peer (32 bytes hex).
	CreatePeer(ctx context.Context, p *model.Peer) (int64, error)
	ListPeers(ctx context.Context, workspaceID string) ([]*model.Peer, error)
	GetPeer(ctx context.Context, workspaceID string, id int64) (*model.Peer, error)
	GetPeerByName(ctx context.Context, workspaceID, name string) (*model.Peer, error)
	DeletePeer(ctx context.Context, workspaceID string, id int64) error
	UpdatePeerStatus(ctx context.Context, id int64, status, lastError string, lastSeen int64) error

	// Dataset versioning (v1.2). Datasets are content-addressed +
	// per-workspace + per-name versioned. The Store is responsible for
	// row CRUD only; the actual content bytes live in the CAS store
	// (internal/datasets).
	CreateDatasetVersion(ctx context.Context, d *model.DatasetVersion, parents []int64) (*model.DatasetVersion, error)
	ListDatasets(ctx context.Context, workspaceID string) ([]*model.DatasetVersion, error)
	ListDatasetVersions(ctx context.Context, workspaceID, name string) ([]*model.DatasetVersion, error)
	GetDatasetVersion(ctx context.Context, workspaceID, name string, version int64) (*model.DatasetVersion, error)
	GetDatasetLineage(ctx context.Context, workspaceID, name string, version int64) (*model.DatasetLineage, error)
	SoftDeleteDatasetVersion(ctx context.Context, workspaceID, name string, version int64) error
	DatasetHashStillReferenced(ctx context.Context, hash string) (bool, error)

	// Workspaces.
	CreateWorkspace(ctx context.Context, w *model.Workspace) error
	GetWorkspace(ctx context.Context, id string) (*model.Workspace, error)
	ListWorkspaces(ctx context.Context) ([]*model.Workspace, error)
	UpdateWorkspace(ctx context.Context, id string, name *string, description *string) error
	DeleteWorkspace(ctx context.Context, id string) error

	// Workspace members.
	AddMember(ctx context.Context, workspaceID, userID, role string) error
	RemoveMember(ctx context.Context, workspaceID, userID string) error
	ListMembers(ctx context.Context, workspaceID string) ([]*model.WorkspaceMember, error)
	GetMemberRole(ctx context.Context, workspaceID, userID string) (string, error)

	// Scoped experiment lookup.
	GetExperimentByNameInWorkspace(ctx context.Context, workspaceID, name string) (*model.Experiment, error)
}

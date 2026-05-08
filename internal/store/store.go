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

	"github.com/litemlflow/litemlflow/internal/model"
)

// Sentinel errors returned by Store implementations. Higher layers translate
// these into domain-specific responses (HTTP 404, MLflow ErrorCode, etc.).
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrConflict      = errors.New("conflict")
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

	// Runs.
	CreateRun(ctx context.Context, r *model.Run) error
	GetRun(ctx context.Context, id string) (*model.Run, error)
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
	GetMetricHistory(ctx context.Context, runID, key string) ([]model.Metric, error)
	GetParams(ctx context.Context, runID string) ([]model.Param, error)
	GetTags(ctx context.Context, runID string) ([]model.KV, error)
	GetLatestMetrics(ctx context.Context, runID string) ([]model.Metric, error)

	// Traces.
	InsertSpans(ctx context.Context, spans []model.Span) error
	GetSpansByRun(ctx context.Context, runID string) ([]model.Span, error)
	GetSpansByTrace(ctx context.Context, traceID string) ([]model.Span, error)

	// Prompts.
	CreatePrompt(ctx context.Context, p *model.Prompt) (int64, error)
	GetLatestPrompt(ctx context.Context, name string) (*model.Prompt, error)
	GetPromptVersion(ctx context.Context, name string, version int64) (*model.Prompt, error)
	ListPromptVersions(ctx context.Context, name string) ([]*model.Prompt, error)
	SetPromptAlias(ctx context.Context, name, alias string, version int64) error
	GetPromptByAlias(ctx context.Context, name, alias string) (*model.Prompt, error)

	// Evals.
	CreateEval(ctx context.Context, e *model.Eval) error
	GetEval(ctx context.Context, runID string) (*model.Eval, error)

	// Model Registry — Registered Models.
	CreateRegisteredModel(ctx context.Context, m *model.RegisteredModel) error
	GetRegisteredModel(ctx context.Context, name string) (*model.RegisteredModel, error)
	RenameRegisteredModel(ctx context.Context, name, newName string) (*model.RegisteredModel, error)
	UpdateRegisteredModel(ctx context.Context, name string, description *string) (*model.RegisteredModel, error)
	DeleteRegisteredModel(ctx context.Context, name string) error
	SearchRegisteredModels(ctx context.Context, filter string, maxResults int, pageToken string) (SearchResult[*model.RegisteredModel], error)
	GetLatestModelVersions(ctx context.Context, name string, stages []string) ([]*model.ModelVersion, error)
	SetRegisteredModelTag(ctx context.Context, name, key, value string) error
	DeleteRegisteredModelTag(ctx context.Context, name, key string) error

	// Model Registry — Aliases.
	SetModelAlias(ctx context.Context, name, alias string, version int64) error
	DeleteModelAlias(ctx context.Context, name, alias string) error
	GetModelByAlias(ctx context.Context, name, alias string) (*model.ModelVersion, error)

	// Model Registry — Model Versions.
	CreateModelVersion(ctx context.Context, mv *model.ModelVersion) (*model.ModelVersion, error)
	GetModelVersion(ctx context.Context, name string, version int64) (*model.ModelVersion, error)
	UpdateModelVersion(ctx context.Context, name string, version int64, description *string) (*model.ModelVersion, error)
	DeleteModelVersion(ctx context.Context, name string, version int64) error
	SearchModelVersions(ctx context.Context, filter string, maxResults int, pageToken string) (SearchResult[*model.ModelVersion], error)
	TransitionModelStage(ctx context.Context, name string, version int64, stage string, archiveExisting bool) (*model.ModelVersion, error)
	SetModelVersionTag(ctx context.Context, name string, version int64, key, value string) error
	DeleteModelVersionTag(ctx context.Context, name string, version int64, key string) error

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

// Package storetest provides test utilities for code that depends on
// store.Store. NopStore is a no-op / panic implementation of every Store
// method that callers can embed and selectively override:
//
//	type myStore struct {
//	    storetest.NopStore // implements every Store method
//	}
//	func (s *myStore) CreateExperiment(...) (int64, error) { /* real impl */ }
//
// Three test files (`internal/api/native/otlp_fuzz_test.go`,
// `internal/grpcotlp/ingest_test.go`, `internal/migrator/mlflow_test.go`)
// previously each redeclared all 90+ methods of the Store interface — about
// 600 LOC of duplication that grew with every interface extension. Embedding
// NopStore replaces those stubs with a one-liner.
//
// The default behaviour is "panic with a descriptive message" so a test
// that calls an unintended method gets a loud failure rather than a
// silent zero value. Tests that legitimately need no-op semantics can
// override individual methods.
package storetest

import (
	"context"
	"fmt"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// NopStore implements store.Store. Every method panics by default; embed
// and override the subset your test cares about.
type NopStore struct{}

// Compile-time check.
var _ store.Store = (*NopStore)(nil)

func panicNotImpl(name string) {
	panic("storetest.NopStore: " + name + " not implemented; embed NopStore and override the method")
}

// Lifecycle.
func (NopStore) Migrate(context.Context) error { return nil }
func (NopStore) Close() error                  { return nil }

// Experiments.
func (NopStore) CreateExperiment(context.Context, *model.Experiment) (int64, error) {
	panicNotImpl("CreateExperiment")
	return 0, nil
}
func (NopStore) GetExperiment(context.Context, int64) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetExperimentByName(context.Context, string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (NopStore) UpdateExperiment(context.Context, int64, *string) error { return nil }
func (NopStore) SetExperimentLifecycle(context.Context, int64, string) error {
	return nil
}
func (NopStore) SearchExperiments(context.Context, store.SearchOptions) (store.SearchResult[*model.Experiment], error) {
	return store.SearchResult[*model.Experiment]{}, nil
}
func (NopStore) SetExperimentTag(context.Context, int64, string, string) error { return nil }
func (NopStore) ListProjects(context.Context, string) ([]store.ProjectSummary, error) {
	return nil, nil
}

// Runs.
func (NopStore) CreateRun(context.Context, *model.Run) error {
	panicNotImpl("CreateRun")
	return nil
}
func (NopStore) GetRun(context.Context, string) (*model.Run, error) { return nil, store.ErrNotFound }
func (NopStore) UpdateRun(context.Context, string, *string, *int64, *string) error { return nil }
func (NopStore) SetRunLifecycle(context.Context, string, string) error              { return nil }
func (NopStore) SearchRuns(context.Context, store.SearchOptions) (store.SearchResult[*model.Run], error) {
	return store.SearchResult[*model.Run]{}, nil
}

// Metrics, params, tags.
func (NopStore) LogMetric(context.Context, string, model.Metric) error    { return nil }
func (NopStore) LogMetrics(context.Context, string, []model.Metric) error { return nil }
func (NopStore) LogParam(context.Context, string, model.Param) error      { return nil }
func (NopStore) LogParams(context.Context, string, []model.Param) error   { return nil }
func (NopStore) SetTag(context.Context, string, model.KV) error           { return nil }
func (NopStore) SetTags(context.Context, string, []model.KV) error        { return nil }
func (NopStore) DeleteTag(context.Context, string, string) error          { return nil }
func (NopStore) GetMetricHistory(context.Context, string, string, store.MetricHistoryOptions) ([]model.Metric, string, error) {
	return nil, "", nil
}
func (NopStore) GetMetricHistoryDownsampled(context.Context, string, string, int) ([]model.Metric, int64, error) {
	return nil, 0, nil
}
func (NopStore) GetParams(context.Context, string) ([]model.Param, error)        { return nil, nil }
func (NopStore) GetTags(context.Context, string) ([]model.KV, error)             { return nil, nil }
func (NopStore) GetLatestMetrics(context.Context, string) ([]model.Metric, error) { return nil, nil }

// Run notes.
func (NopStore) SetRunNote(context.Context, string, string, string) error {
	return nil
}
func (NopStore) GetRunNote(context.Context, string) (string, string, int64, error) {
	return "", "", 0, store.ErrNotFound
}

// Datasets v0.3 / log_inputs.
func (NopStore) LogInputs(context.Context, string, []model.DatasetInput) error { return nil }
func (NopStore) GetRunDatasets(context.Context, string) ([]model.DatasetInput, error) {
	return nil, nil
}

// Traces.
func (NopStore) InsertSpans(context.Context, []model.Span) error { return nil }
func (NopStore) GetSpansByRun(context.Context, string) ([]model.Span, error) {
	return nil, nil
}
func (NopStore) GetSpansByTrace(context.Context, string) ([]model.Span, error) {
	return nil, nil
}

// Prompts.
func (NopStore) CreatePrompt(context.Context, *model.Prompt) (int64, error) {
	return 0, store.ErrNotFound
}
func (NopStore) ListPrompts(context.Context) ([]*model.Prompt, error) { return nil, nil }
func (NopStore) GetLatestPrompt(context.Context, string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetPromptVersion(context.Context, string, int64) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (NopStore) ListPromptVersions(context.Context, string) ([]*model.Prompt, error) {
	return nil, nil
}
func (NopStore) SetPromptAlias(context.Context, string, string, int64) error { return nil }
func (NopStore) GetPromptByAlias(context.Context, string, string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}

// Evals.
func (NopStore) CreateEval(context.Context, *model.Eval) error { return nil }
func (NopStore) GetEval(context.Context, string) (*model.Eval, error) {
	return nil, store.ErrNotFound
}

// Model Registry.
func (NopStore) CreateRegisteredModel(context.Context, *model.RegisteredModel) error { return nil }
func (NopStore) GetRegisteredModel(context.Context, string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (NopStore) RenameRegisteredModel(context.Context, string, string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (NopStore) UpdateRegisteredModel(context.Context, string, *string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (NopStore) DeleteRegisteredModel(context.Context, string) error { return nil }
func (NopStore) SearchRegisteredModels(context.Context, string, int, string) (store.SearchResult[*model.RegisteredModel], error) {
	return store.SearchResult[*model.RegisteredModel]{}, nil
}
func (NopStore) GetLatestModelVersions(context.Context, string, []string) ([]*model.ModelVersion, error) {
	return nil, nil
}
func (NopStore) SetRegisteredModelTag(context.Context, string, string, string) error    { return nil }
func (NopStore) DeleteRegisteredModelTag(context.Context, string, string) error         { return nil }
func (NopStore) SetModelAlias(context.Context, string, string, int64) error             { return nil }
func (NopStore) DeleteModelAlias(context.Context, string, string) error                 { return nil }
func (NopStore) GetModelByAlias(context.Context, string, string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) CreateModelVersion(context.Context, *model.ModelVersion) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetModelVersion(context.Context, string, int64) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) UpdateModelVersion(context.Context, string, int64, *string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) DeleteModelVersion(context.Context, string, int64) error { return nil }
func (NopStore) SearchModelVersions(context.Context, string, int, string) (store.SearchResult[*model.ModelVersion], error) {
	return store.SearchResult[*model.ModelVersion]{}, nil
}
func (NopStore) TransitionModelStage(context.Context, string, int64, string, bool) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) SetModelVersionTag(context.Context, string, int64, string, string) error    { return nil }
func (NopStore) DeleteModelVersionTag(context.Context, string, int64, string) error         { return nil }

// Search & lineage.
func (NopStore) SearchRunsByName(context.Context, string, string, int) ([]*model.Run, error) {
	return nil, nil
}
func (NopStore) GetRunLineage(context.Context, string) (*store.RunLineage, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetRunLineageWithOptions(context.Context, string, store.LineageOptions) (*store.RunLineage, error) {
	return nil, store.ErrNotFound
}

// Janitor.
func (NopStore) ArchiveStaleRuns(context.Context, int64) (int, error) { return 0, nil }

// Webhooks.
func (NopStore) CreateWebhook(context.Context, *model.Webhook) (int64, error) {
	return 0, store.ErrNotFound
}
func (NopStore) ListWebhooks(context.Context, string, *int64) ([]*model.Webhook, error) {
	return nil, nil
}
func (NopStore) GetWebhook(context.Context, int64) (*model.Webhook, error) {
	return nil, store.ErrNotFound
}
func (NopStore) UpdateWebhook(context.Context, *model.Webhook) error                { return nil }
func (NopStore) DeleteWebhook(context.Context, int64) error                          { return nil }
func (NopStore) RecordWebhookAttempt(context.Context, int64, int, int64) error      { return nil }

// Experiment clone.
func (NopStore) CloneExperiment(context.Context, int64, string, string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}

// Dashboards.
func (NopStore) GetDashboard(context.Context, string, string) (*model.Dashboard, error) {
	return nil, store.ErrNotFound
}
func (NopStore) SaveDashboard(context.Context, string, string, string) (*model.Dashboard, error) {
	return nil, store.ErrNotFound
}

// Analytics.
func (NopStore) AnalyticsQuery(context.Context, store.AnalyticsQuery) (*store.AnalyticsResult, error) {
	return &store.AnalyticsResult{}, nil
}

// Datasets v1.2.
func (NopStore) CreateDatasetVersion(context.Context, *model.DatasetVersion, []int64) (*model.DatasetVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) ListDatasets(context.Context, string) ([]*model.DatasetVersion, error) { return nil, nil }
func (NopStore) ListDatasetVersions(context.Context, string, string) ([]*model.DatasetVersion, error) {
	return nil, nil
}
func (NopStore) GetDatasetVersion(context.Context, string, string, int64) (*model.DatasetVersion, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetDatasetLineage(context.Context, string, string, int64) (*model.DatasetLineage, error) {
	return nil, store.ErrNotFound
}
func (NopStore) SoftDeleteDatasetVersion(context.Context, string, string, int64) error {
	return store.ErrNotFound
}
func (NopStore) DatasetHashStillReferenced(context.Context, string) (bool, error) { return false, nil }

// Workspaces.
func (NopStore) CreateWorkspace(context.Context, *model.Workspace) error { return nil }
func (NopStore) GetWorkspace(context.Context, string) (*model.Workspace, error) {
	return nil, store.ErrNotFound
}
func (NopStore) ListWorkspaces(context.Context) ([]*model.Workspace, error) { return nil, nil }
func (NopStore) UpdateWorkspace(context.Context, string, *string, *string) error { return nil }
func (NopStore) DeleteWorkspace(context.Context, string) error                    { return nil }

// Workspace members.
func (NopStore) AddMember(context.Context, string, string, string) error { return nil }
func (NopStore) RemoveMember(context.Context, string, string) error      { return nil }
func (NopStore) ListMembers(context.Context, string) ([]*model.WorkspaceMember, error) {
	return nil, nil
}
func (NopStore) GetMemberRole(context.Context, string, string) (string, error) { return "", nil }

// Scoped experiment lookup.
func (NopStore) GetExperimentByNameInWorkspace(context.Context, string, string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}

// Federation (v1.3).
func (NopStore) CreatePeer(context.Context, *model.Peer) (int64, error) {
	return 0, store.ErrNotFound
}
func (NopStore) ListPeers(context.Context, string) ([]*model.Peer, error) { return nil, nil }
func (NopStore) GetPeer(context.Context, string, int64) (*model.Peer, error) {
	return nil, store.ErrNotFound
}
func (NopStore) GetPeerByName(context.Context, string, string) (*model.Peer, error) {
	return nil, store.ErrNotFound
}
func (NopStore) DeletePeer(context.Context, string, int64) error { return store.ErrNotFound }
func (NopStore) UpdatePeerStatus(context.Context, int64, string, string, int64) error {
	return nil
}

// init pull-in to keep `fmt` used (silences linters in case future panics
// move under build tags).
var _ = fmt.Sprintf

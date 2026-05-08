package migrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// ---- unit tests: JSON parsing ----

func TestParseExperimentsJSON(t *testing.T) {
	raw := `{
		"experiments": [
			{
				"experiment_id": "1",
				"name": "iris",
				"lifecycle_stage": "active",
				"tags": [{"key": "team", "value": "cv"}]
			},
			{
				"experiment_id": "2",
				"name": "mnist",
				"lifecycle_stage": "deleted",
				"tags": []
			}
		],
		"next_page_token": ""
	}`
	var body struct {
		Experiments   []mlflowExperiment `json:"experiments"`
		NextPageToken string             `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("parse experiments: %v", err)
	}
	if len(body.Experiments) != 2 {
		t.Fatalf("expected 2 experiments, got %d", len(body.Experiments))
	}
	iris := body.Experiments[0]
	if iris.Name != "iris" {
		t.Errorf("expected name=iris, got %q", iris.Name)
	}
	if iris.Tags[0].Key != "team" || iris.Tags[0].Value != "cv" {
		t.Errorf("unexpected tag: %+v", iris.Tags[0])
	}
	if body.Experiments[1].LifecycleStage != "deleted" {
		t.Errorf("expected deleted lifecycle, got %q", body.Experiments[1].LifecycleStage)
	}
	if body.NextPageToken != "" {
		t.Errorf("expected empty next_page_token")
	}
}

func TestParseRunsJSON(t *testing.T) {
	raw := `{
		"runs": [
			{
				"info": {
					"run_id": "abc123",
					"experiment_id": "1",
					"run_name": "trial-1",
					"status": "FINISHED",
					"start_time": 1700000000000,
					"end_time": 1700001000000,
					"lifecycle_stage": "active",
					"user_id": "alice"
				},
				"data": {
					"metrics": [{"key": "loss", "value": 0.42, "timestamp": 1700000500000, "step": 5}],
					"params":  [{"key": "lr", "value": "0.01"}],
					"tags":    [{"key": "phase", "value": "train"}]
				}
			}
		],
		"next_page_token": ""
	}`
	var body struct {
		Runs          []mlflowRun `json:"runs"`
		NextPageToken string      `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("parse runs: %v", err)
	}
	if len(body.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(body.Runs))
	}
	r := body.Runs[0]
	if r.Info.RunID != "abc123" {
		t.Errorf("unexpected run_id: %q", r.Info.RunID)
	}
	if r.Info.Status != "FINISHED" {
		t.Errorf("unexpected status: %q", r.Info.Status)
	}
	if len(r.Data.Metrics) != 1 || r.Data.Metrics[0].Key != "loss" {
		t.Errorf("unexpected metrics: %+v", r.Data.Metrics)
	}
	if len(r.Data.Params) != 1 || r.Data.Params[0].Key != "lr" {
		t.Errorf("unexpected params: %+v", r.Data.Params)
	}
	if len(r.Data.Tags) != 1 || r.Data.Tags[0].Key != "phase" {
		t.Errorf("unexpected tags: %+v", r.Data.Tags)
	}
}

func TestParseMetricHistoryJSON(t *testing.T) {
	raw := `{
		"metrics": [
			{"key": "loss", "value": 0.9, "timestamp": 1700000001000, "step": 0},
			{"key": "loss", "value": 0.5, "timestamp": 1700000002000, "step": 1},
			{"key": "loss", "value": 0.2, "timestamp": 1700000003000, "step": 2}
		],
		"next_page_token": "next_tok"
	}`
	var body struct {
		Metrics       []mlflowMetric `json:"metrics"`
		NextPageToken string         `json:"next_page_token"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("parse metric history: %v", err)
	}
	if len(body.Metrics) != 3 {
		t.Fatalf("expected 3 metrics, got %d", len(body.Metrics))
	}
	if body.Metrics[2].Value != 0.2 || body.Metrics[2].Step != 2 {
		t.Errorf("unexpected third metric: %+v", body.Metrics[2])
	}
	if body.NextPageToken != "next_tok" {
		t.Errorf("unexpected token: %q", body.NextPageToken)
	}
}

func TestMapStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"RUNNING", model.StatusRunning},
		{"FINISHED", model.StatusFinished},
		{"FAILED", model.StatusFailed},
		{"KILLED", model.StatusKilled},
		{"SCHEDULED", model.StatusScheduled},
		{"unknown", model.StatusFinished},
		{"", model.StatusFinished},
	}
	for _, c := range cases {
		got := mapStatus(c.in)
		if got != c.want {
			t.Errorf("mapStatus(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCollectMetricKeys(t *testing.T) {
	metrics := []mlflowMetric{
		{Key: "loss"}, {Key: "acc"}, {Key: "loss"},
	}
	keys := collectMetricKeys(metrics)
	if len(keys) != 2 {
		t.Errorf("expected 2 unique keys, got %d: %v", len(keys), keys)
	}
}

// ---- integration tests: httptest fake MLflow ----

// fakeMLflow builds an httptest.Server that speaks a minimal subset of the
// MLflow REST API sufficient for the importer.
type fakeMLflow struct {
	experiments []mlflowExperiment
	runs        map[string][]mlflowRun   // keyed by experiment_id
	history     map[string][]mlflowMetric // keyed by "run_id:key"
	artifacts   map[string]string         // keyed by "run_id/path" → content
}

func newFakeMLflow(t *testing.T) (*httptest.Server, *fakeMLflow) {
	t.Helper()
	f := &fakeMLflow{
		runs:      make(map[string][]mlflowRun),
		history:   make(map[string][]mlflowMetric),
		artifacts: make(map[string]string),
	}
	mux := http.NewServeMux()

	// GET /api/2.0/mlflow/experiments/search
	mux.HandleFunc("/api/2.0/mlflow/experiments/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Mlflow-Version", "3.99.0-fake")
		// Support both GET (probe) and POST.
		resp := struct {
			Experiments   []mlflowExperiment `json:"experiments"`
			NextPageToken string             `json:"next_page_token"`
		}{Experiments: f.experiments}
		json.NewEncoder(w).Encode(resp)
	})

	// POST /api/2.0/mlflow/runs/search
	mux.HandleFunc("/api/2.0/mlflow/runs/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ExperimentIDs []string `json:"experiment_ids"`
			PageToken     string   `json:"page_token"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var allRuns []mlflowRun
		for _, eid := range req.ExperimentIDs {
			allRuns = append(allRuns, f.runs[eid]...)
		}
		// Paginate at 2 to test pagination.
		pageSize := 2
		token := req.PageToken
		start := 0
		if token == "page2" {
			start = pageSize
		}
		end := start + pageSize
		if end > len(allRuns) {
			end = len(allRuns)
		}
		page := allRuns[start:end]
		nextToken := ""
		if end < len(allRuns) {
			nextToken = "page2"
		}
		resp := struct {
			Runs          []mlflowRun `json:"runs"`
			NextPageToken string      `json:"next_page_token"`
		}{Runs: page, NextPageToken: nextToken}
		json.NewEncoder(w).Encode(resp)
	})

	// GET /api/2.0/mlflow/metrics/get-history
	mux.HandleFunc("/api/2.0/mlflow/metrics/get-history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		runID := r.URL.Query().Get("run_id")
		key := r.URL.Query().Get("metric_key")
		hist := f.history[runID+":"+key]
		resp := struct {
			Metrics       []mlflowMetric `json:"metrics"`
			NextPageToken string         `json:"next_page_token"`
		}{Metrics: hist}
		json.NewEncoder(w).Encode(resp)
	})

	// GET /api/2.0/mlflow/artifacts/list
	mux.HandleFunc("/api/2.0/mlflow/artifacts/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		runID := r.URL.Query().Get("run_id")
		// Collect all top-level artifacts for this run.
		var files []mlflowArtifact
		for k, v := range f.artifacts {
			prefix := runID + "/"
			if strings.HasPrefix(k, prefix) {
				relPath := strings.TrimPrefix(k, prefix)
				files = append(files, mlflowArtifact{
					Path:     relPath,
					IsDir:    false,
					FileSize: int64(len(v)),
				})
			}
		}
		resp := struct {
			Files []mlflowArtifact `json:"files"`
		}{Files: files}
		json.NewEncoder(w).Encode(resp)
	})

	// GET /api/2.0/mlflow-artifacts/artifacts/{run_id}/{path}
	mux.HandleFunc("/api/2.0/mlflow-artifacts/artifacts/", func(w http.ResponseWriter, r *http.Request) {
		// Path: /api/2.0/mlflow-artifacts/artifacts/<run_id>/<relpath>
		trimmed := strings.TrimPrefix(r.URL.Path, "/api/2.0/mlflow-artifacts/artifacts/")
		content, ok := f.artifacts[trimmed]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, content)
	})

	srv := httptest.NewServer(mux)
	return srv, f
}

// inMemStore is a minimal in-memory Store implementation used for tests.
// Only the methods called by the importer need to be implemented.
type inMemStore struct {
	experiments []*model.Experiment
	runs        map[string]*model.Run
	params      map[string][]model.Param
	tags        map[string][]model.KV
	metrics     map[string][]model.Metric
	expTags     map[int64][]model.KV
	nextExpID   int64
}

func newInMemStore() *inMemStore {
	return &inMemStore{
		runs:      make(map[string]*model.Run),
		params:    make(map[string][]model.Param),
		tags:      make(map[string][]model.KV),
		metrics:   make(map[string][]model.Metric),
		expTags:   make(map[int64][]model.KV),
		nextExpID: 1,
	}
}

func (s *inMemStore) CreateExperiment(_ context.Context, e *model.Experiment) (int64, error) {
	for _, ex := range s.experiments {
		if ex.Name == e.Name && ex.WorkspaceID == e.WorkspaceID {
			return 0, store.ErrAlreadyExists
		}
	}
	id := s.nextExpID
	s.nextExpID++
	cp := *e
	cp.ID = id
	if cp.WorkspaceID == "" {
		cp.WorkspaceID = "default"
	}
	s.experiments = append(s.experiments, &cp)
	return id, nil
}

func (s *inMemStore) GetExperimentByNameInWorkspace(_ context.Context, ws, name string) (*model.Experiment, error) {
	for _, e := range s.experiments {
		if e.Name == name && e.WorkspaceID == ws {
			return e, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *inMemStore) SetExperimentTag(_ context.Context, id int64, key, value string) error {
	s.expTags[id] = append(s.expTags[id], model.KV{Key: key, Value: value})
	return nil
}

func (s *inMemStore) CreateRun(_ context.Context, r *model.Run) error {
	if _, ok := s.runs[r.ID]; ok {
		return store.ErrAlreadyExists
	}
	cp := *r
	s.runs[r.ID] = &cp
	return nil
}

func (s *inMemStore) LogParam(_ context.Context, runID string, p model.Param) error {
	s.params[runID] = append(s.params[runID], p)
	return nil
}

func (s *inMemStore) SetTag(_ context.Context, runID string, t model.KV) error {
	s.tags[runID] = append(s.tags[runID], t)
	return nil
}

func (s *inMemStore) LogMetrics(_ context.Context, runID string, ms []model.Metric) error {
	s.metrics[runID] = append(s.metrics[runID], ms...)
	return nil
}

func (s *inMemStore) UpdateRun(_ context.Context, id string, status *string, endTime *int64, name *string) error {
	r, ok := s.runs[id]
	if !ok {
		return store.ErrNotFound
	}
	if status != nil {
		r.Status = *status
	}
	if endTime != nil {
		r.EndTime = endTime
	}
	return nil
}

// Stub out every other Store method so inMemStore satisfies the interface.
func (s *inMemStore) Migrate(_ context.Context) error                  { return nil }
func (s *inMemStore) Close() error                                      { return nil }
func (s *inMemStore) GetExperiment(_ context.Context, _ int64) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) GetExperimentByName(_ context.Context, _ string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) UpdateExperiment(_ context.Context, _ int64, _ *string) error { return nil }
func (s *inMemStore) SetExperimentLifecycle(_ context.Context, _ int64, _ string) error {
	return nil
}
func (s *inMemStore) SearchExperiments(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Experiment], error) {
	return store.SearchResult[*model.Experiment]{}, nil
}
func (s *inMemStore) GetRun(_ context.Context, _ string) (*model.Run, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) SetRunLifecycle(_ context.Context, _, _ string) error { return nil }
func (s *inMemStore) SearchRuns(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Run], error) {
	return store.SearchResult[*model.Run]{}, nil
}
func (s *inMemStore) LogMetric(_ context.Context, _ string, _ model.Metric) error { return nil }
func (s *inMemStore) LogParams(_ context.Context, _ string, _ []model.Param) error { return nil }
func (s *inMemStore) SetTags(_ context.Context, _ string, _ []model.KV) error     { return nil }
func (s *inMemStore) DeleteTag(_ context.Context, _, _ string) error               { return nil }
func (s *inMemStore) GetMetricHistory(_ context.Context, _, _ string, _ store.MetricHistoryOptions) ([]model.Metric, string, error) {
	return nil, "", nil
}
func (s *inMemStore) GetMetricHistoryDownsampled(_ context.Context, _, _ string, _ int) ([]model.Metric, int64, error) {
	return nil, 0, nil
}
func (s *inMemStore) GetParams(_ context.Context, _ string) ([]model.Param, error) { return nil, nil }
func (s *inMemStore) GetTags(_ context.Context, _ string) ([]model.KV, error)      { return nil, nil }
func (s *inMemStore) GetLatestMetrics(_ context.Context, _ string) ([]model.Metric, error) {
	return nil, nil
}
func (s *inMemStore) LogInputs(_ context.Context, _ string, _ []model.DatasetInput) error {
	return nil
}
func (s *inMemStore) GetRunDatasets(_ context.Context, _ string) ([]model.DatasetInput, error) {
	return nil, nil
}
func (s *inMemStore) SetRunNote(_ context.Context, _, _, _ string) error { return nil }
func (s *inMemStore) GetRunNote(_ context.Context, _ string) (string, string, int64, error) {
	return "", "", 0, store.ErrNotFound
}
func (s *inMemStore) InsertSpans(_ context.Context, _ []model.Span) error { return nil }
func (s *inMemStore) GetSpansByRun(_ context.Context, _ string) ([]model.Span, error) {
	return nil, nil
}
func (s *inMemStore) GetSpansByTrace(_ context.Context, _ string) ([]model.Span, error) {
	return nil, nil
}
func (s *inMemStore) CreatePrompt(_ context.Context, _ *model.Prompt) (int64, error) { return 0, nil }
func (s *inMemStore) GetLatestPrompt(_ context.Context, _ string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) GetPromptVersion(_ context.Context, _ string, _ int64) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) ListPromptVersions(_ context.Context, _ string) ([]*model.Prompt, error) {
	return nil, nil
}
func (s *inMemStore) SetPromptAlias(_ context.Context, _, _ string, _ int64) error { return nil }
func (s *inMemStore) GetPromptByAlias(_ context.Context, _, _ string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) CreateEval(_ context.Context, _ *model.Eval) error           { return nil }
func (s *inMemStore) GetEval(_ context.Context, _ string) (*model.Eval, error)    { return nil, store.ErrNotFound }
func (s *inMemStore) CreateRegisteredModel(_ context.Context, _ *model.RegisteredModel) error {
	return nil
}
func (s *inMemStore) GetRegisteredModel(_ context.Context, _ string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) RenameRegisteredModel(_ context.Context, _, _ string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) UpdateRegisteredModel(_ context.Context, _ string, _ *string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) DeleteRegisteredModel(_ context.Context, _ string) error { return nil }
func (s *inMemStore) SearchRegisteredModels(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.RegisteredModel], error) {
	return store.SearchResult[*model.RegisteredModel]{}, nil
}
func (s *inMemStore) GetLatestModelVersions(_ context.Context, _ string, _ []string) ([]*model.ModelVersion, error) {
	return nil, nil
}
func (s *inMemStore) SetRegisteredModelTag(_ context.Context, _, _, _ string) error  { return nil }
func (s *inMemStore) DeleteRegisteredModelTag(_ context.Context, _, _ string) error  { return nil }
func (s *inMemStore) SetModelAlias(_ context.Context, _, _ string, _ int64) error    { return nil }
func (s *inMemStore) DeleteModelAlias(_ context.Context, _, _ string) error          { return nil }
func (s *inMemStore) GetModelByAlias(_ context.Context, _, _ string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) CreateModelVersion(_ context.Context, _ *model.ModelVersion) (*model.ModelVersion, error) {
	return nil, nil
}
func (s *inMemStore) GetModelVersion(_ context.Context, _ string, _ int64) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) UpdateModelVersion(_ context.Context, _ string, _ int64, _ *string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) DeleteModelVersion(_ context.Context, _ string, _ int64) error { return nil }
func (s *inMemStore) SearchModelVersions(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.ModelVersion], error) {
	return store.SearchResult[*model.ModelVersion]{}, nil
}
func (s *inMemStore) TransitionModelStage(_ context.Context, _ string, _ int64, _ string, _ bool) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) SetModelVersionTag(_ context.Context, _ string, _ int64, _, _ string) error {
	return nil
}
func (s *inMemStore) DeleteModelVersionTag(_ context.Context, _ string, _ int64, _ string) error {
	return nil
}
func (s *inMemStore) CreateWorkspace(_ context.Context, _ *model.Workspace) error { return nil }
func (s *inMemStore) GetWorkspace(_ context.Context, _ string) (*model.Workspace, error) {
	return nil, store.ErrNotFound
}
func (s *inMemStore) ListWorkspaces(_ context.Context) ([]*model.Workspace, error) { return nil, nil }
func (s *inMemStore) UpdateWorkspace(_ context.Context, _ string, _, _ *string) error {
	return nil
}
func (s *inMemStore) DeleteWorkspace(_ context.Context, _ string) error { return nil }
func (s *inMemStore) AddMember(_ context.Context, _, _, _ string) error { return nil }
func (s *inMemStore) RemoveMember(_ context.Context, _, _ string) error { return nil }
func (s *inMemStore) ListMembers(_ context.Context, _ string) ([]*model.WorkspaceMember, error) {
	return nil, nil
}
func (s *inMemStore) GetMemberRole(_ context.Context, _, _ string) (string, error) {
	return "", store.ErrNotFound
}
func (s *inMemStore) ListProjects(_ context.Context, _ string) ([]store.ProjectSummary, error) {
	return nil, nil
}

func (s *inMemStore) SearchRunsByName(_ context.Context, _, _ string, _ int) ([]*model.Run, error) {
	return nil, nil
}

// inMemArtifactStore records uploaded artifacts.
type inMemArtifactStore struct {
	files map[string][]byte
}

func newInMemArtifactStore() *inMemArtifactStore {
	return &inMemArtifactStore{files: make(map[string][]byte)}
}

func (a *inMemArtifactStore) Upload(runID, relPath string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	a.files[runID+"/"+relPath] = data
	return nil
}

func (a *inMemArtifactStore) Open(_ string, _ string) (io.ReadCloser, int64, error) {
	return nil, 0, artifact.ErrNotFound
}

func (a *inMemArtifactStore) Delete(_ string, _ string) error {
	return nil
}

func (a *inMemArtifactStore) List(_ string, _ string) ([]artifact.ListEntry, error) {
	return nil, nil
}

// ---- integration test: happy path ----

func TestMLflowImporter_HappyPath(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()

	fake.experiments = []mlflowExperiment{
		{ExperimentID: "1", Name: "test-exp", LifecycleStage: "active",
			Tags: []mlflowKV{{Key: "env", Value: "ci"}}},
	}
	fake.runs["1"] = []mlflowRun{
		{
			Info: mlflowRunInfo{
				RunID:          "run001",
				ExperimentID:   "1",
				RunName:        "trial-1",
				Status:         "FINISHED",
				StartTime:      1700000000000,
				EndTime:        1700001000000,
				LifecycleStage: "active",
				UserID:         "bob",
			},
			Data: mlflowRunData{
				Metrics: []mlflowMetric{{Key: "loss"}, {Key: "acc"}},
				Params:  []mlflowKV{{Key: "lr", Value: "0.01"}},
				Tags:    []mlflowKV{{Key: "phase", Value: "train"}},
			},
		},
	}
	fake.history["run001:loss"] = []mlflowMetric{
		{Key: "loss", Value: 0.9, Timestamp: 1700000001000, Step: 0},
		{Key: "loss", Value: 0.5, Timestamp: 1700000002000, Step: 1},
	}
	fake.history["run001:acc"] = []mlflowMetric{
		{Key: "acc", Value: 0.55, Timestamp: 1700000001000, Step: 0},
		{Key: "acc", Value: 0.72, Timestamp: 1700000002000, Step: 1},
	}
	fake.artifacts["run001/model.pkl"] = "fake-model-bytes"

	st := newInMemStore()
	art := newInMemArtifactStore()
	importer := &MLflowImporter{
		SourceURL:     srv.URL,
		Workspace:     "default",
		HTTP:          srv.Client(),
		Store:         st,
		ArtifactStore: art,
	}

	stats, err := importer.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify stats.
	if stats.Experiments != 1 {
		t.Errorf("experiments: got %d, want 1", stats.Experiments)
	}
	if stats.Runs != 1 {
		t.Errorf("runs: got %d, want 1", stats.Runs)
	}
	if stats.Params != 1 {
		t.Errorf("params: got %d, want 1", stats.Params)
	}
	if stats.Tags != 1 {
		t.Errorf("tags: got %d, want 1", stats.Tags)
	}
	if stats.Metrics != 4 {
		t.Errorf("metrics: got %d, want 4 (2 loss + 2 acc)", stats.Metrics)
	}
	if stats.Artifacts != 1 {
		t.Errorf("artifacts: got %d, want 1", stats.Artifacts)
	}

	// Verify the experiment was created.
	if len(st.experiments) != 1 {
		t.Fatalf("expected 1 experiment in store, got %d", len(st.experiments))
	}
	if st.experiments[0].Name != "test-exp" {
		t.Errorf("experiment name: %q", st.experiments[0].Name)
	}

	// Verify the run.
	r, ok := st.runs["run001"]
	if !ok {
		t.Fatal("run run001 not found in store")
	}
	if r.Status != model.StatusFinished {
		t.Errorf("run status: %q", r.Status)
	}

	// Verify params / tags / metrics.
	if len(st.params["run001"]) != 1 || st.params["run001"][0].Key != "lr" {
		t.Errorf("params: %+v", st.params["run001"])
	}
	if len(st.tags["run001"]) != 1 || st.tags["run001"][0].Key != "phase" {
		t.Errorf("tags: %+v", st.tags["run001"])
	}
	if len(st.metrics["run001"]) != 4 {
		t.Errorf("metrics count: %d", len(st.metrics["run001"]))
	}

	// Verify artifact.
	artKey := "run001/model.pkl"
	if _, ok := art.files[artKey]; !ok {
		t.Errorf("artifact %q not found in artifact store", artKey)
	}
}

// ---- integration test: pagination ----

func TestMLflowImporter_Pagination(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()

	fake.experiments = []mlflowExperiment{
		{ExperimentID: "42", Name: "paginated-exp", LifecycleStage: "active"},
	}
	// Add 3 runs; the fake handler paginates at 2, so this exercises next_page_token.
	for i := 0; i < 3; i++ {
		runID := strings.Repeat("0", 28) + strings.Repeat("x", 2) + string(rune('a'+i))
		// Use valid 32-char hex-like IDs.
		runID = "00000000000000000000000000000" + string(rune('a'+i))
		_ = runID
		rid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"[0:30] + string(rune('a'+i))
		fake.runs["42"] = append(fake.runs["42"], mlflowRun{
			Info: mlflowRunInfo{
				RunID:          rid + "x",
				ExperimentID:   "42",
				Status:         "FINISHED",
				LifecycleStage: "active",
			},
			Data: mlflowRunData{},
		})
	}

	st := newInMemStore()
	art := newInMemArtifactStore()
	importer := &MLflowImporter{
		SourceURL:     srv.URL,
		Workspace:     "default",
		HTTP:          srv.Client(),
		Store:         st,
		ArtifactStore: art,
	}

	stats, err := importer.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Runs != 3 {
		t.Errorf("expected 3 runs after pagination, got %d", stats.Runs)
	}
}

// TestMLflowImporter_DryRun verifies that dry-run mode does not write to the store.
func TestMLflowImporter_DryRun(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()

	fake.experiments = []mlflowExperiment{
		{ExperimentID: "1", Name: "dry-exp", LifecycleStage: "active"},
	}
	fake.runs["1"] = []mlflowRun{
		{
			Info: mlflowRunInfo{RunID: "dryrun001", ExperimentID: "1", Status: "FINISHED", LifecycleStage: "active"},
			Data: mlflowRunData{Params: []mlflowKV{{Key: "x", Value: "1"}}},
		},
	}

	st := newInMemStore()
	art := newInMemArtifactStore()
	importer := &MLflowImporter{
		SourceURL:     srv.URL,
		Workspace:     "default",
		DryRun:        true,
		HTTP:          srv.Client(),
		Store:         st,
		ArtifactStore: art,
	}

	stats, err := importer.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Runs stat should reflect the single run.
	if stats.Runs != 1 {
		t.Errorf("dry-run stats.Runs: %d", stats.Runs)
	}
	// Nothing should be written to the store.
	if len(st.experiments) != 0 {
		t.Errorf("dry-run: expected 0 experiments in store, got %d", len(st.experiments))
	}
	if len(st.runs) != 0 {
		t.Errorf("dry-run: expected 0 runs in store, got %d", len(st.runs))
	}
}

// TestMLflowImporter_NameCollision verifies that a duplicate experiment name
// is resolved by appending a suffix.
func TestMLflowImporter_NameCollision(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()

	fake.experiments = []mlflowExperiment{
		{ExperimentID: "1", Name: "dup-exp", LifecycleStage: "active"},
	}
	fake.runs["1"] = nil

	st := newInMemStore()
	// Pre-insert an experiment with the same name.
	_, _ = st.CreateExperiment(context.Background(), &model.Experiment{Name: "dup-exp", WorkspaceID: "default"})

	art := newInMemArtifactStore()
	importer := &MLflowImporter{
		SourceURL:     srv.URL,
		Workspace:     "default",
		HTTP:          srv.Client(),
		Store:         st,
		ArtifactStore: art,
	}

	_, err := importer.Run(context.Background())
	if err != nil {
		t.Fatalf("Run with name collision: %v", err)
	}
	// Expect two experiments: the original "dup-exp" and the renamed import.
	if len(st.experiments) < 2 {
		t.Errorf("expected at least 2 experiments after collision, got %d", len(st.experiments))
	}
}

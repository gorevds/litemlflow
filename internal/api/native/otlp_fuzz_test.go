package native_test

// Fuzz test for the IngestOTLP HTTP handler.
//
// IngestOTLP accepts OTLP/JSON. The fuzz test feeds arbitrary JSON bodies and
// verifies that the handler never panics. Errors (4xx, 5xx) are acceptable.
//
// Run as regular unit test (seed corpus only):
//
//	go test -count=1 ./internal/api/native/
//
// Run with fuzzing:
//
//	go test -fuzz=FuzzIngestOTLP -fuzztime=60s ./internal/api/native/
//
// See docs/contributing-fuzz.md for extended guidance.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/litemlflow/litemlflow/internal/api/native"
	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
)

// --- minimal in-memory store stub ---

// stubStore satisfies the store.Store interface with in-memory state, just
// enough for the OTLP fuzz target (InsertSpans + SearchExperiments used by
// Readyz; all other methods are stubs that return ErrNotFound).
type stubStore struct {
	spans []model.Span
}

func (s *stubStore) Migrate(_ context.Context) error { return nil }
func (s *stubStore) Close() error                    { return nil }

func (s *stubStore) InsertSpans(_ context.Context, spans []model.Span) error {
	s.spans = append(s.spans, spans...)
	return nil
}

func (s *stubStore) GetSpansByRun(_ context.Context, _ string) ([]model.Span, error) {
	return nil, store.ErrNotFound
}

func (s *stubStore) GetSpansByTrace(_ context.Context, _ string) ([]model.Span, error) {
	return nil, store.ErrNotFound
}

func (s *stubStore) SearchExperiments(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Experiment], error) {
	return store.SearchResult[*model.Experiment]{}, nil
}

// Stub out all the remaining interface methods. They're not exercised by
// IngestOTLP but the interface requires them.

func (s *stubStore) CreateExperiment(_ context.Context, _ *model.Experiment) (int64, error) {
	return 0, store.ErrNotFound
}
func (s *stubStore) GetExperiment(_ context.Context, _ int64) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetExperimentByName(_ context.Context, _ string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetExperimentByNameInWorkspace(_ context.Context, _, _ string) (*model.Experiment, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) UpdateExperiment(_ context.Context, _ int64, _ *string) error {
	return store.ErrNotFound
}
func (s *stubStore) SetExperimentLifecycle(_ context.Context, _ int64, _ string) error {
	return store.ErrNotFound
}
func (s *stubStore) SetExperimentTag(_ context.Context, _ int64, _, _ string) error {
	return store.ErrNotFound
}
func (s *stubStore) CreateRun(_ context.Context, _ *model.Run) error { return store.ErrNotFound }
func (s *stubStore) GetRun(_ context.Context, _ string) (*model.Run, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) UpdateRun(_ context.Context, _ string, _ *string, _ *int64, _ *string) error {
	return store.ErrNotFound
}
func (s *stubStore) SetRunLifecycle(_ context.Context, _, _ string) error { return store.ErrNotFound }
func (s *stubStore) SearchRuns(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Run], error) {
	return store.SearchResult[*model.Run]{}, nil
}
func (s *stubStore) LogMetric(_ context.Context, _ string, _ model.Metric) error {
	return store.ErrNotFound
}
func (s *stubStore) LogMetrics(_ context.Context, _ string, _ []model.Metric) error {
	return store.ErrNotFound
}
func (s *stubStore) LogParam(_ context.Context, _ string, _ model.Param) error {
	return store.ErrNotFound
}
func (s *stubStore) LogParams(_ context.Context, _ string, _ []model.Param) error {
	return store.ErrNotFound
}
func (s *stubStore) SetTag(_ context.Context, _ string, _ model.KV) error { return store.ErrNotFound }
func (s *stubStore) SetTags(_ context.Context, _ string, _ []model.KV) error {
	return store.ErrNotFound
}
func (s *stubStore) DeleteTag(_ context.Context, _, _ string) error { return store.ErrNotFound }
func (s *stubStore) GetMetricHistory(_ context.Context, _, _ string, _ store.MetricHistoryOptions) ([]model.Metric, string, error) {
	return nil, "", store.ErrNotFound
}
func (s *stubStore) GetMetricHistoryDownsampled(_ context.Context, _, _ string, _ int) ([]model.Metric, int64, error) {
	return nil, 0, store.ErrNotFound
}
func (s *stubStore) GetParams(_ context.Context, _ string) ([]model.Param, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetTags(_ context.Context, _ string) ([]model.KV, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetLatestMetrics(_ context.Context, _ string) ([]model.Metric, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) LogInputs(_ context.Context, _ string, _ []model.DatasetInput) error {
	return store.ErrNotFound
}
func (s *stubStore) GetRunDatasets(_ context.Context, _ string) ([]model.DatasetInput, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) CreatePrompt(_ context.Context, _ *model.Prompt) (int64, error) {
	return 0, store.ErrNotFound
}
func (s *stubStore) GetLatestPrompt(_ context.Context, _ string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetPromptVersion(_ context.Context, _ string, _ int64) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) ListPromptVersions(_ context.Context, _ string) ([]*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) SetPromptAlias(_ context.Context, _, _ string, _ int64) error {
	return store.ErrNotFound
}
func (s *stubStore) GetPromptByAlias(_ context.Context, _, _ string) (*model.Prompt, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) CreateEval(_ context.Context, _ *model.Eval) error { return store.ErrNotFound }
func (s *stubStore) GetEval(_ context.Context, _ string) (*model.Eval, error) {
	return nil, store.ErrNotFound
}

// Model registry stubs.
func (s *stubStore) CreateRegisteredModel(_ context.Context, _ *model.RegisteredModel) error {
	return store.ErrNotFound
}
func (s *stubStore) GetRegisteredModel(_ context.Context, _ string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) RenameRegisteredModel(_ context.Context, _, _ string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) UpdateRegisteredModel(_ context.Context, _ string, _ *string) (*model.RegisteredModel, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) DeleteRegisteredModel(_ context.Context, _ string) error { return store.ErrNotFound }
func (s *stubStore) SearchRegisteredModels(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.RegisteredModel], error) {
	return store.SearchResult[*model.RegisteredModel]{}, nil
}
func (s *stubStore) GetLatestModelVersions(_ context.Context, _ string, _ []string) ([]*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) SetRegisteredModelTag(_ context.Context, _, _, _ string) error {
	return store.ErrNotFound
}
func (s *stubStore) DeleteRegisteredModelTag(_ context.Context, _, _ string) error {
	return store.ErrNotFound
}
func (s *stubStore) SetModelAlias(_ context.Context, _, _ string, _ int64) error {
	return store.ErrNotFound
}
func (s *stubStore) DeleteModelAlias(_ context.Context, _, _ string) error { return store.ErrNotFound }
func (s *stubStore) GetModelByAlias(_ context.Context, _, _ string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) CreateModelVersion(_ context.Context, _ *model.ModelVersion) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetModelVersion(_ context.Context, _ string, _ int64) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) UpdateModelVersion(_ context.Context, _ string, _ int64, _ *string) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) DeleteModelVersion(_ context.Context, _ string, _ int64) error {
	return store.ErrNotFound
}
func (s *stubStore) SearchModelVersions(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.ModelVersion], error) {
	return store.SearchResult[*model.ModelVersion]{}, nil
}
func (s *stubStore) TransitionModelStage(_ context.Context, _ string, _ int64, _ string, _ bool) (*model.ModelVersion, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) SetModelVersionTag(_ context.Context, _ string, _ int64, _, _ string) error {
	return store.ErrNotFound
}
func (s *stubStore) DeleteModelVersionTag(_ context.Context, _ string, _ int64, _ string) error {
	return store.ErrNotFound
}

// Workspace stubs.
func (s *stubStore) CreateWorkspace(_ context.Context, _ *model.Workspace) error {
	return store.ErrNotFound
}
func (s *stubStore) GetWorkspace(_ context.Context, _ string) (*model.Workspace, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) ListWorkspaces(_ context.Context) ([]*model.Workspace, error) { return nil, nil }
func (s *stubStore) UpdateWorkspace(_ context.Context, _ string, _ *string, _ *string) error {
	return store.ErrNotFound
}
func (s *stubStore) DeleteWorkspace(_ context.Context, _ string) error { return store.ErrNotFound }
func (s *stubStore) AddMember(_ context.Context, _, _, _ string) error { return store.ErrNotFound }
func (s *stubStore) RemoveMember(_ context.Context, _, _ string) error { return store.ErrNotFound }
func (s *stubStore) ListMembers(_ context.Context, _ string) ([]*model.WorkspaceMember, error) {
	return nil, store.ErrNotFound
}
func (s *stubStore) GetMemberRole(_ context.Context, _, _ string) (string, error) {
	return "", store.ErrNotFound
}

// --- fuzz setup ---

func newTestHandler() *native.Handler {
	return &native.Handler{Store: &stubStore{}}
}

func newTestRouter(h *native.Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

// validOTLPBody returns a well-formed minimal OTLP/JSON request body.
func validOTLPBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{
							"key":   "service.name",
							"value": map[string]any{"stringValue": "my-svc"},
						},
					},
				},
				"scopeSpans": []map[string]any{
					{
						"spans": []map[string]any{
							{
								"traceId":            "abc123",
								"spanId":             "span001",
								"name":               "test-span",
								"kind":               2,
								"startTimeUnixNano":  "1000000000",
								"endTimeUnixNano":    "2000000000",
								"attributes":         []map[string]any{},
								"status":             map[string]any{"code": 1, "message": "OK"},
							},
						},
					},
				},
			},
		},
	})
	return body
}

// FuzzIngestOTLP exercises the IngestOTLP handler with arbitrary JSON bodies.
//
// Oracles:
//  1. The handler must never panic.
//  2. The response status must be a valid HTTP status code (200–599).
//  3. The response body must be valid JSON (the handler always writes JSON).
func FuzzIngestOTLP(f *testing.F) {
	// Seed corpus: valid, partially valid, and adversarial inputs.
	f.Add(validOTLPBody())
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"resourceSpans":[]}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{}]}]}]}`))

	// Oversized array.
	bigSpans := make([]map[string]any, 2000)
	for i := range bigSpans {
		bigSpans[i] = map[string]any{
			"traceId":           fmt.Sprintf("trace%d", i),
			"spanId":            fmt.Sprintf("span%d", i),
			"name":              "s",
			"startTimeUnixNano": "1000",
			"endTimeUnixNano":   "2000",
		}
	}
	bigBody, _ := json.Marshal(map[string]any{
		"resourceSpans": []map[string]any{
			{"scopeSpans": []map[string]any{{"spans": bigSpans}}},
		},
	})
	f.Add(bigBody)

	// Deeply nested structure.
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"k","value":{"stringValue":` +
		strings.Repeat(`{"nested":`, 30) + `"deep"` + strings.Repeat(`}`, 30) + `}}]},"scopeSpans":[]}]}`))

	// Wrong field types.
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"startTimeUnixNano":true,"endTimeUnixNano":null}]}]}]}`))
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"kind":"not-a-number"}]}]}]}`))

	// SQL injection attempt in span attributes.
	f.Add([]byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"litemlflow.run_id","value":{"stringValue":"' OR 1=1 --"}}]},"scopeSpans":[{"spans":[{"traceId":"t1","spanId":"s1","name":"n","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`))

	// Zero-length body.
	f.Add([]byte(``))

	// Timestamps at extreme values.
	f.Add([]byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"t","spanId":"s","name":"n",` +
		`"startTimeUnixNano":"9999999999999999999","endTimeUnixNano":"-9999999999999999999"}]}]}]}`))

	h := newTestHandler()
	router := newTestRouter(h)

	f.Fuzz(func(t *testing.T, body []byte) {
		// Only fuzz valid UTF-8 JSON-like inputs; binary bodies are not
		// something OTLP/JSON clients send.
		if !utf8.Valid(body) {
			t.Skip("non-UTF-8 body")
		}

		// Oracle 1: handler must not panic.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("IngestOTLP panicked on body (len=%d): %v", len(body), r)
			}
		}()

		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		// Oracle 2: HTTP status must be in a valid range.
		status := w.Code
		if status < 200 || status > 599 {
			t.Errorf("unexpected HTTP status %d", status)
		}

		// Oracle 3: response body must be valid JSON (or empty for 204/etc).
		respBody := w.Body.Bytes()
		if len(respBody) > 0 {
			if !json.Valid(respBody) {
				t.Errorf("response body is not valid JSON (status=%d): %q", status, respBody)
			}
		}
	})
}

// FuzzIngestTraces exercises the IngestTraces handler (POST /api/v1/traces).
// This endpoint uses a different JSON schema from OTLP.
func FuzzIngestTraces(f *testing.F) {
	validTrace, _ := json.Marshal(map[string]any{
		"trace_id": "abc123",
		"spans": []map[string]any{
			{
				"id":            "span001",
				"trace_id":      "abc123",
				"name":          "root",
				"start_time_ns": 1000000000,
				"end_time_ns":   2000000000,
			},
		},
	})
	f.Add(validTrace)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"spans":[]}`))
	f.Add([]byte(`{"spans":null}`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"trace_id":"t","spans":[{"name":"s","start_time_ns":"not-a-number"}]}`))
	f.Add([]byte(`{"spans":[{"start_time_ns":-9999999999999999999,"name":"x"}]}`))

	// Injection attempt.
	f.Add([]byte(`{"trace_id":"' OR 1=1","spans":[{"name":"x","start_time_ns":1}]}`))

	h := newTestHandler()
	router := newTestRouter(h)

	f.Fuzz(func(t *testing.T, body []byte) {
		if !utf8.Valid(body) {
			t.Skip("non-UTF-8 body")
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("IngestTraces panicked on body (len=%d): %v", len(body), r)
			}
		}()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code < 200 || w.Code > 599 {
			t.Errorf("unexpected HTTP status %d", w.Code)
		}
		if rb := w.Body.Bytes(); len(rb) > 0 && !json.Valid(rb) {
			t.Errorf("response body is not valid JSON (status=%d): %q", w.Code, rb)
		}
	})
}

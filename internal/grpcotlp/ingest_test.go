package grpcotlp_test

import (
	"context"
	"encoding/hex"
	"net"
	"sync"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	respb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/gorevds/litemlflow/internal/grpcotlp"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// ---- fake store -------------------------------------------------------------

// fakeStore implements store.Store, recording InsertSpans calls.
// All other methods panic (they should never be called by grpcotlp).
type fakeStore struct {
	nopStore
	mu    sync.Mutex
	spans []model.Span
}

func (f *fakeStore) InsertSpans(_ context.Context, spans []model.Span) error {
	f.mu.Lock()
	f.spans = append(f.spans, spans...)
	f.mu.Unlock()
	return nil
}

func (f *fakeStore) recorded() []model.Span {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Span, len(f.spans))
	copy(out, f.spans)
	return out
}

// nopStore satisfies the store.Store interface with no-ops / panics so that
// fakeStore only needs to override the methods that grpcotlp actually calls.
type nopStore struct{}

func (nopStore) Migrate(_ context.Context) error                                { return nil }
func (nopStore) Close() error                                                   { return nil }
func (nopStore) InsertSpans(_ context.Context, _ []model.Span) error           { panic("not impl") }
func (nopStore) GetSpansByRun(_ context.Context, _ string) ([]model.Span, error) {
	return nil, nil
}
func (nopStore) GetSpansByTrace(_ context.Context, _ string) ([]model.Span, error) {
	return nil, nil
}

// Satisfy the remaining store.Store methods with panics (never called in tests).
func (nopStore) CreateExperiment(_ context.Context, _ *model.Experiment) (int64, error) {
	panic("not impl")
}
func (nopStore) GetExperiment(_ context.Context, _ int64) (*model.Experiment, error) {
	panic("not impl")
}
func (nopStore) GetExperimentByName(_ context.Context, _ string) (*model.Experiment, error) {
	panic("not impl")
}
func (nopStore) UpdateExperiment(_ context.Context, _ int64, _ *string) error { panic("not impl") }
func (nopStore) SetExperimentLifecycle(_ context.Context, _ int64, _ string) error {
	panic("not impl")
}
func (nopStore) SearchExperiments(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Experiment], error) {
	panic("not impl")
}
func (nopStore) SetExperimentTag(_ context.Context, _ int64, _, _ string) error { panic("not impl") }
func (nopStore) CreateRun(_ context.Context, _ *model.Run) error                { panic("not impl") }
func (nopStore) GetRun(_ context.Context, _ string) (*model.Run, error)         { panic("not impl") }
func (nopStore) UpdateRun(_ context.Context, _ string, _ *string, _ *int64, _ *string) error {
	panic("not impl")
}
func (nopStore) SetRunLifecycle(_ context.Context, _ string, _ string) error { panic("not impl") }
func (nopStore) SearchRuns(_ context.Context, _ store.SearchOptions) (store.SearchResult[*model.Run], error) {
	panic("not impl")
}
func (nopStore) LogMetric(_ context.Context, _ string, _ model.Metric) error { panic("not impl") }
func (nopStore) LogMetrics(_ context.Context, _ string, _ []model.Metric) error {
	panic("not impl")
}
func (nopStore) LogParam(_ context.Context, _ string, _ model.Param) error { panic("not impl") }
func (nopStore) LogParams(_ context.Context, _ string, _ []model.Param) error {
	panic("not impl")
}
func (nopStore) SetTag(_ context.Context, _ string, _ model.KV) error { panic("not impl") }
func (nopStore) SetTags(_ context.Context, _ string, _ []model.KV) error {
	panic("not impl")
}
func (nopStore) DeleteTag(_ context.Context, _, _ string) error { panic("not impl") }
func (nopStore) GetMetricHistory(_ context.Context, _, _ string, _ store.MetricHistoryOptions) ([]model.Metric, string, error) {
	panic("not impl")
}
func (nopStore) GetMetricHistoryDownsampled(_ context.Context, _, _ string, _ int) ([]model.Metric, int64, error) {
	panic("not impl")
}
func (nopStore) GetParams(_ context.Context, _ string) ([]model.Param, error) { panic("not impl") }
func (nopStore) GetTags(_ context.Context, _ string) ([]model.KV, error)      { panic("not impl") }
func (nopStore) GetLatestMetrics(_ context.Context, _ string) ([]model.Metric, error) {
	panic("not impl")
}
func (nopStore) LogInputs(_ context.Context, _ string, _ []model.DatasetInput) error {
	panic("not impl")
}
func (nopStore) GetRunDatasets(_ context.Context, _ string) ([]model.DatasetInput, error) {
	panic("not impl")
}
func (nopStore) CreatePrompt(_ context.Context, _ *model.Prompt) (int64, error) { panic("not impl") }
func (nopStore) GetLatestPrompt(_ context.Context, _ string) (*model.Prompt, error) {
	panic("not impl")
}
func (nopStore) GetPromptVersion(_ context.Context, _ string, _ int64) (*model.Prompt, error) {
	panic("not impl")
}
func (nopStore) ListPromptVersions(_ context.Context, _ string) ([]*model.Prompt, error) {
	panic("not impl")
}
func (nopStore) SetPromptAlias(_ context.Context, _, _ string, _ int64) error { panic("not impl") }
func (nopStore) GetPromptByAlias(_ context.Context, _, _ string) (*model.Prompt, error) {
	panic("not impl")
}
func (nopStore) CreateEval(_ context.Context, _ *model.Eval) error            { panic("not impl") }
func (nopStore) GetEval(_ context.Context, _ string) (*model.Eval, error)     { panic("not impl") }
func (nopStore) CreateRegisteredModel(_ context.Context, _ *model.RegisteredModel) error {
	panic("not impl")
}
func (nopStore) GetRegisteredModel(_ context.Context, _ string) (*model.RegisteredModel, error) {
	panic("not impl")
}
func (nopStore) RenameRegisteredModel(_ context.Context, _, _ string) (*model.RegisteredModel, error) {
	panic("not impl")
}
func (nopStore) UpdateRegisteredModel(_ context.Context, _ string, _ *string) (*model.RegisteredModel, error) {
	panic("not impl")
}
func (nopStore) DeleteRegisteredModel(_ context.Context, _ string) error { panic("not impl") }
func (nopStore) SearchRegisteredModels(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.RegisteredModel], error) {
	panic("not impl")
}
func (nopStore) GetLatestModelVersions(_ context.Context, _ string, _ []string) ([]*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) SetRegisteredModelTag(_ context.Context, _, _, _ string) error { panic("not impl") }
func (nopStore) DeleteRegisteredModelTag(_ context.Context, _, _ string) error { panic("not impl") }
func (nopStore) SetModelAlias(_ context.Context, _, _ string, _ int64) error   { panic("not impl") }
func (nopStore) DeleteModelAlias(_ context.Context, _, _ string) error         { panic("not impl") }
func (nopStore) GetModelByAlias(_ context.Context, _, _ string) (*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) CreateModelVersion(_ context.Context, _ *model.ModelVersion) (*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) GetModelVersion(_ context.Context, _ string, _ int64) (*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) UpdateModelVersion(_ context.Context, _ string, _ int64, _ *string) (*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) DeleteModelVersion(_ context.Context, _ string, _ int64) error { panic("not impl") }
func (nopStore) SearchModelVersions(_ context.Context, _ string, _ int, _ string) (store.SearchResult[*model.ModelVersion], error) {
	panic("not impl")
}
func (nopStore) TransitionModelStage(_ context.Context, _ string, _ int64, _ string, _ bool) (*model.ModelVersion, error) {
	panic("not impl")
}
func (nopStore) SetModelVersionTag(_ context.Context, _ string, _ int64, _, _ string) error {
	panic("not impl")
}
func (nopStore) DeleteModelVersionTag(_ context.Context, _ string, _ int64, _ string) error {
	panic("not impl")
}
func (nopStore) CreateWorkspace(_ context.Context, _ *model.Workspace) error { panic("not impl") }
func (nopStore) GetWorkspace(_ context.Context, _ string) (*model.Workspace, error) {
	panic("not impl")
}
func (nopStore) ListWorkspaces(_ context.Context) ([]*model.Workspace, error) { panic("not impl") }
func (nopStore) UpdateWorkspace(_ context.Context, _ string, _ *string, _ *string) error {
	panic("not impl")
}
func (nopStore) DeleteWorkspace(_ context.Context, _ string) error               { panic("not impl") }
func (nopStore) AddMember(_ context.Context, _, _, _ string) error               { panic("not impl") }
func (nopStore) RemoveMember(_ context.Context, _, _ string) error               { panic("not impl") }
func (nopStore) ListMembers(_ context.Context, _ string) ([]*model.WorkspaceMember, error) {
	panic("not impl")
}
func (nopStore) GetMemberRole(_ context.Context, _, _ string) (string, error) { panic("not impl") }
func (nopStore) GetExperimentByNameInWorkspace(_ context.Context, _, _ string) (*model.Experiment, error) {
	panic("not impl")
}
func (nopStore) ListProjects(_ context.Context, _ string) ([]store.ProjectSummary, error) {
	return nil, nil
}

// ---- in-memory gRPC server setup -------------------------------------------

// newBufconnServer starts a grpcotlp.Server over an in-memory bufconn listener
// and returns a connected client plus a stop function.
func newBufconnServer(t *testing.T, fs *fakeStore) (coltracepb.TraceServiceClient, func()) {
	t.Helper()

	const bufSize = 1 << 20 // 1 MiB
	lis := bufconn.Listen(bufSize)

	srv, err := grpcotlp.NewWithListener(lis, fs)
	if err != nil {
		t.Fatalf("grpcotlp.NewWithListener: %v", err)
	}

	go func() { _ = srv.ServeListener() }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	client := coltracepb.NewTraceServiceClient(conn)
	stop := func() {
		_ = conn.Close()
		srv.Stop()
	}
	return client, stop
}

// ---- tests ------------------------------------------------------------------

// TestGRPCOTLPExportOneSpan verifies that a single resource span is ingested
// and results in a model.Span in the store with the correct field mapping.
func TestGRPCOTLPExportOneSpan(t *testing.T) {
	t.Parallel()

	fs := &fakeStore{}
	client, stop := newBufconnServer(t, fs)
	defer stop()

	traceIDBytes, _ := hex.DecodeString("0af7651916cd43dd8448eb211c80319c")
	spanIDBytes, _ := hex.DecodeString("b7ad6b7169203331")

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &respb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "litemlflow.run_id", Value: &commonpb.AnyValue{
							Value: &commonpb.AnyValue_StringValue{StringValue: "run-abc"},
						}},
						{Key: "service.name", Value: &commonpb.AnyValue{
							Value: &commonpb.AnyValue_StringValue{StringValue: "my-service"},
						}},
					},
				},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           traceIDBytes,
								SpanId:            spanIDBytes,
								Name:              "inference",
								Kind:              tracepb.Span_SPAN_KIND_SERVER,
								StartTimeUnixNano: 1_000_000_000,
								EndTimeUnixNano:   2_000_000_000,
								Status: &tracepb.Status{
									Code:    tracepb.Status_STATUS_CODE_OK,
									Message: "",
								},
								Attributes: []*commonpb.KeyValue{
									{Key: "tokens.input", Value: &commonpb.AnyValue{
										Value: &commonpb.AnyValue_IntValue{IntValue: 120},
									}},
								},
							},
						},
					},
				},
			},
		},
	}

	resp, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Verify partial_success.rejected_spans == 0.
	if resp.GetPartialSuccess().GetRejectedSpans() != 0 {
		t.Errorf("rejected_spans: want 0, got %d", resp.GetPartialSuccess().GetRejectedSpans())
	}

	spans := fs.recorded()
	if len(spans) != 1 {
		t.Fatalf("want 1 span recorded, got %d", len(spans))
	}
	sp := spans[0]

	if sp.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("TraceID: got %q", sp.TraceID)
	}
	if sp.ID != "b7ad6b7169203331" {
		t.Errorf("SpanID: got %q", sp.ID)
	}
	if sp.RunID != "run-abc" {
		t.Errorf("RunID: got %q", sp.RunID)
	}
	if sp.Name != "inference" {
		t.Errorf("Name: got %q", sp.Name)
	}
	if sp.Kind != "SERVER" {
		t.Errorf("Kind: got %q", sp.Kind)
	}
	if sp.StatusCode != "OK" {
		t.Errorf("StatusCode: got %q", sp.StatusCode)
	}
	if sp.StartTimeNS != 1_000_000_000 {
		t.Errorf("StartTimeNS: got %d", sp.StartTimeNS)
	}
	if sp.EndTimeNS == nil || *sp.EndTimeNS != 2_000_000_000 {
		t.Errorf("EndTimeNS: got %v", sp.EndTimeNS)
	}
}

// TestGRPCOTLPPartialSuccessZeroRejected verifies that the response always
// reports 0 rejected spans even for minimal / empty requests.
func TestGRPCOTLPPartialSuccessZeroRejected(t *testing.T) {
	t.Parallel()

	fs := &fakeStore{}
	client, stop := newBufconnServer(t, fs)
	defer stop()

	resp, err := client.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{})
	if err != nil {
		t.Fatalf("Export empty request: %v", err)
	}
	if resp.GetPartialSuccess().GetRejectedSpans() != 0 {
		t.Errorf("want rejected_spans=0, got %d", resp.GetPartialSuccess().GetRejectedSpans())
	}
}

// TestGRPCOTLPMalformedTraceID verifies that a span with a non-standard-length
// trace_id is still ingested (best-effort, same as the HTTP path). The ID is
// hex-encoded verbatim; rejected_spans remains 0.
func TestGRPCOTLPMalformedTraceID(t *testing.T) {
	t.Parallel()

	fs := &fakeStore{}
	client, stop := newBufconnServer(t, fs)
	defer stop()

	// 6 bytes — wrong length for a standard OTLP trace ID (should be 16).
	malformedID := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe}
	spanIDBytes, _ := hex.DecodeString("1234567890abcdef")

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId:           malformedID,
								SpanId:            spanIDBytes,
								Name:              "weird-span",
								StartTimeUnixNano: 500,
							},
						},
					},
				},
			},
		},
	}

	resp, err := client.Export(context.Background(), req)
	if err != nil {
		t.Fatalf("Export malformed trace_id: %v", err)
	}
	if resp.GetPartialSuccess().GetRejectedSpans() != 0 {
		t.Errorf("want rejected_spans=0, got %d", resp.GetPartialSuccess().GetRejectedSpans())
	}

	spans := fs.recorded()
	if len(spans) != 1 {
		t.Fatalf("want 1 span recorded (best-effort), got %d", len(spans))
	}
	// The TraceID should be the hex encoding of the malformed bytes.
	wantTraceID := hex.EncodeToString(malformedID)
	if spans[0].TraceID != wantTraceID {
		t.Errorf("TraceID: want %q (verbatim hex), got %q", wantTraceID, spans[0].TraceID)
	}
}

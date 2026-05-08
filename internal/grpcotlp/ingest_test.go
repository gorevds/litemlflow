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
	"github.com/gorevds/litemlflow/internal/store/storetest"
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

// nopStore is a type alias to storetest.NopStore; concrete tests embed it
// (or fakeStore embeds it) and override the subset of methods they need.
// All non-overridden methods are no-op / panic — see storetest/nopstore.go.
type nopStore = storetest.NopStore

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

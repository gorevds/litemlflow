package grpcotlp_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	respb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/gorevds/litemlflow/internal/grpcotlp"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// startTCP runs a receiver on a real loopback listener.
func startTCP(t *testing.T, fs *fakeStore, opts ...grpcotlp.Option) coltracepb.TraceServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := grpcotlp.NewWithListener(lis, fs, opts...)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener() }()
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop() })
	return coltracepb.NewTraceServiceClient(conn)
}

func oneSpanReq(name string) *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: make([]byte, 16), SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: name, StartTimeUnixNano: 1,
		}}}},
	}}}
}

func TestGRPCOTLPAuthenticatorRequired(t *testing.T) {
	t.Parallel()
	fs := &fakeStore{}
	var gotUser string
	client := startTCP(t, fs,
		grpcotlp.WithAuthenticator(func(_ context.Context, a string) (string, error) {
			if a == "Bearer good" {
				return "alice", nil
			}
			return "", errors.New("bad")
		}),
		grpcotlp.WithAuthorizer(func(_ context.Context, ws, user string) error {
			gotUser = user
			if ws != "default" {
				return errors.New("denied")
			}
			return nil
		}),
	)

	_, err := client.Export(context.Background(), oneSpanReq("x"))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no creds: want Unauthenticated, got %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer bad")
	_, err = client.Export(ctx, oneSpanReq("x"))
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("bad creds: want Unauthenticated, got %v", err)
	}
	if n := len(fs.recorded()); n != 0 {
		t.Fatalf("rejected calls must not insert spans, got %d", n)
	}

	ctx = metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer good")
	if _, err := client.Export(ctx, oneSpanReq("x")); err != nil {
		t.Fatalf("good creds: %v", err)
	}
	if gotUser != "alice" {
		t.Errorf("authorizer saw user %q, want alice", gotUser)
	}

	// An unknown workspace is rejected before the authorizer runs.
	ctx = metadata.AppendToOutgoingContext(ctx, "x-workspace", "other")
	_, err = client.Export(ctx, oneSpanReq("x"))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown workspace: want InvalidArgument, got %v", err)
	}
}

func TestGRPCOTLPAuthorizerDenied(t *testing.T) {
	t.Parallel()
	fs := &fakeStore{}
	client := startTCP(t, fs, grpcotlp.WithAuthorizer(func(context.Context, string, string) error {
		return errors.New("nope")
	}))
	_, err := client.Export(context.Background(), oneSpanReq("x"))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

// scopedStore knows workspace "ws-b" containing only run "run-b".
type scopedStore struct{ fakeStore }

func (s *scopedStore) GetWorkspace(_ context.Context, id string) (*model.Workspace, error) {
	if id == "ws-b" {
		return &model.Workspace{ID: id}, nil
	}
	return nil, store.ErrNotFound
}

func (s *scopedStore) GetRunInWorkspace(_ context.Context, id, ws string) (*model.Run, error) {
	if id == "run-b" && ws == "ws-b" {
		return &model.Run{ID: id}, nil
	}
	return nil, store.ErrNotFound
}

func TestGRPCOTLPRunLinkageScoped(t *testing.T) {
	t.Parallel()
	ss := &scopedStore{}
	lis := bufconn.Listen(1 << 20)
	srv, err := grpcotlp.NewWithListener(lis, ss)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener() }()
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop() })
	client := coltracepb.NewTraceServiceClient(conn)

	withRun := func(run string) *coltracepb.ExportTraceServiceRequest {
		r := oneSpanReq("x")
		r.ResourceSpans[0].Resource = &respb.Resource{Attributes: []*commonpb.KeyValue{{
			Key: "litemlflow.run_id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: run}},
		}}}
		return r
	}
	wsB := metadata.AppendToOutgoingContext(context.Background(), "x-workspace", "ws-b")

	// Default workspace (no metadata) cannot link to ws-b's run.
	if _, err := client.Export(context.Background(), withRun("run-b")); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-workspace link: want NotFound, got %v", err)
	}
	// ws-b cannot link to an unknown/foreign run.
	if _, err := client.Export(wsB, withRun("run-a")); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign run: want NotFound, got %v", err)
	}
	if n := len(ss.recorded()); n != 0 {
		t.Fatalf("rejected exports inserted %d spans", n)
	}
	// ws-b linking to its own run succeeds.
	if _, err := client.Export(wsB, withRun("run-b")); err != nil {
		t.Fatalf("own run: %v", err)
	}
	if n := len(ss.recorded()); n != 1 {
		t.Fatalf("want 1 span, got %d", n)
	}
}

func TestGRPCOTLPMaxRecvSize(t *testing.T) {
	t.Parallel()
	fs := &fakeStore{}
	client := startTCP(t, fs)
	// Just over the cap → ResourceExhausted, never reaching the store.
	big := oneSpanReq(strings.Repeat("a", grpcotlp.MaxRecvMsgSize+1))
	_, err := client.Export(context.Background(), big)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized: want ResourceExhausted, got %v", err)
	}
	// Comfortably under the cap succeeds (above gRPC's 4 MiB default).
	ok := oneSpanReq(strings.Repeat("a", 8<<20))
	if _, err := client.Export(context.Background(), ok); err != nil {
		t.Fatalf("8 MiB export: %v", err)
	}
}

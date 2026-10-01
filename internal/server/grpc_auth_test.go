package server

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net"
	"path/filepath"
	"testing"
	"time"

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

	"github.com/gorevds/litemlflow/internal/auth"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/grpcotlp"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func openGRPCTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "grpc.db"), dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func startGRPCOTLP(t *testing.T, cfg config.Config, st *store.SQLiteStore) coltracepb.TraceServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv, err := grpcotlp.NewWithListener(lis, st, grpcOTLPOptions(cfg, st)...)
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
	return coltracepb.NewTraceServiceClient(conn)
}

func otlpReq(runID string) *coltracepb.ExportTraceServiceRequest {
	tid, _ := hex.DecodeString("0af7651916cd43dd8448eb211c80319c")
	sid, _ := hex.DecodeString("b7ad6b7169203331")
	rs := &tracepb.ResourceSpans{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
		TraceId: tid, SpanId: sid, Name: "s", StartTimeUnixNano: 1,
	}}}}}
	if runID != "" {
		rs.Resource = &respb.Resource{Attributes: []*commonpb.KeyValue{{
			Key: "litemlflow.run_id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: runID}},
		}}}
	}
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{rs}}
}

func withMD(kv ...string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(kv...))
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %v, want %v (err=%v)", got, want, err)
	}
}

func TestGRPCOTLPAuthBasicMode(t *testing.T) {
	st := openGRPCTestStore(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Auth: "basic", BasicUser: "alice", BasicPassHash: hash}
	client := startGRPCOTLP(t, cfg, st)

	// No credentials → Unauthenticated.
	_, err = client.Export(ctx, otlpReq(""))
	wantCode(t, err, codes.Unauthenticated)

	// Wrong bearer / wrong basic → Unauthenticated.
	_, err = client.Export(withMD("authorization", "Bearer nope"), otlpReq(""))
	wantCode(t, err, codes.Unauthenticated)
	bad := base64.StdEncoding.EncodeToString([]byte("alice:wrong"))
	_, err = client.Export(withMD("authorization", "Basic "+bad), otlpReq(""))
	wantCode(t, err, codes.Unauthenticated)

	// Valid basic credentials.
	good := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if _, err := client.Export(withMD("authorization", "Basic "+good), otlpReq("")); err != nil {
		t.Fatalf("basic export: %v", err)
	}

	// Valid session bearer token.
	sess := &model.Session{ID: "sess-123", UserID: "alice", AuthMethod: "basic",
		CreatedAt: time.Now().UnixMilli(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Export(withMD("authorization", "Bearer sess-123"), otlpReq("")); err != nil {
		t.Fatalf("bearer export: %v", err)
	}
}

func TestGRPCOTLPAuthOIDCModeRejectsBasic(t *testing.T) {
	st := openGRPCTestStore(t)
	hash, _ := auth.HashPassword("s3cret")
	cfg := config.Config{Auth: "oidc", BasicUser: "alice", BasicPassHash: hash}
	client := startGRPCOTLP(t, cfg, st)
	good := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	_, err := client.Export(withMD("authorization", "Basic "+good), otlpReq(""))
	wantCode(t, err, codes.Unauthenticated)
}

func TestGRPCOTLPAuthNoneIsOpen(t *testing.T) {
	st := openGRPCTestStore(t)
	client := startGRPCOTLP(t, config.Config{Auth: "none"}, st)
	if _, err := client.Export(context.Background(), otlpReq("")); err != nil {
		t.Fatalf("export: %v", err)
	}
}

func TestGRPCOTLPWorkspaceScoping(t *testing.T) {
	st := openGRPCTestStore(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, &model.Workspace{ID: "ws-b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(ctx, "ws-b", "alice", "editor"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddMember(ctx, "ws-b", "victor", "viewer"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*model.Session{
		{ID: "tok-alice", UserID: "alice"}, {ID: "tok-victor", UserID: "victor"}, {ID: "tok-mallory", UserID: "mallory"},
	} {
		s.AuthMethod = "oidc"
		s.CreatedAt = time.Now().UnixMilli()
		s.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
		if err := st.CreateSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// A run in the default workspace (another tenant from ws-b's view).
	expID, err := st.CreateExperiment(ctx, &model.Experiment{Name: "e-default"})
	if err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ExperimentID: expID, StartTime: 1}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	// A run in ws-b.
	expB, err := st.CreateExperiment(ctx, &model.Experiment{Name: "e-b", WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatal(err)
	}
	runB := &model.Run{ExperimentID: expB, StartTime: 1}
	if err := st.CreateRun(ctx, runB); err != nil {
		t.Fatal(err)
	}

	client := startGRPCOTLP(t, config.Config{Auth: "oidc"}, st)

	// Unknown workspace.
	_, err = client.Export(withMD("authorization", "Bearer tok-alice", "x-workspace", "nope"), otlpReq(""))
	wantCode(t, err, codes.InvalidArgument)
	// Non-member and viewer cannot write into ws-b.
	_, err = client.Export(withMD("authorization", "Bearer tok-mallory", "x-workspace", "ws-b"), otlpReq(""))
	wantCode(t, err, codes.PermissionDenied)
	_, err = client.Export(withMD("authorization", "Bearer tok-victor", "x-workspace", "ws-b"), otlpReq(""))
	wantCode(t, err, codes.PermissionDenied)
	// Editor in ws-b cannot link spans to a run of the default workspace.
	_, err = client.Export(withMD("authorization", "Bearer tok-alice", "x-workspace", "ws-b"), otlpReq(run.ID))
	wantCode(t, err, codes.NotFound)
	// ... nor can a default-workspace caller link to ws-b's run.
	_, err = client.Export(withMD("authorization", "Bearer tok-mallory"), otlpReq(runB.ID))
	wantCode(t, err, codes.NotFound)
	// Linking to its own workspace's run succeeds.
	if _, err := client.Export(withMD("authorization", "Bearer tok-alice", "x-workspace", "ws-b"), otlpReq(runB.ID)); err != nil {
		t.Fatalf("export into own run: %v", err)
	}
	spans, err := st.GetSpansByRun(ctx, runB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("want 1 span on runB, got %d", len(spans))
	}
}

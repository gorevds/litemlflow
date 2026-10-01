// Package grpcotlp implements a gRPC OTLP/TraceService receiver.
//
// Usage:
//
//	srv, err := grpcotlp.New(addr, store, grpcotlp.WithAuthenticator(fn))
//	// ...
//	go srv.Serve()
//	// on shutdown:
//	srv.Stop()
package grpcotlp

import (
	"context"
	"fmt"
	"net"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gorevds/litemlflow/internal/store"
)

// MaxRecvMsgSize bounds a single inbound OTLP export. 16 MiB is far above
// what an OTel SDK batch exporter sends (it splits batches at ~512 spans) yet
// small enough that a single unauthenticated-looking request cannot force a
// large allocation.
const MaxRecvMsgSize = 16 << 20

// maxSendMsgSize bounds responses; ExportTraceServiceResponse is tiny.
const maxSendMsgSize = 1 << 20

// Authenticator validates the raw value of the `authorization` request
// metadata (e.g. "Bearer <token>" or "Basic <b64>") and returns the
// authenticated user id. Any error rejects the call with
// codes.Unauthenticated. An empty authorization value is rejected before
// the Authenticator is invoked.
type Authenticator func(ctx context.Context, authorization string) (user string, err error)

// Authorizer decides whether user may write traces into workspace. A non-nil
// error rejects the call with codes.PermissionDenied (or the error's own gRPC
// status, when it carries one). user is "" when no Authenticator is set.
type Authorizer func(ctx context.Context, workspace, user string) error

// Option configures a Server.
type Option func(*options)

type options struct {
	authn Authenticator
	authz Authorizer
}

// WithAuthenticator requires every call to carry `authorization` metadata
// accepted by fn. Without it the receiver is open (auth=none deployments).
func WithAuthenticator(fn Authenticator) Option {
	return func(o *options) { o.authn = fn }
}

// WithAuthorizer installs a per-workspace write check run after the
// workspace has been resolved from `x-workspace` metadata.
func WithAuthorizer(fn Authorizer) Option {
	return func(o *options) { o.authz = fn }
}

// Server wraps a gRPC server that listens for OTLP trace exports.
type Server struct {
	addr string
	grpc *grpc.Server
	lis  net.Listener // non-nil when constructed via NewWithListener
	st   store.Store
}

type userCtxKey struct{}

// userFromContext returns the user stored by the auth interceptor.
func userFromContext(ctx context.Context) string {
	u, _ := ctx.Value(userCtxKey{}).(string)
	return u
}

// authInterceptor enforces the Authenticator on every unary call.
func authInterceptor(authn Authenticator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		var authz string
		if v := md.Get("authorization"); len(v) > 0 {
			authz = v[0]
		}
		if authz == "" {
			return nil, status.Error(codes.Unauthenticated, "missing authorization metadata")
		}
		user, err := authn(ctx, authz)
		if err != nil || user == "" {
			return nil, status.Error(codes.Unauthenticated, "invalid credentials")
		}
		return handler(context.WithValue(ctx, userCtxKey{}, user), req)
	}
}

// grpcOptions returns the hardened gRPC server options for the OTLP receiver.
// gRPC's own default recv cap is 4 MiB; we raise it to MaxRecvMsgSize (16 MiB)
// so large-but-legitimate batches are accepted while still bounding memory per
// message, and cap concurrent streams per connection.
//
// Operators exposing the gRPC port to untrusted networks should still place a
// rate-limiting reverse proxy in front; these caps are defense-in-depth.
func grpcOptions(o options) []grpc.ServerOption {
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxRecvMsgSize),
		grpc.MaxSendMsgSize(maxSendMsgSize),
		grpc.MaxConcurrentStreams(1024),
	}
	if o.authn != nil {
		opts = append(opts, grpc.ChainUnaryInterceptor(authInterceptor(o.authn)))
	}
	return opts
}

func newGRPC(st store.Store, opts []Option) *grpc.Server {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	g := grpc.NewServer(grpcOptions(o)...)
	coltracepb.RegisterTraceServiceServer(g, &traceServiceServer{store: st, authz: o.authz})
	return g
}

// New creates a Server that will listen on addr when Serve is called.
//
// No TLS is set up on the gRPC listener itself; operators who need TLS should
// place a TLS-terminating sidecar or reverse proxy in front. See
// docs/adr/0002-grpc-otlp-deps.md for rationale.
func New(addr string, st store.Store, opts ...Option) (*Server, error) {
	if addr == "" {
		return nil, fmt.Errorf("grpcotlp: addr is required")
	}
	return &Server{addr: addr, grpc: newGRPC(st, opts), st: st}, nil
}

// NewWithListener creates a Server backed by an already-open net.Listener.
// This is used in tests (bufconn) and for embedders that want to manage the
// listener lifecycle themselves.
func NewWithListener(lis net.Listener, st store.Store, opts ...Option) (*Server, error) {
	if lis == nil {
		return nil, fmt.Errorf("grpcotlp: listener is required")
	}
	return &Server{grpc: newGRPC(st, opts), lis: lis, st: st}, nil
}

// Serve starts a new TCP listener on s.addr and accepts connections. It blocks
// until Stop is called or a fatal error occurs. Callers should run it in a goroutine.
func (s *Server) Serve() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("grpcotlp listen %s: %w", s.addr, err)
	}
	return s.grpc.Serve(ln)
}

// ServeListener accepts connections on the listener supplied to NewWithListener.
// It blocks until Stop is called. Callers should run it in a goroutine.
func (s *Server) ServeListener() error {
	if s.lis == nil {
		return fmt.Errorf("grpcotlp: no listener; use Serve() instead")
	}
	return s.grpc.Serve(s.lis)
}

// Stop performs a graceful shutdown of the gRPC server.
func (s *Server) Stop() {
	s.grpc.GracefulStop()
}

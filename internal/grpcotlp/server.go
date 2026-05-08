// Package grpcotlp implements a gRPC OTLP/TraceService receiver.
//
// Usage:
//
//	srv, err := grpcotlp.New(addr, store)
//	// ...
//	go srv.Serve()
//	// on shutdown:
//	srv.Stop()
package grpcotlp

import (
	"fmt"
	"net"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/litemlflow/litemlflow/internal/store"
)

// Server wraps a gRPC server that listens for OTLP trace exports.
type Server struct {
	addr string
	grpc *grpc.Server
	lis  net.Listener // non-nil when constructed via NewWithListener
	st   store.Store
}

// New creates a Server that will listen on addr when Serve is called.
//
// No TLS is set up on the gRPC listener itself; operators who need TLS should
// place a TLS-terminating sidecar or reverse proxy in front. See
// docs/adr/0002-grpc-otlp-deps.md for rationale.
func New(addr string, st store.Store) (*Server, error) {
	if addr == "" {
		return nil, fmt.Errorf("grpcotlp: addr is required")
	}
	g := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(g, &traceServiceServer{store: st})
	return &Server{addr: addr, grpc: g, st: st}, nil
}

// NewWithListener creates a Server backed by an already-open net.Listener.
// This is used in tests (bufconn) and for embedders that want to manage the
// listener lifecycle themselves.
func NewWithListener(lis net.Listener, st store.Store) (*Server, error) {
	if lis == nil {
		return nil, fmt.Errorf("grpcotlp: listener is required")
	}
	g := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(g, &traceServiceServer{store: st})
	return &Server{grpc: g, lis: lis, st: st}, nil
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

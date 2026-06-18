package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/api/mlflow"
	"github.com/gorevds/litemlflow/internal/api/native"
	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/auth"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/datasets"
	"github.com/gorevds/litemlflow/internal/federation"
	"github.com/gorevds/litemlflow/internal/grpcotlp"
	"github.com/gorevds/litemlflow/internal/metrics"
	"github.com/gorevds/litemlflow/internal/store"
	"github.com/gorevds/litemlflow/internal/webhooks"
	"github.com/gorevds/litemlflow/ui"
)

// Server bundles the HTTP server and its dependencies.
type Server struct {
	cfg        config.Config
	logger     *slog.Logger
	store      *store.SQLiteStore
	artifacts  artifact.Store
	httpd      *http.Server
	grpcSrv    *grpcotlp.Server // nil when OTLPGRPCAddr is not configured
	dispatcher *webhooks.Dispatcher
}

// New constructs a server, opening the store and preparing the router.
// Call Run to start serving.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, err
	}
	st, err := store.OpenSQLite(ctx, cfg.DBPath, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, err
	}
	// STORAGE-S3: select backend based on config.ArtifactBackend.
	var art artifact.Store
	switch cfg.ArtifactBackend {
	case "", "fs":
		art, err = artifact.NewFilesystemStore(cfg.ArtifactsDir)
	case "s3":
		art, err = artifact.NewS3Store(artifact.S3Config{
			Endpoint:           cfg.S3Endpoint,
			Bucket:             cfg.S3Bucket,
			Region:             cfg.S3Region,
			AccessKey:          cfg.S3AccessKey,
			SecretKey:          cfg.S3SecretKey,
			Prefix:             cfg.S3Prefix,
			MultipartThreshold: cfg.S3MultipartThreshold,
		})
	default:
		err = fmt.Errorf("unknown artifact backend %q", cfg.ArtifactBackend)
	}
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	uiFS, err := ui.Files()
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	// Create webhook dispatcher (started lazily in buildRouter). The echo
	// ring buffer captures deliveries to lmf:// URLs for the demo UI.
	echoLog := webhooks.NewEchoLog(0)
	dispatcher := webhooks.NewWithOptions(ctx, st, logger, webhooks.Options{Echo: echoLog})
	// The dispatcher has already started its worker pool. If any later init
	// step fails we must stop it so those goroutines don't leak until ctx is
	// canceled (independent-review). Cleared once New succeeds.
	ok := false
	defer func() {
		if !ok {
			dispatcher.Stop(time.Second)
		}
	}()

	// Datasets v1.2: content-addressed store rooted under <data>/datasets/.
	// Errors here are fatal — dataset upload paths depend on it.
	datasetCAS, err := datasets.NewFilesystemCAS(filepath.Join(cfg.DataDir, "datasets"))
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("dataset cas: %w", err)
	}

	router := buildRouter(cfg, logger, st, art, uiFS, dispatcher, echoLog, datasetCAS)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	// GRPC-OTLP: optionally start a gRPC OTLP receiver on a separate port.
	var grpcSrv *grpcotlp.Server
	if cfg.OTLPGRPCAddr != "" {
		grpcSrv, err = grpcotlp.New(cfg.OTLPGRPCAddr, st)
		if err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("grpc otlp: %w", err)
		}
	}

	ok = true // init succeeded; the deferred dispatcher.Stop is a no-op now.
	return &Server{
		cfg: cfg, logger: logger, store: st, artifacts: art,
		httpd: httpSrv, grpcSrv: grpcSrv, dispatcher: dispatcher,
	}, nil
}

// Run starts serving until ctx is canceled or an error occurs.
//
// On shutdown the order is: HTTP first (stop accepting new requests), then
// the webhook dispatcher (drain in-flight deliveries with a 5 s ceiling),
// then gRPC OTLP (stop accepting traces), then close the store. The
// dispatcher drain matters because audit-log webhooks would otherwise be
// silently lost on SIGTERM.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 2)

	// Start the background janitor if enabled. stopJanitor cancels it and
	// waits for the goroutine to exit; it must run before store.Close() so a
	// tick in flight cannot hit a closed database.
	stopJanitor := func() {}
	if s.cfg.JanitorEnabled {
		stopJanitor = StartJanitor(ctx, s.store, s.cfg.JanitorInterval, s.cfg.RunStaleAfter, s.cfg.EventsRetention, s.logger)
	}

	// Start HTTP server.
	go func() {
		s.logger.Info("listening", slog.String("addr", s.cfg.Addr), slog.String("data", s.cfg.DataDir))
		if err := s.httpd.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Optionally start the gRPC OTLP server on a separate goroutine.
	if s.grpcSrv != nil {
		go func() {
			s.logger.Info("grpc otlp listening", slog.String("addr", s.cfg.OTLPGRPCAddr))
			if err := s.grpcSrv.Serve(); err != nil {
				errCh <- fmt.Errorf("grpc otlp: %w", err)
			}
		}()
	}

	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpd.Shutdown(shutdownCtx)
		// Drain webhooks: don't lose in-flight audit-log deliveries.
		if s.dispatcher != nil {
			s.dispatcher.Stop(5 * time.Second)
		}
		if s.grpcSrv != nil {
			s.grpcSrv.Stop()
		}
		stopJanitor() // cancel + await the janitor before closing the store
		_ = s.store.Close()
	}

	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case err := <-errCh:
		shutdown()
		return err
	}
}

// Handler exposes the HTTP handler — useful for tests via httptest.
func (s *Server) Handler() http.Handler { return s.httpd.Handler }

// Close releases all resources.
func (s *Server) Close() error {
	_ = s.httpd.Close()
	return s.store.Close()
}

func buildRouter(cfg config.Config, logger *slog.Logger, st store.Store, art artifact.Store, uiFS fs.FS, dispatcher *webhooks.Dispatcher, echoLog *webhooks.EchoLog, datasetCAS datasets.Store) http.Handler {
	// Build the metrics registry before anything else so we can mount /metrics
	// BEFORE the auth middleware (Prometheus scrapers don't send credentials).
	reg := metrics.NewRegistry()
	std := metrics.NewStandard(reg, cfg.DBPath)

	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoveryMiddleware(logger))
	r.Use(securityHeadersMiddleware)
	// v2.0: rewrite /api/v2/... → /api/v1/... before the router matches.
	// Both namespaces resolve to the same handler; v2 is a stable alias
	// per ADR 0003 — clients pinning to the LTS contract use v2, existing
	// v1 clients keep working unchanged.
	r.Use(apiV2AliasMiddleware)
	r.Use(loggingMiddleware(logger))
	// Metrics middleware runs before auth so it captures every request,
	// including unauthenticated ones that are rejected by auth.
	r.Use(metricsMiddleware(std))
	// Rate-limit credential brute force per client IP. ~5 burst, refilling 1
	// attempt / 12s ≈ 5 attempts/min sustained per IP. The same limiter guards
	// the login endpoint (every POST) and failed HTTP Basic attempts (only
	// failures consume a token, so legitimate Basic clients are unaffected).
	// Placed after apiV2Alias so the path is already normalized.
	authLimiter := newAuthRateLimiter(5, 1.0/12.0)
	r.Use(rateLimitAuthMiddleware(authLimiter))
	// Body limit applies to all non-artifact endpoints; artifact subrouter
	// re-applies its own (larger) limit.
	r.Use(bodyLimitMiddleware(cfg.MaxRequestSize))

	// AUTH-OIDC: wire the session-aware auth middleware. The SQLiteStore
	// implements SessionLookup via the methods in store/sessions.go.
	var sessions SessionLookup
	if sqlSt, ok := st.(*store.SQLiteStore); ok {
		sessions = sqlSt
	}
	r.Use(authMiddlewareWithSessions(cfg, sessions, authLimiter))
	// TENANCY: workspaceMiddleware runs after auth so the workspace is available
	// to all downstream handlers. It validates the X-Workspace header /
	// lmf_workspace cookie against the store and falls back to "default".
	r.Use(workspaceMiddleware(st))
	// RBAC: role enforcement runs after workspace resolution. In auth=none mode
	// and for the default workspace with no members it is a no-op, preserving
	// backward compat for solo users and the MLflow compat test suite.
	r.Use(rbacMiddleware(cfg, st))

	// /metrics is public (see isPublicPath). Auth middleware skips it, so
	// Prometheus can scrape without credentials even when auth=basic.
	r.Get("/metrics", func(w http.ResponseWriter, req *http.Request) {
		std.RefreshProcess()
		metrics.Handler(reg)(w, req)
	})

	// Mount API surfaces.
	mlh := &mlflow.Handler{Store: st, Artifacts: art, Dispatcher: dispatcher}
	mlh.Mount(r)

	// AUTH-OIDC: build native handler with full auth wiring.
	nat := &native.Handler{
		Store: st, Cfg: cfg, SessionStore: nil, EchoLog: echoLog, Datasets: datasetCAS,
		// v1.3: bounded in-memory cache for federated search responses.
		FederationCache: federation.NewCache(0, 0),
	}
	if sqlSt, ok := st.(*store.SQLiteStore); ok {
		nat.SessionStore = sqlSt
	}
	// Wire OIDC provider when configured.
	if cfg.Auth == "oidc" && cfg.OIDCIssuer != "" && cfg.OIDCClientID != "" {
		scopes := strings.Fields(cfg.OIDCScopes)
		nat.OIDCProvider = auth.NewProvider(
			cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret,
			cfg.OIDCRedirectURL, scopes,
		)
	}
	nat.Mount(r)

	// UI (and root redirect).
	uiHandler := http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS)))
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/ui/", http.StatusFound)
	})
	r.Get("/ui", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/ui/", http.StatusFound)
	})
	r.Get("/ui/*", func(w http.ResponseWriter, req *http.Request) {
		// Serve index.html for the root of /ui/ since http.FileServer
		// auto-redirects, which interferes with our subapp paths.
		uiHandler.ServeHTTP(w, req)
	})

	return r
}

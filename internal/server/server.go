package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/litemlflow/litemlflow/internal/api/mlflow"
	"github.com/litemlflow/litemlflow/internal/api/native"
	"github.com/litemlflow/litemlflow/internal/artifact"
	"github.com/litemlflow/litemlflow/internal/auth"
	"github.com/litemlflow/litemlflow/internal/config"
	"github.com/litemlflow/litemlflow/internal/store"
	"github.com/litemlflow/litemlflow/ui"
)

// Server bundles the HTTP server and its dependencies.
type Server struct {
	cfg       config.Config
	logger    *slog.Logger
	store     *store.SQLiteStore
	artifacts artifact.Store
	httpd     *http.Server
}

// New constructs a server, opening the store and preparing the router.
// Call Run to start serving.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
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
			Endpoint:  cfg.S3Endpoint,
			Bucket:    cfg.S3Bucket,
			Region:    cfg.S3Region,
			AccessKey: cfg.S3AccessKey,
			SecretKey: cfg.S3SecretKey,
			Prefix:    cfg.S3Prefix,
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
	router := buildRouter(cfg, logger, st, art, uiFS)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
	return &Server{cfg: cfg, logger: logger, store: st, artifacts: art, httpd: srv}, nil
}

// Run starts serving until ctx is canceled or an error occurs.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", slog.String("addr", s.cfg.Addr), slog.String("data", s.cfg.DataDir))
		if err := s.httpd.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpd.Shutdown(shutdownCtx)
		_ = s.store.Close()
		return nil
	case err := <-errCh:
		_ = s.store.Close()
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

func buildRouter(cfg config.Config, logger *slog.Logger, st store.Store, art artifact.Store, uiFS fs.FS) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoveryMiddleware(logger))
	r.Use(loggingMiddleware(logger))
	// Body limit applies to all non-artifact endpoints; artifact subrouter
	// re-applies its own (larger) limit.
	r.Use(bodyLimitMiddleware(cfg.MaxRequestSize))

	// AUTH-OIDC: wire the session-aware auth middleware. The SQLiteStore
	// implements SessionLookup via the methods in store/sessions.go.
	var sessions SessionLookup
	if sqlSt, ok := st.(*store.SQLiteStore); ok {
		sessions = sqlSt
	}
	r.Use(authMiddlewareWithSessions(cfg, sessions))

	// Mount API surfaces.
	mlh := &mlflow.Handler{Store: st, Artifacts: art}
	mlh.Mount(r)

	// AUTH-OIDC: build native handler with full auth wiring.
	nat := &native.Handler{Store: st, Cfg: cfg, SessionStore: nil}
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

package server_test

// Guards independent-review P2: server.New starts the webhook dispatcher's
// worker pool (8 workers + 1 monitor goroutine) partway through init. If a
// later step fails (dataset CAS, gRPC OTLP), New returned the error without
// stopping the dispatcher, leaking those goroutines until the caller's ctx was
// eventually canceled. We force the dataset-CAS step to fail and assert the
// goroutine count settles back near baseline.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/server"
)

func TestServerNewStopsDispatcherOnInitError(t *testing.T) {
	dir := t.TempDir()
	// Place a regular file where the dataset CAS expects to create a directory
	// (<data>/datasets), so NewFilesystemCAS fails — this happens AFTER the
	// dispatcher's workers have started.
	if err := os.WriteFile(filepath.Join(dir, "datasets"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}
	cfg := config.Config{
		DataDir:        dir,
		DBPath:         filepath.Join(dir, "t.db"),
		ArtifactsDir:   filepath.Join(dir, "art"),
		MaxRequestSize: 1 << 20,
		Addr:           ":0",
		Auth:           "none",
	}
	c, err := config.FromEnv(cfg)
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := server.New(ctx, c, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("expected New to fail when the dataset CAS path is a regular file")
	}

	// The dispatcher pool is 9 goroutines; a leak keeps the count ~+9. Poll
	// (transient runtime/test noise settles well under +4) — the cancel() is
	// deferred and must NOT be the thing that frees them.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+4 {
			return
		}
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle: before=%d after=%d (dispatcher workers leaked on init error)",
		before, runtime.NumGoroutine())
}

// Command litemlflow is the LiteMLflow server entry point.
//
// Usage:
//
//	litemlflow up           [--data DIR] [--addr HOST:PORT] [--auth MODE] ...
//	litemlflow version
//	litemlflow migrate      [--data DIR]
//	litemlflow rollback     [--data DIR]
//	litemlflow backup       [--data DIR] [--out FILE]
//	litemlflow restore      [--data DIR] [--in FILE]
//	litemlflow import-mlflow --from URL --data DIR [--workspace WS] [--include-deleted] [--dry-run]
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorevds/litemlflow/internal/artifact"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/migrator"
	"github.com/gorevds/litemlflow/internal/migrations"
	"github.com/gorevds/litemlflow/internal/server"
	"github.com/gorevds/litemlflow/internal/store"
	"github.com/gorevds/litemlflow/pkg/version"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "up", "serve":
		err = runUp(args)
	case "version", "--version", "-v":
		fmt.Println("litemlflow", version.String())
	case "migrate":
		err = runMigrate(args)
	case "rollback":
		err = runRollback(args)
	case "backup":
		err = runBackup(args)
	case "restore":
		err = runRestore(args)
	case "import-mlflow":
		err = runImportMLflow(args)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `litemlflow — single-binary experiment tracker.

Usage:
  litemlflow up           [--data DIR] [--addr HOST:PORT] [--auth MODE] [--basic-user USER --basic-pass-hash HASH] [--dev]
                          [--oidc-issuer URL] [--oidc-client-id ID] [--oidc-client-secret SECRET]
                          [--oidc-redirect-url URL] [--session-ttl DURATION]
                          [--artifact-backend fs|s3]
                          [--s3-endpoint URL] [--s3-bucket BUCKET] [--s3-region REGION]
                          [--s3-access-key KEY] [--s3-secret-key SECRET] [--s3-prefix PREFIX]
                          [--s3-multipart-threshold BYTES]
                          [--otlp-grpc-addr HOST:PORT]
  litemlflow migrate      [--data DIR]
  litemlflow rollback     [--data DIR]
  litemlflow backup       [--data DIR] [--out FILE]
  litemlflow restore      [--data DIR] [--in FILE]
  litemlflow import-mlflow --from MLFLOW_URL --data DIR [--workspace WS] [--include-deleted] [--dry-run]
  litemlflow version

Environment variables override defaults; flags override env vars.

  LITEMLFLOW_DATA                the data directory (required)
  LITEMLFLOW_ADDR                listen address, e.g., :5000
  LITEMLFLOW_AUTH                one of: none|basic|oidc
  LITEMLFLOW_BASIC_USER          basic auth username
  LITEMLFLOW_BASIC_PASS_HASH     basic auth password (hex SHA-256)
  LITEMLFLOW_OIDC_ISSUER         OIDC issuer URL (required for auth=oidc)
  LITEMLFLOW_OIDC_CLIENT_ID      OIDC client ID (required for auth=oidc)
  LITEMLFLOW_OIDC_CLIENT_SECRET  OIDC client secret (optional; omit for public clients)
  LITEMLFLOW_OIDC_REDIRECT_URL   OIDC redirect URL (required for auth=oidc)
  LITEMLFLOW_OIDC_SCOPES         space-separated scopes (default: openid email profile)
  LITEMLFLOW_SESSION_TTL         session duration, e.g. 168h (default 168h = 7 days)
  LITEMLFLOW_ARTIFACT_BACKEND    artifact backend: fs (default) or s3
  LITEMLFLOW_S3_ENDPOINT         S3-compatible endpoint URL
  LITEMLFLOW_S3_BUCKET           S3 bucket name
  LITEMLFLOW_S3_REGION           S3 region (e.g. us-east-1)
  LITEMLFLOW_S3_ACCESS_KEY       S3 access key ID
  LITEMLFLOW_S3_SECRET_KEY       S3 secret access key
  LITEMLFLOW_S3_PREFIX           optional S3 key prefix (e.g. litemlflow/)
  LITEMLFLOW_DEV=1               dev-mode logs and verbose errors
`)
}

func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory (overrides $LITEMLFLOW_DATA)")
	addr := fs.String("addr", "", "listen address, e.g., :5000")
	auth := fs.String("auth", "", "auth mode: none, basic, oidc")
	basicUser := fs.String("basic-user", "", "basic auth username")
	basicPassHash := fs.String("basic-pass-hash", "", "basic auth password (hex SHA-256)")
	dev := fs.Bool("dev", false, "enable dev mode")
	// AUTH-OIDC: OIDC / session flags
	oidcIssuer := fs.String("oidc-issuer", "", "OIDC issuer URL (required when --auth=oidc)")
	oidcClientID := fs.String("oidc-client-id", "", "OIDC client ID")
	oidcClientSecret := fs.String("oidc-client-secret", "", "OIDC client secret (optional for public clients)")
	oidcRedirectURL := fs.String("oidc-redirect-url", "", "OIDC redirect URL")
	sessionTTL := fs.Duration("session-ttl", 0, "session lifetime, e.g. 168h (default 7 days)")
	// STORAGE-S3: artifact backend flags
	artifactBackend := fs.String("artifact-backend", "", "artifact storage backend: fs (default) or s3")
	s3Endpoint := fs.String("s3-endpoint", "", "S3 endpoint URL, e.g. https://s3.amazonaws.com")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket name")
	s3Region := fs.String("s3-region", "", "S3 region, e.g. us-east-1")
	s3AccessKey := fs.String("s3-access-key", "", "S3 access key ID")
	s3SecretKey := fs.String("s3-secret-key", "", "S3 secret access key")
	s3Prefix := fs.String("s3-prefix", "", "optional S3 key prefix, e.g. litemlflow/")
	s3MultipartThreshold := fs.Int64("s3-multipart-threshold", 0, "min upload size (bytes) for multipart S3 upload; default 100 MiB")
	// GRPC-OTLP: optional gRPC OTLP receiver
	otlpGRPCAddr := fs.String("otlp-grpc-addr", "", "listen address for OTLP/gRPC receiver, e.g. 127.0.0.1:4317 (disabled by default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{
		DataDir:              *dataDir,
		Addr:                 *addr,
		Auth:                 *auth,
		BasicUser:            *basicUser,
		BasicPassHash:        *basicPassHash,
		DevMode:              *dev,
		OIDCIssuer:           *oidcIssuer,
		OIDCClientID:         *oidcClientID,
		OIDCClientSecret:     *oidcClientSecret,
		OIDCRedirectURL:      *oidcRedirectURL,
		SessionTTL:           *sessionTTL,
		ArtifactBackend:      *artifactBackend,
		S3Endpoint:           *s3Endpoint,
		S3Bucket:             *s3Bucket,
		S3Region:             *s3Region,
		S3AccessKey:          *s3AccessKey,
		S3SecretKey:          *s3SecretKey,
		S3Prefix:             *s3Prefix,
		S3MultipartThreshold: *s3MultipartThreshold,
		OTLPGRPCAddr:         *otlpGRPCAddr,
	})
	if err != nil {
		return err
	}
	logger := newLogger(cfg.DevMode)
	srv, err := server.New(context.Background(), cfg, logger)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("litemlflow starting", slog.String("version", version.Version))
	return srv.Run(ctx)
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	st, err := store.OpenSQLite(context.Background(), cfg.DBPath, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		return err
	}
	v, err := migrations.CurrentVersion(context.Background(), st.DB())
	if err != nil {
		return err
	}
	fmt.Println("schema version:", v)
	return nil
}

func runRollback(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}
	st, err := store.OpenSQLite(context.Background(), cfg.DBPath, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := migrations.Rollback(context.Background(), st.DB()); err != nil {
		return err
	}
	v, _ := migrations.CurrentVersion(context.Background(), st.DB())
	fmt.Println("rolled back; schema version now:", v)
	return nil
}

// runBackup tars the data directory into a single .tar.gz file.
//
// The server should be stopped or quiescent during a backup; SQLite WAL is
// included verbatim, so even if writes are happening, the backup is at
// least crash-consistent (matches a power-loss scenario).
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory")
	out := fs.String("out", "", "output file (default: litemlflow-backup-<ts>.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}
	target := *out
	if target == "" {
		target = fmt.Sprintf("litemlflow-backup-%d.tar.gz", nowTS())
	}
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	root := cfg.DataDir
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			fp, err := os.Open(path)
			if err != nil {
				return err
			}
			defer fp.Close()
			if _, err := io.Copy(tw, fp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("backup written to", target)
	return nil
}

// runRestore unpacks a .tar.gz produced by runBackup into the data directory.
// Refuses to overwrite a non-empty data dir (use a fresh dir).
func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory")
	in := fs.String("in", "", "input backup file (.tar.gz)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("--in is required")
	}
	cfg, err := config.FromEnv(config.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}
	if entries, _ := os.ReadDir(cfg.DataDir); len(entries) > 0 {
		return errors.New("data directory is not empty; restore into a fresh directory")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	// Decompression-bomb defense: cap the inflated size at a high but finite
	// ceiling. Real-world LiteMLflow backups are under 100 GiB; the cap lets
	// large legitimate restores succeed but stops a malicious 1 KiB → 1 EiB
	// gzip from filling the disk. Operators with bigger backups can override
	// via LITEMLFLOW_RESTORE_MAX_GIB.
	maxGiB := int64(200)
	if v := os.Getenv("LITEMLFLOW_RESTORE_MAX_GIB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxGiB = n
		}
	}
	limited := io.LimitReader(gz, maxGiB<<30)
	tr := tar.NewReader(limited)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Refuse path traversal in archive entries.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", hdr.Name)
		}
		dst := filepath.Join(cfg.DataDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o750); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return err
			}
			// Sanitize file modes: never restore world-writable, setuid, or
			// other dangerous bits even if the archive specifies them.
			// Keep only owner+group bits.
			mode := os.FileMode(hdr.Mode) & 0o640
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			_ = out.Close()
		}
	}
	fmt.Println("restored to", cfg.DataDir)
	return nil
}

// runImportMLflow copies experiments/runs/metrics/params/tags/artifacts from
// a running MLflow tracking server directly into a LiteMLflow data directory.
// LiteMLflow must NOT be running on that data directory during import.
func runImportMLflow(args []string) error {
	fs := flag.NewFlagSet("import-mlflow", flag.ContinueOnError)
	from := fs.String("from", "", "URL of the source MLflow tracking server (required)")
	dataDir := fs.String("data", "", "target LiteMLflow data directory (required)")
	workspace := fs.String("workspace", "default", "workspace to import into")
	includeDeleted := fs.Bool("include-deleted", false, "also import lifecycle_stage='deleted' rows")
	dryRun := fs.Bool("dry-run", false, "enumerate but do not write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("--from is required")
	}
	if *dataDir == "" {
		return errors.New("--data is required")
	}

	// Initialise the target data directory and SQLite store.
	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	dbPath := filepath.Join(*dataDir, "litemlflow.db")
	artifactsDir := filepath.Join(*dataDir, "artifacts")

	ctx := context.Background()
	st, err := store.OpenSQLite(ctx, dbPath, *dataDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Apply any pending schema migrations.
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}

	artStore, err := artifact.NewFilesystemStore(artifactsDir)
	if err != nil {
		return fmt.Errorf("open artifact store: %w", err)
	}

	filter := migrator.FilterActive
	if *includeDeleted {
		filter = migrator.FilterAll
	}

	imp := &migrator.MLflowImporter{
		SourceURL:     *from,
		Workspace:     *workspace,
		DryRun:        *dryRun,
		Include:       filter,
		HTTP:          &http.Client{Timeout: 120 * time.Second},
		Store:         st,
		ArtifactStore: artStore,
		OnProgress: func(stage string, n int) {
			fmt.Printf("[import] %s: %d entities processed\n", stage, n)
		},
	}
	imp.SetCheckpointDir(*dataDir)

	if _, err := imp.Run(ctx); err != nil {
		return err
	}
	return nil
}

func newLogger(dev bool) *slog.Logger {
	level := slog.LevelInfo
	if dev {
		level = slog.LevelDebug
	}
	if dev {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// nowTS returns the current Unix timestamp; pulled into a var so tests can
// freeze time when checking backup filenames.
var nowTS = func() int64 { return time.Now().Unix() }

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
//
// **S3 artifact backend:** runBackup only tars cfg.DataDir, which on an
// S3-configured server contains the SQLite DB but NOT the artifacts (those
// live in the bucket). Restoring such a tar into a fresh deploy will yield
// a UI full of broken artifact links. To prevent silent data loss, runBackup
// refuses to proceed when ArtifactBackend=="s3" unless the operator passes
// --include-only-db (acknowledging the gap and snapshotting the bucket
// separately) or --include-s3 (which streams every object in
// <prefix>/<...> into the tar — only feasible for small buckets).
func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := fs.String("data", "", "data directory")
	out := fs.String("out", "", "output file (default: litemlflow-backup-<ts>.tar.gz)")
	includeOnlyDB := fs.Bool("include-only-db", false,
		"with --artifact-backend=s3, acknowledge that artifacts are not in the backup (snapshot the bucket separately)")
	includeS3 := fs.Bool("include-s3", false,
		"with --artifact-backend=s3, stream every artifact object into the backup tar (slow on large buckets)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}

	// S3 sanity gate. Either the operator opts in to the partial backup
	// (--include-only-db, with the warning surfaced) or to the full one
	// (--include-s3, which streams the bucket). Neither set → hard error.
	if cfg.ArtifactBackend == "s3" && !*includeOnlyDB && !*includeS3 {
		return errors.New(
			"backup with --artifact-backend=s3 would silently exclude artifacts from the tar.\n" +
				"  Pick one:\n" +
				"    --include-only-db   tar only the SQLite DB; snapshot the bucket separately\n" +
				"    --include-s3        stream every S3 object into the backup tar (slow)\n",
		)
	}
	if *includeOnlyDB && *includeS3 {
		return errors.New("--include-only-db and --include-s3 are mutually exclusive")
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

	if cfg.ArtifactBackend == "s3" && *includeS3 {
		// Stream every artifact under every active run into the tar. We
		// enumerate runs from the SQLite DB and use the existing
		// artifact.Store.List(runID, dir) API rather than a backend-walker
		// — both FilesystemStore and S3Store reject runID=="" with
		// ErrInvalidPath, so a "list-everything" sentinel doesn't exist.
		s3Store, err := artifact.NewS3Store(artifact.S3Config{
			Endpoint: cfg.S3Endpoint, Bucket: cfg.S3Bucket, Region: cfg.S3Region,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, Prefix: cfg.S3Prefix,
		})
		if err != nil {
			return fmt.Errorf("backup: open s3 store: %w", err)
		}
		st, err := store.OpenSQLite(context.Background(), cfg.DBPath, cfg.DataDir)
		if err != nil {
			return fmt.Errorf("backup: open store for run enumeration: %w", err)
		}
		defer st.Close()
		count, bytes, err := streamS3IntoTar(tw, s3Store, st)
		if err != nil {
			return fmt.Errorf("backup: stream s3 artifacts: %w", err)
		}
		fmt.Printf("backup wrote %d S3 objects (%d bytes) into the tar\n", count, bytes)
	}

	fmt.Println("backup written to", target)
	if cfg.ArtifactBackend == "s3" && *includeOnlyDB {
		fmt.Println("WARNING: artifacts in S3 are NOT in this tar; snapshot the bucket separately to avoid restore-time link rot.")
	}
	return nil
}

// streamS3IntoTar walks every artifact under every active run via the
// artifact.Store interface (which requires a non-empty runID), copying each
// object into "s3-artifacts/<runID>/<relPath>". Returns total file count
// and bytes streamed.
func streamS3IntoTar(tw *tar.Writer, art artifact.Store, st *store.SQLiteStore) (int, int64, error) {
	// Enumerate active run IDs.
	rows, err := st.DB().QueryContext(context.Background(),
		`SELECT id FROM runs WHERE lifecycle_stage = 'active' ORDER BY id`)
	if err != nil {
		return 0, 0, fmt.Errorf("enumerate runs: %w", err)
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		runIDs = append(runIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	var count int
	var totalLen int64

	// Recursive walk per run. child.Path is relative to the run's
	// artifact root, so the tar name is simply runID + "/" + child.Path.
	// (Concatenating any parent prefix would double up because List
	// already returns full relative paths.)
	var walk func(runID, dir string) error
	walk = func(runID, dir string) error {
		entries, lerr := art.List(runID, dir)
		if lerr != nil {
			return lerr
		}
		for _, child := range entries {
			if child.IsDir {
				if werr := walk(runID, child.Path); werr != nil {
					return werr
				}
				continue
			}
			rc, _, oerr := art.Open(runID, child.Path)
			if oerr != nil {
				return oerr
			}
			hdr := &tar.Header{
				Name:    "s3-artifacts/" + runID + "/" + child.Path,
				Mode:    0o640,
				Size:    child.Size, // use List size — no extra stat round-trip
				ModTime: time.Now(),
			}
			if werr := tw.WriteHeader(hdr); werr != nil {
				_ = rc.Close()
				return werr
			}
			n, cerr := io.Copy(tw, rc)
			_ = rc.Close()
			if cerr != nil {
				return cerr
			}
			count++
			totalLen += n
		}
		return nil
	}

	for _, runID := range runIDs {
		if werr := walk(runID, ""); werr != nil {
			return count, totalLen, fmt.Errorf("run %s: %w", runID, werr)
		}
	}
	return count, totalLen, nil
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

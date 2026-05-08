// Command litemlflow is the LiteMLflow server entry point.
//
// Usage:
//
//	litemlflow up      [--data DIR] [--addr HOST:PORT] [--auth MODE] ...
//	litemlflow version
//	litemlflow migrate [--data DIR]
//	litemlflow rollback [--data DIR]
//	litemlflow backup  [--data DIR] [--out FILE]
//	litemlflow restore [--data DIR] [--in FILE]
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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/litemlflow/litemlflow/internal/config"
	"github.com/litemlflow/litemlflow/internal/migrations"
	"github.com/litemlflow/litemlflow/internal/server"
	"github.com/litemlflow/litemlflow/internal/store"
	"github.com/litemlflow/litemlflow/pkg/version"
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
  litemlflow up       [--data DIR] [--addr HOST:PORT] [--auth MODE] [--basic-user USER --basic-pass-hash HASH] [--dev]
                      [--artifact-backend fs|s3]
                      [--s3-endpoint URL] [--s3-bucket BUCKET] [--s3-region REGION]
                      [--s3-access-key KEY] [--s3-secret-key SECRET] [--s3-prefix PREFIX]
  litemlflow migrate  [--data DIR]
  litemlflow rollback [--data DIR]
  litemlflow backup   [--data DIR] [--out FILE]
  litemlflow restore  [--data DIR] [--in FILE]
  litemlflow version

Environment variables override defaults; flags override env vars.

  LITEMLFLOW_DATA              the data directory (required)
  LITEMLFLOW_ADDR              listen address, e.g., :5000
  LITEMLFLOW_AUTH              one of: none|basic|oidc
  LITEMLFLOW_BASIC_USER        basic auth username
  LITEMLFLOW_BASIC_PASS_HASH   basic auth password (hex SHA-256)
  LITEMLFLOW_DEV=1             dev-mode logs and verbose errors
  LITEMLFLOW_ARTIFACT_BACKEND  artifact backend: fs (default) or s3
  LITEMLFLOW_S3_ENDPOINT       S3-compatible endpoint URL
  LITEMLFLOW_S3_BUCKET         S3 bucket name
  LITEMLFLOW_S3_REGION         S3 region (e.g. us-east-1)
  LITEMLFLOW_S3_ACCESS_KEY     S3 access key ID
  LITEMLFLOW_S3_SECRET_KEY     S3 secret access key
  LITEMLFLOW_S3_PREFIX         optional S3 key prefix (e.g. litemlflow/)
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
	// Artifact backend flags.
	artifactBackend := fs.String("artifact-backend", "", "artifact storage backend: fs (default) or s3")
	s3Endpoint := fs.String("s3-endpoint", "", "S3 endpoint URL, e.g. https://s3.amazonaws.com")
	s3Bucket := fs.String("s3-bucket", "", "S3 bucket name")
	s3Region := fs.String("s3-region", "", "S3 region, e.g. us-east-1")
	s3AccessKey := fs.String("s3-access-key", "", "S3 access key ID")
	s3SecretKey := fs.String("s3-secret-key", "", "S3 secret access key")
	s3Prefix := fs.String("s3-prefix", "", "optional S3 key prefix, e.g. litemlflow/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv(config.Config{
		DataDir:         *dataDir,
		Addr:            *addr,
		Auth:            *auth,
		BasicUser:       *basicUser,
		BasicPassHash:   *basicPassHash,
		DevMode:         *dev,
		ArtifactBackend: *artifactBackend,
		S3Endpoint:      *s3Endpoint,
		S3Bucket:        *s3Bucket,
		S3Region:        *s3Region,
		S3AccessKey:     *s3AccessKey,
		S3SecretKey:     *s3SecretKey,
		S3Prefix:        *s3Prefix,
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
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
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
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
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
	tr := tar.NewReader(gz)
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
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			// Sanitize file modes: never restore world-writable, setuid, or
			// other dangerous bits even if the archive specifies them.
			// Keep only the user/group/other read-write-execute bits.
			mode := os.FileMode(hdr.Mode) & 0o644
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

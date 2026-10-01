package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupRestoreRoundTripExcludesOwnOutput: --out inside the data dir must
// not be archived into itself, and every data file must round-trip.
func TestBackupRestoreRoundTripExcludesOwnOutput(t *testing.T) {
	src := t.TempDir()
	want := map[string]string{}
	for i := 0; i < 40; i++ {
		rel := filepath.Join("artifacts", fmt.Sprintf("r%d", i%4), fmt.Sprintf("f%d.txt", i))
		want[filepath.ToSlash(rel)] = fmt.Sprintf("payload-%d", i)
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, rel), []byte(want[filepath.ToSlash(rel)]), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(src, "self-backup.tar.gz")
	if err := runBackup([]string{"--data", src, "--out", out}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// The archive must be a complete gzip stream and must not contain itself.
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		if hdr.Name == "self-backup.tar.gz" {
			t.Fatal("backup archived its own output file")
		}
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := runRestore([]string{"--data", dst, "--in", out}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for rel, content := range want {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || string(got) != content {
			t.Fatalf("%s: got %q err=%v, want %q", rel, got, err, content)
		}
	}
}

// TestBackupWhileWriterActiveIsConsistent: a backup taken while another
// connection has committed rows still sitting in the WAL (no checkpoint) and
// an open, uncommitted write transaction must restore to a DB that passes
// integrity_check, contains every committed row and none of the uncommitted
// ones. The raw -wal/-shm files must not be archived.
func TestBackupWhileWriterActiveIsConsistent(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	dbPath := filepath.Join(src, "litemlflow.db")
	live, err := sql.Open("sqlite", "file:"+dbPath+
		"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	live.SetMaxOpenConns(2)
	if _, err := live.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	const committed = 500
	for i := 0; i < committed; i++ {
		if _, err := live.ExecContext(ctx, `INSERT INTO t(v) VALUES (?)`, strings.Repeat("x", 200)); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(dbPath + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("expected committed data in a non-empty WAL, stat=%v err=%v", fi, err)
	}
	tx, err := live.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for i := 0; i < 100; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO t(v) VALUES ('uncommitted')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(src, "artifacts", "r1"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "artifacts", "r1", "model.bin"), []byte("weights"), 0o640); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup([]string{"--data", src, "--out", out}); err != nil {
		t.Fatalf("backup while writer active: %v", err)
	}
	_ = tx.Rollback()

	names := tarNames(t, out)
	for _, n := range names {
		if strings.HasSuffix(n, "-wal") || strings.HasSuffix(n, "-shm") {
			t.Fatalf("live SQLite sidecar %q archived", n)
		}
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := runRestore([]string{"--data", dst, "--in", out}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "artifacts", "r1", "model.bin")); err != nil || string(got) != "weights" {
		t.Fatalf("artifact: got %q err=%v", got, err)
	}
	rdb, err := sql.Open("sqlite", "file:"+filepath.Join(dst, "litemlflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var ic string
	if err := rdb.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check = %q err=%v", ic, err)
	}
	var n, unc int
	if err := rdb.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE v='uncommitted') FROM t`).Scan(&n, &unc); err != nil {
		t.Fatal(err)
	}
	if n != committed || unc != 0 {
		t.Fatalf("restored rows = %d (uncommitted %d), want %d committed only", n, unc, committed)
	}
}

func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
	}
}

func TestCheckHealth(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"healthy", 200, `{"ok":true}`, true},
		{"not ok", 200, `{"ok":false}`, false},
		{"bad json", 200, `nope`, false},
		{"5xx", 503, `{"ok":true}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			err := checkHealth(srv.Client(), srv.URL+"/healthz")
			if (err == nil) != tc.ok {
				t.Fatalf("checkHealth err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
	if err := checkHealth(http.DefaultClient, "http://127.0.0.1:1/healthz"); err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestDefaultHealthURL(t *testing.T) {
	for in, want := range map[string]string{
		"":               "http://127.0.0.1:5000/healthz",
		":5000":          "http://127.0.0.1:5000/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"10.0.0.5:9000":  "http://10.0.0.5:9000/healthz",
		"[::]:7000":      "http://127.0.0.1:7000/healthz",
		"127.0.0.1:5001": "http://127.0.0.1:5001/healthz",
	} {
		if got := defaultHealthURL(in); got != want {
			t.Errorf("defaultHealthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBackupOfStoppedWALDatabase: with the server stopped (WAL DB closed, no
// -wal/-shm left behind) the read-only snapshot connection must still work.
func TestBackupOfStoppedWALDatabase(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	dbPath := filepath.Join(src, "litemlflow.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE t(id INTEGER PRIMARY KEY); INSERT INTO t VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := runBackup([]string{"--data", src, "--out", out}); err != nil {
		t.Fatalf("backup: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if err := runRestore([]string{"--data", dst, "--in", out}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rdb, err := sql.Open("sqlite", "file:"+filepath.Join(dst, "litemlflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var n int
	if err := rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("restored rows = %d err=%v, want 3", n, err)
	}
}

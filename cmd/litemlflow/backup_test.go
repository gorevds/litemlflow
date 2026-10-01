package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

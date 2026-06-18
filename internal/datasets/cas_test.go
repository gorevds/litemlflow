package datasets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCASPutAndGet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewFilesystemCAS(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("hello, datasets v2")
	want := sha256.Sum256(body)
	wantHex := hex.EncodeToString(want[:])

	gotHash, size, err := store.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHex {
		t.Fatalf("hash mismatch: got %s want %s", gotHash, wantHex)
	}
	if size != int64(len(body)) {
		t.Errorf("size: got %d want %d", size, len(body))
	}

	// File is at <root>/<aa>/<bb...>.
	expectPath := filepath.Join(root, gotHash[:2], gotHash[2:])
	if _, err := os.Stat(expectPath); err != nil {
		t.Errorf("expected CAS file at %s: %v", expectPath, err)
	}

	rc, gsize, err := store.Get(gotHash)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if gsize != size {
		t.Errorf("Get size mismatch")
	}
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch: got %q", got)
	}

	ok, err := store.Has(gotHash)
	if err != nil || !ok {
		t.Errorf("Has should return true: %v %v", err, ok)
	}
}

func TestCASDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewFilesystemCAS(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("the same content")
	h1, _, err := store.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := store.Put(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("dedup hashes should match: %s vs %s", h1, h2)
	}
	// Walk the CAS root and count files (excluding any leftover .part).
	count := 0
	err = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".part") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected exactly one CAS file after two identical Puts, found %d", count)
	}
}

func TestCASMissingHash(t *testing.T) {
	t.Parallel()
	store, _ := NewFilesystemCAS(t.TempDir())
	missing := strings.Repeat("a", 64)
	ok, err := store.Has(missing)
	if err != nil || ok {
		t.Errorf("Has on missing: ok=%v err=%v", ok, err)
	}
	_, _, err = store.Get(missing)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Get on missing: want os.ErrNotExist, got %v", err)
	}
	if err := store.Delete(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Delete on missing: want os.ErrNotExist, got %v", err)
	}
}

func TestCASInvalidHash(t *testing.T) {
	t.Parallel()
	store, _ := NewFilesystemCAS(t.TempDir())
	cases := []string{
		"",                            // empty
		"too-short",                   // wrong length
		strings.Repeat("z", 64),       // non-hex char
		strings.Repeat("AA", 32),      // uppercase rejected
		strings.Repeat("a", 63) + "/", // path-traversal attempt
	}
	for _, h := range cases {
		if _, err := store.Has(h); err == nil {
			t.Errorf("Has(%q): expected validation error", h)
		}
		if _, _, err := store.Get(h); err == nil {
			t.Errorf("Get(%q): expected validation error", h)
		}
	}
}

func TestCASPutNilReader(t *testing.T) {
	t.Parallel()
	store, _ := NewFilesystemCAS(t.TempDir())
	if _, _, err := store.Put(nil); err == nil {
		t.Errorf("expected nil-reader error")
	}
}

func TestCASConcurrentDedup(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, _ := NewFilesystemCAS(root)
	body := bytes.Repeat([]byte("x"), 4096)

	const N = 16
	var wg sync.WaitGroup
	hashes := make([]string, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, _, err := store.Put(bytes.NewReader(body))
			hashes[i] = h
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("worker %d: %v", i, e)
		}
		if hashes[i] != hashes[0] {
			t.Errorf("worker %d hash diverged: %q vs %q", i, hashes[i], hashes[0])
		}
	}

	// Exactly one final file.
	count := 0
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasSuffix(p, ".part") {
			count++
		}
		return nil
	})
	if count != 1 {
		t.Errorf("expected 1 CAS file after %d concurrent Puts, got %d", N, count)
	}
}

func TestCASShardLayout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, _ := NewFilesystemCAS(root)
	bodies := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("gamma"),
	}
	for _, b := range bodies {
		_, _, err := store.Put(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
	}
	// Every CAS file should sit two levels deep: <root>/<aa>/<bb...>
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(p, ".part") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		segs := strings.Split(rel, string(filepath.Separator))
		if len(segs) != 2 {
			t.Errorf("file %q is not 2-level sharded; segs=%v", rel, segs)
		}
		if len(segs[0]) != 2 {
			t.Errorf("shard prefix should be 2 chars, got %q", segs[0])
		}
		return nil
	})
}

// Package datasets implements a content-addressed file store for the v1.2
// dataset versioning feature.
//
// Layout:
//
//	<root>/<aa>/<bb...>
//
// where <aa> is the first 2 hex chars of the SHA-256 and <bb...> is the
// remaining 62 chars. The 2-char shard prefix keeps directory fan-out
// bounded — most filesystems start to perform poorly with >10k files in a
// flat dir. With a 2-char hex prefix we get 256 sub-dirs and a typical
// fan-out of (N / 256) per dir; even at 1M datasets that's ~4k entries
// per shard, still comfortable.
//
// Writes are streaming + atomic:
//
//   - Compute SHA-256 while writing to "<final>.part" in the destination dir
//   - On success, rename(2) to the final path (POSIX rename is atomic when
//     source and destination are in the same filesystem, which they are
//     because the .part lives in the same shard dir).
//   - If the final path already exists (Has returned true mid-write,
//     concurrent uploader, …) we silently keep the existing file and
//     unlink the .part. Content-addressing makes this safe — the bytes
//     are by definition equal.
//
// All operations are safe for concurrent callers; the rename-on-finish
// pattern is the only synchronization needed.
package datasets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CAS is a filesystem content-addressed store.
//
// New users should call NewFilesystemCAS; the type is exported only so
// tests can stub it. The Store interface (below) is what callers depend
// on.
type CAS struct {
	root string
}

// Store is the small interface dataset upload / download paths use.
//
// We could re-use internal/artifact's Store interface but the semantics
// differ enough (CAS keys, no run-id scoping, no MoveToRun) that a fresh
// abstraction is clearer.
type Store interface {
	// Put streams r into the CAS, returning the SHA-256 hex digest and
	// the number of bytes written. The bytes are written to a temp file
	// first, then renamed atomically to the final CAS path. If a file at
	// that path already exists (same content), Put returns the existing
	// hash without overwriting.
	Put(r io.Reader) (hash string, size int64, err error)

	// Has returns true if the CAS has an object with the given hash.
	Has(hash string) (bool, error)

	// Get opens an object for reading. Caller must Close it.
	Get(hash string) (io.ReadCloser, int64, error)

	// Delete removes an object. Use sparingly — content addressing means
	// multiple datasets may reference the same blob; the store-side
	// caller is responsible for ensuring no live row references hash
	// before calling Delete. Returns os.ErrNotExist if missing.
	Delete(hash string) error
}

// NewFilesystemCAS returns a Store rooted at root, creating it if needed.
func NewFilesystemCAS(root string) (Store, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir cas root %q: %w", root, err)
	}
	return &CAS{root: root}, nil
}

// pathFor returns the absolute path for a given hash, plus the parent dir.
// It validates that hash is a 64-char lowercase-hex string so we never
// build a path from user input.
func (c *CAS) pathFor(hash string) (full, parent string, err error) {
	if len(hash) != 64 {
		return "", "", fmt.Errorf("invalid hash length %d (want 64)", len(hash))
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", "", fmt.Errorf("invalid hash char %q (lowercase hex required)", r)
		}
	}
	parent = filepath.Join(c.root, hash[:2])
	full = filepath.Join(parent, hash[2:])
	return full, parent, nil
}

// Put implements Store.
func (c *CAS) Put(r io.Reader) (string, int64, error) {
	if r == nil {
		return "", 0, errors.New("nil reader")
	}
	// Stream into a temp file under root, hashing as we go. We pick the
	// temp dir as the root itself so the rename to the final shard dir
	// stays inside one filesystem. (We can't write into the shard dir
	// before knowing the hash, so the rename has to span dirs — they're
	// always under the same root, which is on the same FS.)
	tmp, err := os.CreateTemp(c.root, ".upload-*.part")
	if err != nil {
		return "", 0, fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	n, err := io.Copy(mw, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", 0, fmt.Errorf("write: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	finalPath, parent, err := c.pathFor(hash)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	if err := os.MkdirAll(parent, 0o750); err != nil {
		cleanup()
		return "", 0, fmt.Errorf("mkdir shard: %w", err)
	}

	// Atomic rename. If the final path already exists (concurrent upload
	// of identical content, or a re-upload), the existing file is the
	// authoritative copy — we drop the temp.
	if _, err := os.Stat(finalPath); err == nil {
		cleanup()
		return hash, n, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return "", 0, fmt.Errorf("stat final: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// One legitimate race: another goroutine renamed between our
		// stat and rename. Re-check the destination — if it's there
		// now, we treat the rename as a no-op success.
		if _, statErr := os.Stat(finalPath); statErr == nil {
			cleanup()
			return hash, n, nil
		}
		cleanup()
		return "", 0, fmt.Errorf("rename: %w", err)
	}
	// Tighten file mode after the rename — CreateTemp gives 0o600 by default
	// on most platforms but some umasks open it wider. Be explicit.
	_ = os.Chmod(finalPath, 0o640)
	return hash, n, nil
}

// Has implements Store.
func (c *CAS) Has(hash string) (bool, error) {
	full, _, err := c.pathFor(hash)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// Get implements Store.
func (c *CAS) Get(hash string) (io.ReadCloser, int64, error) {
	full, _, err := c.pathFor(hash)
	if err != nil {
		return nil, 0, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// Delete implements Store.
func (c *CAS) Delete(hash string) error {
	full, _, err := c.pathFor(hash)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

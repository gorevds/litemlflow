// Package artifact stores blob files associated with runs.
//
// v0.1 ships a filesystem backend. The interface is the same any S3-compat
// or GCS plugin would implement. Plugins are out-of-process (gRPC subprocesses)
// and are not in the v0.1 binary.
package artifact

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Errors.
var (
	ErrNotFound       = errors.New("artifact not found")
	ErrInvalidPath    = errors.New("invalid artifact path")
	ErrPayloadTooBig  = errors.New("artifact payload exceeds max size")
)

// Store is the artifact persistence interface.
type Store interface {
	// Upload writes an artifact at runID/relPath, replacing any existing file.
	// maxSize enforces a per-upload byte cap (set to 0 to disable).
	Upload(runID, relPath string, r io.Reader, maxSize int64) error
	// Open returns a reader for an existing artifact and its size.
	Open(runID, relPath string) (io.ReadCloser, int64, error)
	// Delete removes a file or directory recursively.
	Delete(runID, relPath string) error
	// List returns immediate children of a directory under runID/dir.
	List(runID, dir string) ([]ListEntry, error)
}

// ListEntry describes one element of an artifact directory.
type ListEntry struct {
	Path  string // path relative to the run's artifact root
	IsDir bool
	Size  int64
}

// FilesystemStore stores artifacts under root/<runID>/<relPath>.
type FilesystemStore struct {
	Root string
}

// NewFilesystemStore returns a filesystem-backed artifact store rooted at
// the given directory. The directory is created if it doesn't exist.
func NewFilesystemStore(root string) (*FilesystemStore, error) {
	if root == "" {
		return nil, errors.New("artifact root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir artifact root: %w", err)
	}
	return &FilesystemStore{Root: root}, nil
}

// Upload writes an artifact under the run's directory.
//
// Path traversal is prevented by:
//   1. Cleaning the path with filepath.Clean.
//   2. Rejecting absolute paths and any cleaned path that escapes the root
//      via the prefix check after Abs resolution.
func (f *FilesystemStore) Upload(runID, relPath string, r io.Reader, maxSize int64) error {
	abs, err := f.absoluteFor(runID, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp := abs + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(tmp) // no-op if rename succeeded
	}()
	var src io.Reader = r
	if maxSize > 0 {
		// LimitReader returns N+1 bytes' worth of work — we detect overflow
		// by checking if there's data left after the cap.
		src = io.LimitReader(r, maxSize+1)
	}
	written, err := io.Copy(out, src)
	if err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}
	if maxSize > 0 && written > maxSize {
		return ErrPayloadTooBig
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Open returns a reader for the file at runID/relPath.
func (f *FilesystemStore) Open(runID, relPath string) (io.ReadCloser, int64, error) {
	abs, err := f.absoluteFor(runID, relPath)
	if err != nil {
		return nil, 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	if st.IsDir() {
		return nil, 0, ErrInvalidPath
	}
	fp, err := os.Open(abs)
	if err != nil {
		return nil, 0, err
	}
	return fp, st.Size(), nil
}

// Delete removes a file or directory recursively, scoped to the run.
func (f *FilesystemStore) Delete(runID, relPath string) error {
	abs, err := f.absoluteFor(runID, relPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	return os.RemoveAll(abs)
}

// List returns the immediate children of runID/dir.
func (f *FilesystemStore) List(runID, dir string) ([]ListEntry, error) {
	abs, err := f.absoluteFor(runID, dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return []ListEntry{}, nil
		}
		return nil, err
	}
	if !st.IsDir() {
		return []ListEntry{{Path: dir, IsDir: false, Size: st.Size()}}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]ListEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		var rel string
		if dir == "" || dir == "." {
			rel = e.Name()
		} else {
			rel = filepath.ToSlash(filepath.Join(dir, e.Name()))
		}
		entry := ListEntry{Path: rel, IsDir: info.IsDir()}
		if !info.IsDir() {
			entry.Size = info.Size()
		}
		out = append(out, entry)
	}
	return out, nil
}

// absoluteFor resolves runID + relPath to an absolute path under f.Root,
// returning ErrInvalidPath if the resolved path would escape the root or
// the run's subdir.
func (f *FilesystemStore) absoluteFor(runID, relPath string) (string, error) {
	if runID == "" {
		return "", ErrInvalidPath
	}
	if strings.ContainsAny(runID, "/\\") {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean("/" + relPath) // e.g. "/foo/bar"
	if clean == "/" {
		clean = ""
	}
	full := filepath.Join(f.Root, runID, clean)
	absRoot, err := filepath.Abs(filepath.Join(f.Root, runID))
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absFull+string(filepath.Separator), absRoot+string(filepath.Separator)) && absFull != absRoot {
		return "", ErrInvalidPath
	}
	return absFull, nil
}

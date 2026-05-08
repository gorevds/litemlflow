package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// notesStore reuses the newStore helper defined in sqlite_test.go (same package).
func notesStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "notes_test.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestRunNotes_SetGetDelete(t *testing.T) {
	s := notesStore(t)
	ctx := context.Background()

	// Create an experiment + run.
	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "notes-test-exp"})
	if err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ExperimentID: expID}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runID := run.ID

	// GetRunNote on a run with no note → ErrNotFound.
	_, _, _, err = s.GetRunNote(ctx, runID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Set a note.
	const note1 = "# My note\n\nThis is *important*."
	if err := s.SetRunNote(ctx, runID, note1, "alice"); err != nil {
		t.Fatal(err)
	}

	// Read it back.
	content, by, at, err := s.GetRunNote(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if content != note1 {
		t.Errorf("content mismatch: got %q, want %q", content, note1)
	}
	if by != "alice" {
		t.Errorf("updated_by: got %q, want %q", by, "alice")
	}
	if at <= 0 {
		t.Errorf("updated_at should be positive, got %d", at)
	}

	// Upsert — overwrite with new content.
	const note2 = "Updated note"
	if err := s.SetRunNote(ctx, runID, note2, "bob"); err != nil {
		t.Fatal(err)
	}
	content2, by2, _, err := s.GetRunNote(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if content2 != note2 {
		t.Errorf("updated content: got %q, want %q", content2, note2)
	}
	if by2 != "bob" {
		t.Errorf("updated_by after upsert: got %q, want %q", by2, "bob")
	}

	// Delete via empty content.
	if err := s.SetRunNote(ctx, runID, "", "bob"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = s.GetRunNote(ctx, runID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: expected ErrNotFound, got %v", err)
	}
}

func TestRunNotes_FKCascadeOnRunDelete(t *testing.T) {
	s := notesStore(t)
	ctx := context.Background()

	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "cascade-test-exp"})
	if err != nil {
		t.Fatal(err)
	}
	run := &model.Run{ExperimentID: expID}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	runID := run.ID

	if err := s.SetRunNote(ctx, runID, "cascade note", "alice"); err != nil {
		t.Fatal(err)
	}

	// Soft-delete the run via lifecycle (SetRunLifecycle doesn't remove the row,
	// so we need to directly delete the run row to test FK cascade).
	// We use the underlying DB to DELETE the runs row — which should cascade to
	// run_notes via ON DELETE CASCADE.
	db := s.DB()
	if _, err := db.ExecContext(ctx, `DELETE FROM runs WHERE id = ?`, runID); err != nil {
		t.Fatal("delete run:", err)
	}

	// Note should now be gone due to ON DELETE CASCADE.
	_, _, _, err = s.GetRunNote(ctx, runID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after cascade delete: expected ErrNotFound, got %v", err)
	}
}

func TestRunNotes_ErrNotFoundOnBadRun(t *testing.T) {
	s := notesStore(t)
	ctx := context.Background()

	err := s.SetRunNote(ctx, "nonexistent-run-id", "content", "alice")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run, got %v", err)
	}
}

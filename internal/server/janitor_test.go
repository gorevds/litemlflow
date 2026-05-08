package server_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/server"
	"github.com/gorevds/litemlflow/internal/store"
)

func openJanitorStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "janitor_test.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestJanitorArchivesStaleRuns(t *testing.T) {
	ctx := context.Background()
	st := openJanitorStore(t)

	// Create experiment.
	expID, err := st.CreateExperiment(ctx, &model.Experiment{Name: "janitor-test"})
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	// Create a "stale" run: start_time 25h ago.
	staleRun := &model.Run{
		ExperimentID: expID,
		Name:         "stale-run",
		StartTime:    time.Now().Add(-25 * time.Hour).UnixMilli(),
	}
	if err := st.CreateRun(ctx, staleRun); err != nil {
		t.Fatalf("create stale run: %v", err)
	}

	// Create a fresh run: start_time 1h ago.
	freshRun := &model.Run{
		ExperimentID: expID,
		Name:         "fresh-run",
		StartTime:    time.Now().Add(-1 * time.Hour).UnixMilli(),
	}
	if err := st.CreateRun(ctx, freshRun); err != nil {
		t.Fatalf("create fresh run: %v", err)
	}

	// Run ArchiveStaleRuns with staleBefore = now - 24h.
	staleBefore := time.Now().Add(-24 * time.Hour).UnixMilli()
	n, err := st.ArchiveStaleRuns(ctx, staleBefore)
	if err != nil {
		t.Fatalf("ArchiveStaleRuns: %v", err)
	}
	if n != 1 {
		t.Errorf("archived count: got %d want 1", n)
	}

	// Stale run should be FAILED.
	got, err := st.GetRun(ctx, staleRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusFailed {
		t.Errorf("stale run status: got %q want FAILED", got.Status)
	}
	if got.EndTime == nil {
		t.Error("stale run end_time should be set")
	}

	// Stale run should have the lmf.auto_archived=stale tag.
	tags, err := st.GetTags(ctx, staleRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tag := range tags {
		if tag.Key == "lmf.auto_archived" && tag.Value == "stale" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("lmf.auto_archived=stale tag not found; tags=%v", tags)
	}

	// Fresh run should still be RUNNING.
	gotFresh, err := st.GetRun(ctx, freshRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFresh.Status != model.StatusRunning {
		t.Errorf("fresh run status: got %q want RUNNING", gotFresh.Status)
	}
}

func TestJanitorDoesNotArchiveFinishedRuns(t *testing.T) {
	ctx := context.Background()
	st := openJanitorStore(t)

	expID, err := st.CreateExperiment(ctx, &model.Experiment{Name: "janitor-finished-test"})
	if err != nil {
		t.Fatal(err)
	}

	// Create an old but already-FINISHED run.
	finishedRun := &model.Run{
		ExperimentID: expID,
		Name:         "old-finished",
		StartTime:    time.Now().Add(-48 * time.Hour).UnixMilli(),
	}
	if err := st.CreateRun(ctx, finishedRun); err != nil {
		t.Fatal(err)
	}
	statusFinished := model.StatusFinished
	if err := st.UpdateRun(ctx, finishedRun.ID, &statusFinished, nil, nil); err != nil {
		t.Fatal(err)
	}

	staleBefore := time.Now().Add(-24 * time.Hour).UnixMilli()
	n, err := st.ArchiveStaleRuns(ctx, staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("should not archive finished run, got count=%d", n)
	}
}

func TestStartJanitor(t *testing.T) {
	st := openJanitorStore(t)
	ctx := context.Background()

	expID, err := st.CreateExperiment(ctx, &model.Experiment{Name: "janitor-goroutine-test"})
	if err != nil {
		t.Fatal(err)
	}
	staleRun := &model.Run{
		ExperimentID: expID,
		StartTime:    time.Now().Add(-25 * time.Hour).UnixMilli(),
	}
	if err := st.CreateRun(ctx, staleRun); err != nil {
		t.Fatal(err)
	}

	// Run janitor with very short interval.
	janitorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.StartJanitor(janitorCtx, st, 50*time.Millisecond, 24*time.Hour, nil)

	// Wait for janitor to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := st.GetRun(context.Background(), staleRun.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == model.StatusFailed {
			return // success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("janitor did not archive stale run within 2s")
}

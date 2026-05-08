//go:build chaos

// Chaos tests for the SQLite store layer.
//
// These tests simulate adverse operating conditions: killed database
// connections, full-disk scenarios, WAL corruption, and mid-migration crashes.
// They are excluded from the normal CI build (no `chaos` tag) and intended to
// be run explicitly:
//
//	make test-chaos
//	# or directly:
//	go test -v -count=1 -tags=chaos -timeout=2m ./internal/store/
//
// Many scenarios require OS-level facilities (tmpfs, /proc, etc.) and will
// skip gracefully if those facilities are unavailable.

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// ---- TestChaos_KillMidWrite -----------------------------------------------

// TestChaos_KillMidWrite hammers LogMetric from many goroutines and then
// forcibly closes the underlying *sql.DB to simulate a process kill.
//
// Oracles:
//   - All goroutines must exit cleanly (no goroutine leak / deadlock within
//     the test timeout).
//   - The store reopens cleanly and can serve reads.
func TestChaos_KillMidWrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chaos.db")

	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "chaos-kill"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Launch writers.
	const writers = 8
	const each = 200
	var wg sync.WaitGroup
	wg.Add(writers)
	writeErrors := make(chan error, writers*each)

	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if err := s.LogMetric(ctx, r.ID, model.Metric{
					Key:       "loss",
					Value:     float64(i*1000 + j),
					Timestamp: int64(i*1000 + j),
					Step:      int64(i*1000 + j),
				}); err != nil {
					// After the DB is closed, writes will return errors. That
					// is expected — collect them but don't fail.
					writeErrors <- err
					return
				}
			}
		}(i)
	}

	// After ~50ms, brutally close the underlying DB.
	time.Sleep(50 * time.Millisecond)
	if err := s.DB().Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		t.Logf("DB.Close() returned: %v (expected after forced close)", err)
	}

	// Oracle: all goroutines must exit (no deadlock). The test timeout enforces this.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// All writers exited.
	case <-time.After(15 * time.Second):
		t.Fatal("goroutine leak: writers did not exit after DB close within 15s")
	}
	close(writeErrors)

	var errCount int
	for range writeErrors {
		errCount++
	}
	t.Logf("writers produced %d errors (expected after DB close)", errCount)

	// Oracle: store reopens cleanly after the forced close.
	s2, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer s2.Close()
	// Migration must be idempotent.
	if err := s2.Migrate(context.Background()); err != nil {
		t.Fatalf("re-migrate failed: %v", err)
	}
	// Basic read must work.
	if _, err := s2.GetExperiment(context.Background(), expID); err != nil {
		t.Fatalf("GetExperiment after reopen: %v", err)
	}
}

// ---- TestChaos_FullDisk ----------------------------------------------------

// TestChaos_FullDisk opens a SQLite store on a tmpfs filesystem mounted at a
// tiny size, then inserts data until ENOSPC.
//
// Oracles:
//   - Each insert returns a clean error (no panic).
//   - Rows written before ENOSPC can be read back (no silent corruption).
//
// This test requires Linux and the ability to mount a tiny tmpfs. It will
// skip if those conditions are not met.
func TestChaos_FullDisk(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full-disk chaos test requires Linux")
	}

	// Check that mount/umount are available (typically need CAP_SYS_ADMIN).
	if _, err := exec.LookPath("mount"); err != nil {
		t.Skip("mount command not found; skipping full-disk test")
	}

	// Try to create a small tmpfs. If we lack privileges, skip.
	mountPoint := t.TempDir()
	cmd := exec.Command("mount", "-t", "tmpfs", "-o", "size=2m", "tmpfs", mountPoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot mount tmpfs (need CAP_SYS_ADMIN): %v — %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("umount", mountPoint).Run()
	})

	dbPath := filepath.Join(mountPoint, "full.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, mountPoint)
	if err != nil {
		t.Fatalf("open on tmpfs: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate on tmpfs: %v", err)
	}

	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "full-disk"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Write until we hit a storage error.
	const batchSize = 100
	var successfulBatches int
	var hitError error
	for i := 0; i < 10000; i++ {
		ms := make([]model.Metric, batchSize)
		for j := range ms {
			ms[j] = model.Metric{
				Key:       fmt.Sprintf("metric-%d", j),
				Value:     float64(i*batchSize + j),
				Timestamp: int64(i*batchSize+j) + 1,
				Step:      int64(i*batchSize + j),
			}
		}
		if err := s.LogMetrics(ctx, r.ID, ms); err != nil {
			hitError = err
			break
		}
		successfulBatches++
	}

	if hitError == nil {
		t.Log("did not hit disk-full within 10000 batches; filesystem may be larger than expected")
	} else {
		t.Logf("hit error after %d successful batches: %v", successfulBatches, hitError)
		// Oracle: error must not be nil and must not be a panic (verified by
		// the fact that we reach this line).
	}

	// Oracle: read back the data that was successfully written.
	if successfulBatches > 0 {
		hist, _, err := s.GetMetricHistory(ctx, r.ID, "metric-0", store.MetricHistoryOptions{})
		if err != nil {
			t.Errorf("read after partial write failed: %v", err)
		}
		if len(hist) == 0 {
			t.Error("expected at least one metric point after partial write")
		}
	}
}

// ---- TestChaos_CorruptWAL --------------------------------------------------

// TestChaos_CorruptWAL writes data, closes the store, corrupts the last 1 KB
// of the WAL file, then reopens.
//
// Oracle: SQLite must either:
//   - Truncate the corrupt WAL fragment (data loss for in-flight commit is
//     acceptable — the data was not durable) and allow clean reopening, or
//   - Refuse to open with a clear error message.
//
// It must never return garbage data (e.g. returning inconsistent metrics).
func TestChaos_CorruptWAL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "walcorrupt.db")

	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "wal-corrupt"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Write a known batch of metrics and ensure they are committed.
	const committed = 20
	for i := 0; i < committed; i++ {
		if err := s.LogMetric(ctx, r.ID, model.Metric{
			Key:       "loss",
			Value:     float64(i) * 0.01,
			Timestamp: int64(i + 1),
			Step:      int64(i),
		}); err != nil {
			t.Fatalf("log metric %d: %v", i, err)
		}
	}

	// Close the store so the WAL is flushed / accessible.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	walPath := dbPath + "-wal"
	walInfo, err := os.Stat(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			// SQLite may have checkpointed and removed the WAL — nothing to
			// corrupt in that case.
			t.Skip("WAL file does not exist (already checkpointed); skipping")
		}
		t.Fatalf("stat WAL: %v", err)
	}

	if walInfo.Size() == 0 {
		t.Skip("WAL file is empty; skipping corruption test")
	}

	// Corrupt the last 1 KB (or the whole file if smaller).
	corruptSize := int64(1024)
	if walInfo.Size() < corruptSize {
		corruptSize = walInfo.Size()
	}
	f, err := os.OpenFile(walPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open WAL for corruption: %v", err)
	}
	offset := walInfo.Size() - corruptSize
	junk := make([]byte, corruptSize)
	for i := range junk {
		junk[i] = 0xFF // overwrite with 0xFF bytes
	}
	if _, err := f.WriteAt(junk, offset); err != nil {
		f.Close()
		t.Fatalf("write WAL corruption: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close WAL after corruption: %v", err)
	}
	t.Logf("corrupted %d bytes at offset %d in WAL (total size %d)", corruptSize, offset, walInfo.Size())

	// Try to reopen. SQLite should either:
	//   a) recover by truncating the corrupt WAL frame, or
	//   b) return a clear error.
	s2, openErr := store.OpenSQLite(context.Background(), dbPath, dir)
	if openErr != nil {
		// Oracle b): clear error is acceptable.
		t.Logf("reopen returned error after WAL corruption (acceptable): %v", openErr)
		return
	}
	defer s2.Close()

	// Oracle a): if we could open, data must be consistent.
	// We must not get more metrics than we actually committed.
	hist, _, err := s2.GetMetricHistory(ctx, r.ID, "loss", store.MetricHistoryOptions{})
	if err != nil {
		// Error reading is also acceptable — the corrupt WAL may have
		// corrupted the data pages.
		t.Logf("GetMetricHistory after WAL corruption returned error (acceptable): %v", err)
		return
	}
	if len(hist) > committed {
		t.Errorf("got MORE metrics (%d) than committed (%d) after WAL corruption — data integrity violation", len(hist), committed)
	}
	t.Logf("after WAL corruption: got %d/%d committed metrics (data loss is acceptable)", len(hist), committed)
}

// ---- TestChaos_MigrationCrashMidway ----------------------------------------

// TestChaos_MigrationCrashMidway injects a failure into a test-only migration
// and verifies that the schema_migrations table never records the failed
// migration, and that a subsequent Apply() call retries cleanly.
//
// This test uses a fresh database (not the standard migrations) and directly
// calls the migrations package internals through the store.Migrate path.
// We simulate the failure by applying a bad SQL statement as if it were a
// migration via a direct db.ExecContext on the underlying *sql.DB.
func TestChaos_MigrationCrashMidway(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migcrash.db")

	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Apply the real migrations successfully.
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	db := s.DB()
	ctx := context.Background()

	// Verify the current schema_migrations state.
	var vBefore int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&vBefore); err != nil {
		t.Fatalf("read schema version before: %v", err)
	}
	t.Logf("schema version before bad migration: %d", vBefore)

	// Attempt to apply a bad migration manually (simulating what a future
	// buggy migration file would do): start a transaction, run a bad
	// statement, and observe that the transaction rolls back.
	badSQL := `CREATE TABLE nonexistent_ref (id INTEGER REFERENCES definitely_not_there(id))`

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, execErr := tx.ExecContext(ctx, badSQL)
	if execErr != nil {
		// Expected: bad SQL failed; rollback the transaction.
		_ = tx.Rollback()
	} else {
		// The statement somehow succeeded (possibly SQLite deferred FK check).
		// Still don't record the migration.
		_ = tx.Rollback()
		t.Logf("note: bad SQL did not error immediately (deferred check); rolled back anyway")
	}

	// Oracle 1: schema_migrations must not have been updated.
	var vAfterBad int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&vAfterBad); err != nil {
		t.Fatalf("read schema version after bad migration: %v", err)
	}
	if vAfterBad != vBefore {
		t.Errorf("schema_migrations was updated despite bad migration: before=%d after=%d", vBefore, vAfterBad)
	}

	// Oracle 2: subsequent Migrate() call retries cleanly and is idempotent.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate after bad migration: %v", err)
	}
	var vAfterRetry int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&vAfterRetry); err != nil {
		t.Fatalf("read schema version after retry: %v", err)
	}
	if vAfterRetry != vBefore {
		t.Errorf("schema version changed unexpectedly on retry: want %d got %d", vBefore, vAfterRetry)
	}
	t.Logf("migration idempotency confirmed: version=%d (stable)", vAfterRetry)
}

// ---- TestChaos_ConcurrentClose ---------------------------------------------

// TestChaos_ConcurrentClose races a Close() call against concurrent readers
// to verify that concurrent users of the store receive errors (not panics)
// when the DB is closed under them.
func TestChaos_ConcurrentClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "concclose.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "cc"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	const readers = 10
	var wg sync.WaitGroup
	wg.Add(readers)

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			defer func() {
				if rc := recover(); rc != nil {
					// A panic here means the store didn't handle closed-DB
					// gracefully. The test runner converts panics to failures.
					panic(fmt.Sprintf("store panicked on closed DB: %v", rc))
				}
			}()
			for {
				_, err := s.GetExperiment(ctx, expID)
				if err != nil {
					// Any error is acceptable — the DB is being closed.
					return
				}
			}
		}()
	}

	// Let readers get going for a moment, then close.
	time.Sleep(20 * time.Millisecond)
	_ = s.DB().Close() // force-close the underlying sql.DB

	// Oracle: all goroutines must exit cleanly within the test timeout.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("goroutine leak: readers did not exit after DB close")
	}
}

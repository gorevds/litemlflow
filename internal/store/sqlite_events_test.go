// v1.5 time-travel store tests.
//
// Cover the event log + GetRunAsOf replay across the supported mutations:
// run UPDATE (status, end_time, name), run lifecycle, parent_run_id, tag
// set, tag delete. Also cover the "run did not exist at T" → ErrNotFound
// path, and the SET→DELETE→SET tag history that exercises the undo logic.
package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// TestEventsRunUpdateReplay logs a run, mutates name+status+end_time
// after a known timestamp, and verifies as-of returns the pre-mutation state.
func TestEventsRunUpdateReplay(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-update")

	r := &model.Run{ExperimentID: expID, Name: "original", Status: model.StatusRunning, StartTime: 1000}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Capture the boundary timestamp.
	tBefore := time.Now().UnixMilli()
	time.Sleep(5 * time.Millisecond)

	finished := model.StatusFinished
	endTime := tBefore + 100
	newName := "renamed-after"
	if err := st.UpdateRun(ctx, r.ID, &finished, &endTime, &newName); err != nil {
		t.Fatal(err)
	}

	// Current state: renamed + finished.
	cur, err := st.GetRun(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Name != "renamed-after" || cur.Status != model.StatusFinished {
		t.Fatalf("current state wrong: name=%q status=%q", cur.Name, cur.Status)
	}

	// As-of tBefore: should see the pre-update state (original + RUNNING).
	asOf, _, err := st.GetRunAsOf(ctx, r.ID, tBefore)
	if err != nil {
		t.Fatal(err)
	}
	if asOf.Name != "original" {
		t.Errorf("as-of name: got %q want %q", asOf.Name, "original")
	}
	if asOf.Status != model.StatusRunning {
		t.Errorf("as-of status: got %q want %q", asOf.Status, model.StatusRunning)
	}
	if asOf.EndTime != nil {
		t.Errorf("as-of end_time: got %v want nil (run was not finished yet)", *asOf.EndTime)
	}
}

// TestEventsRunCreatedAfter ensures querying a run as-of a time before
// its creation returns ErrNotFound (does not surface a phantom run).
func TestEventsRunCreatedAfter(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-created-after")

	r := &model.Run{ExperimentID: expID, Name: "future", StartTime: 5000}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Query at t=4999 — before start_time.
	_, _, err := st.GetRunAsOf(ctx, r.ID, 4999)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for as-of before start_time, got %v", err)
	}

	// Query at t=5000 — exactly at start_time, should succeed.
	got, _, err := st.GetRunAsOf(ctx, r.ID, 5000)
	if err != nil {
		t.Fatalf("expected ok at as-of=start_time, got %v", err)
	}
	if got.Name != "future" {
		t.Errorf("name: got %q", got.Name)
	}
}

// TestEventsTagSetDeleteSetReplay verifies the tag undo logic across a
// SET → DELETE → SET sequence on the same key. This is the case the
// applyTagUndo logic explicitly handles via the `before` field.
func TestEventsTagSetDeleteSetReplay(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-tags")
	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: 1000}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// SET k=v1.
	if err := st.SetTag(ctx, r.ID, model.KV{Key: "k", Value: "v1"}); err != nil {
		t.Fatal(err)
	}
	tAfterFirstSet := time.Now().UnixMilli()
	time.Sleep(5 * time.Millisecond)

	// DELETE k.
	if err := st.DeleteTag(ctx, r.ID, "k"); err != nil {
		t.Fatal(err)
	}
	tAfterDelete := time.Now().UnixMilli()
	time.Sleep(5 * time.Millisecond)

	// SET k=v2.
	if err := st.SetTag(ctx, r.ID, model.KV{Key: "k", Value: "v2"}); err != nil {
		t.Fatal(err)
	}

	// As-of after first SET → tag should be v1.
	_, tags, err := st.GetRunAsOf(ctx, r.ID, tAfterFirstSet)
	if err != nil {
		t.Fatal(err)
	}
	if got := findTag(tags, "k"); got != "v1" {
		t.Errorf("as-of after first set: tag k=%q want v1", got)
	}

	// As-of after delete → tag should be absent.
	_, tags, err = st.GetRunAsOf(ctx, r.ID, tAfterDelete)
	if err != nil {
		t.Fatal(err)
	}
	if findTag(tags, "k") != "" {
		t.Errorf("as-of after delete: expected k absent, got %v", tags)
	}

	// As-of now → tag should be v2 (current state).
	now := time.Now().UnixMilli()
	_, tags, err = st.GetRunAsOf(ctx, r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := findTag(tags, "k"); got != "v2" {
		t.Errorf("as-of now: tag k=%q want v2", got)
	}
}

// TestEventsRunLifecycleReplay logs a run, soft-deletes it, and verifies
// the as-of state reflects the active stage before the deletion.
func TestEventsRunLifecycleReplay(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-lifecycle")
	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: 1000}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	tBefore := time.Now().UnixMilli()
	time.Sleep(5 * time.Millisecond)

	if err := st.SetRunLifecycle(ctx, r.ID, model.LifecycleDeleted); err != nil {
		t.Fatal(err)
	}

	// Current: deleted. As-of tBefore: active.
	asOf, _, err := st.GetRunAsOf(ctx, r.ID, tBefore)
	if err != nil {
		t.Fatal(err)
	}
	if asOf.LifecycleStage != model.LifecycleActive {
		t.Errorf("as-of lifecycle: got %q want active", asOf.LifecycleStage)
	}

	now := time.Now().UnixMilli()
	asOf, _, err = st.GetRunAsOf(ctx, r.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if asOf.LifecycleStage != model.LifecycleDeleted {
		t.Errorf("as-of now lifecycle: got %q want deleted", asOf.LifecycleStage)
	}
}

// TestEventsMetricsAsOfReducesPerKey guards independent-review C1: when
// the latest metric point post-dates as_of but an earlier observation
// predates it, the as-of reduction must surface the EARLIER point — not
// drop the key entirely.
func TestEventsMetricsAsOfReducesPerKey(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-metric-asof")
	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: 500}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Three points on "loss": ts=1000 v=1.0, ts=2000 v=0.5, ts=3000 v=0.1.
	for i, ts := range []int64{1000, 2000, 3000} {
		if err := st.LogMetric(ctx, r.ID, model.Metric{
			Key: "loss", Value: float64(3-i) / 10.0, Timestamp: ts, Step: int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// as-of=2500 should pick the ts=2000 point, NOT drop "loss" entirely.
	got, err := st.GetLatestMetricsAsOf(ctx, r.ID, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(got))
	}
	if got[0].Key != "loss" || got[0].Timestamp != 2000 {
		t.Errorf("expected loss@ts=2000, got %+v", got[0])
	}
}

// TestEventsRunAsOfWorkspaceIsolation guards independent-review H3: a
// run_id from another workspace must not leak via GetRunAsOfInWorkspace.
func TestEventsRunAsOfWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	mustCreateWorkspace(t, st, "ws-a-asof")
	mustCreateWorkspace(t, st, "ws-b-asof")
	expA, err := st.CreateExperiment(ctx, &model.Experiment{Name: "ws-a-exp", WorkspaceID: "ws-a-asof"})
	if err != nil {
		t.Fatal(err)
	}
	r := &model.Run{ExperimentID: expA, Name: "secret", StartTime: 1000}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Caller in ws-b-asof tries to time-travel a ws-a-asof run.
	_, _, err = st.GetRunAsOfInWorkspace(ctx, r.ID, "ws-b-asof", time.Now().UnixMilli())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-workspace as-of, got %v", err)
	}

	// Same caller in ws-a-asof works.
	_, _, err = st.GetRunAsOfInWorkspace(ctx, r.ID, "ws-a-asof", time.Now().UnixMilli())
	if err != nil {
		t.Errorf("ws-a-asof caller should succeed, got %v", err)
	}
}

// TestEventsPruneBeforeRemovesOlderRows guards the v1.5 stable janitor:
// PruneEventsBefore must delete rows older than the cutoff and leave
// newer rows alone.
func TestEventsPruneBeforeRemovesOlderRows(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-prune")
	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: 1}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Tag set → emits event A.
	if err := st.SetTag(ctx, r.ID, model.KV{Key: "k", Value: "v1"}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UnixMilli()
	time.Sleep(10 * time.Millisecond)

	// Tag delete → emits event B.
	if err := st.DeleteTag(ctx, r.ID, "k"); err != nil {
		t.Fatal(err)
	}

	// Prune everything older than cutoff. Should drop event A.
	n, err := st.PruneEventsBefore(ctx, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("expected >=1 row pruned, got %d", n)
	}

	// Replay from current: event A is gone, only event B (tag_delete)
	// remains. As-of before cutoff would now over-report the current tag
	// set (we lost the audit trail for the SET) — that's the intentional
	// tradeoff of pruning, documented in the migration.
	asOf, tags, err := st.GetRunAsOf(ctx, r.ID, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if asOf == nil {
		t.Fatal("run should still exist after prune")
	}
	if len(tags) != 0 {
		t.Errorf("after delete, expected no tags, got %v", tags)
	}
}

// TestEventsReplayLimitExceeded guards independent-review M3: a run
// with more events than MaxEventsPerReplay returns ErrReplayLimitExceeded
// instead of slurping unbounded rows.
//
// This test cheats by inserting raw event rows to avoid actually doing
// 50k mutations — the production code path's cap behavior is identical
// regardless of how the rows got there.
func TestEventsReplayLimitExceeded(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "events-limit")
	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: 1}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	// Insert MaxEventsPerReplay+10 events directly.
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events(ts_ms, kind, entity_type, entity_id, payload)
		VALUES (?, 'tag_set', 'run', ?, '{}')
	`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxEventsPerReplay+10; i++ {
		if _, err := stmt.ExecContext(ctx, int64(i+1), r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	_, _, err = st.GetRunAsOf(ctx, r.ID, time.Now().UnixMilli())
	if !errors.Is(err, store.ErrReplayLimitExceeded) {
		t.Errorf("expected ErrReplayLimitExceeded, got %v", err)
	}
}

func findTag(tags []model.KV, key string) string {
	for _, t := range tags {
		if t.Key == key {
			return t.Value
		}
	}
	return ""
}

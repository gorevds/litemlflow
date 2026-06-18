package store_test

// Guards the P1 follow-up (independent-review): run mutations and their
// time-travel event rows must commit atomically. Previously the mutation ran
// on s.db and the event INSERT was a separate, best-effort write — a crash
// between them lost the event, which silently corrupts ?as_of= replay (the
// missing event is never undone, so the historical state wrongly includes the
// mutation). The fix wraps mutation+event in one transaction.
//
// We force the event INSERT to fail by dropping the events table after the run
// exists, then assert the mutation both errors AND leaves the run unchanged
// (proving the write rolled back rather than half-applying).

import (
	"context"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

func TestRunMutationsAtomicWithEventLog(t *testing.T) {
	ctx := context.Background()

	tagValue := func(t *testing.T, st interface {
		GetTags(context.Context, string) ([]model.KV, error)
	}, runID, key string) (string, bool) {
		t.Helper()
		kvs, err := st.GetTags(ctx, runID)
		if err != nil {
			t.Fatalf("get tags: %v", err)
		}
		for _, kv := range kvs {
			if kv.Key == key {
				return kv.Value, true
			}
		}
		return "", false
	}

	t.Run("UpdateRun", func(t *testing.T) {
		st := newStore(t)
		exp := mustCreateExpInStore(t, st, "atomic-update")
		r := &model.Run{ExperimentID: exp, Name: "r", StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx, "DROP TABLE events"); err != nil {
			t.Fatalf("drop events: %v", err)
		}
		newStatus := model.StatusFinished
		if err := st.UpdateRun(ctx, r.ID, &newStatus, nil, nil); err == nil {
			t.Fatal("expected UpdateRun to fail when the event write fails")
		}
		got, err := st.GetRun(ctx, r.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if got.Status != "RUNNING" {
			t.Errorf("UpdateRun not rolled back: status=%q want RUNNING", got.Status)
		}
	})

	t.Run("SetRunLifecycle", func(t *testing.T) {
		st := newStore(t)
		exp := mustCreateExpInStore(t, st, "atomic-lifecycle")
		r := &model.Run{ExperimentID: exp, Name: "r", StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx, "DROP TABLE events"); err != nil {
			t.Fatalf("drop events: %v", err)
		}
		if err := st.SetRunLifecycle(ctx, r.ID, model.LifecycleDeleted); err == nil {
			t.Fatal("expected SetRunLifecycle to fail when the event write fails")
		}
		got, err := st.GetRun(ctx, r.ID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if got.LifecycleStage != "active" {
			t.Errorf("SetRunLifecycle not rolled back: stage=%q want active", got.LifecycleStage)
		}
	})

	t.Run("SetTag", func(t *testing.T) {
		st := newStore(t)
		exp := mustCreateExpInStore(t, st, "atomic-settag")
		r := &model.Run{ExperimentID: exp, Name: "r", StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx, "DROP TABLE events"); err != nil {
			t.Fatalf("drop events: %v", err)
		}
		if err := st.SetTag(ctx, r.ID, model.KV{Key: "k", Value: "v"}); err == nil {
			t.Fatal("expected SetTag to fail when the event write fails")
		}
		if _, ok := tagValue(t, st, r.ID, "k"); ok {
			t.Error("SetTag not rolled back: tag k is present")
		}
	})

	t.Run("DeleteTag", func(t *testing.T) {
		st := newStore(t)
		exp := mustCreateExpInStore(t, st, "atomic-deltag")
		r := &model.Run{ExperimentID: exp, Name: "r", StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if err := st.SetTag(ctx, r.ID, model.KV{Key: "k", Value: "v"}); err != nil {
			t.Fatalf("set tag: %v", err)
		}
		if _, err := st.DB().ExecContext(ctx, "DROP TABLE events"); err != nil {
			t.Fatalf("drop events: %v", err)
		}
		if err := st.DeleteTag(ctx, r.ID, "k"); err == nil {
			t.Fatal("expected DeleteTag to fail when the event write fails")
		}
		if v, ok := tagValue(t, st, r.ID, "k"); !ok || v != "v" {
			t.Errorf("DeleteTag not rolled back: got (%q,%v) want (\"v\",true)", v, ok)
		}
	})
}

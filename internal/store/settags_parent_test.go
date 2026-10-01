package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// SetTags (log-batch) must keep runs.parent_run_id in sync with the
// mlflow.parentRunId tag exactly like SetTag does.
func TestSetTagsSyncsParentRunID(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "settags-parent")

	parent := &model.Run{ExperimentID: expID, StartTime: 1}
	child := &model.Run{ExperimentID: expID, StartTime: 2}
	for _, r := range []*model.Run{parent, child} {
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.SetTags(ctx, child.ID, []model.KV{
		{Key: "other", Value: "x"},
		{Key: "mlflow.parentRunId", Value: parent.ID},
	}); err != nil {
		t.Fatalf("SetTags: %v", err)
	}
	got, err := st.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRunID != parent.ID {
		t.Fatalf("parent_run_id = %q, want %q", got.ParentRunID, parent.ID)
	}

	// A nonexistent parent is rejected atomically (same as SetTag): no tag
	// from the batch is persisted.
	err = st.SetTags(ctx, child.ID, []model.KV{
		{Key: "batch2", Value: "y"},
		{Key: "mlflow.parentRunId", Value: "does-not-exist"},
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing parent: want ErrNotFound, got %v", err)
	}
	tags, err := st.GetTags(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range tags {
		if tg.Key == "batch2" {
			t.Fatalf("failed batch leaked tag %q", tg.Key)
		}
		if tg.Key == "mlflow.parentRunId" && tg.Value != parent.ID {
			t.Fatalf("parent tag changed to %q", tg.Value)
		}
	}
}

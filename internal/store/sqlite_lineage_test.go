package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func TestLineageRoundtrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	// Create an experiment and a parent run.
	expID := mustCreateExpInStore(t, st, "lineage-test")

	parent := &model.Run{
		ExperimentID: expID,
		Name:         "parent-run",
		StartTime:    time.Now().UnixMilli() - 5000,
	}
	if err := st.CreateRun(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// Create a child run with parent_run_id set.
	child := &model.Run{
		ExperimentID: expID,
		Name:         "child-run",
		StartTime:    time.Now().UnixMilli() - 2000,
		ParentRunID:  parent.ID,
	}
	if err := st.CreateRun(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// Verify parent_run_id is persisted.
	got, err := st.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if got.ParentRunID != parent.ID {
		t.Errorf("parent_run_id: got %q want %q", got.ParentRunID, parent.ID)
	}

	// Verify the mlflow.parentRunId tag mirror.
	tags, err := st.GetTags(ctx, child.ID)
	if err != nil {
		t.Fatalf("get tags: %v", err)
	}
	var foundTag bool
	for _, tag := range tags {
		if tag.Key == "mlflow.parentRunId" && tag.Value == parent.ID {
			foundTag = true
			break
		}
	}
	if !foundTag {
		t.Errorf("mlflow.parentRunId tag not set; tags=%v", tags)
	}
}

func TestLineageGetLineage(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	expID := mustCreateExpInStore(t, st, "lineage-tree-test")

	// Build: grandparent → parent → child
	gp := &model.Run{ExperimentID: expID, Name: "grandparent", StartTime: time.Now().UnixMilli() - 10000}
	if err := st.CreateRun(ctx, gp); err != nil {
		t.Fatal(err)
	}
	p := &model.Run{ExperimentID: expID, Name: "parent", StartTime: time.Now().UnixMilli() - 5000, ParentRunID: gp.ID}
	if err := st.CreateRun(ctx, p); err != nil {
		t.Fatal(err)
	}
	c := &model.Run{ExperimentID: expID, Name: "child", StartTime: time.Now().UnixMilli() - 1000, ParentRunID: p.ID}
	if err := st.CreateRun(ctx, c); err != nil {
		t.Fatal(err)
	}

	// GetLineage on the parent: should have one ancestor (grandparent) and one descendant (child).
	lineage, err := st.GetRunLineage(ctx, p.ID)
	if err != nil {
		t.Fatalf("get lineage: %v", err)
	}
	if lineage.Run.ID != p.ID {
		t.Errorf("lineage run id: got %q want %q", lineage.Run.ID, p.ID)
	}
	if len(lineage.Ancestors) != 1 || lineage.Ancestors[0].ID != gp.ID {
		t.Errorf("ancestors: got %v, want [%s]", lineage.Ancestors, gp.ID)
	}
	if len(lineage.Descendants) != 1 || lineage.Descendants[0].ID != c.ID {
		t.Errorf("descendants: got %v, want [%s]", lineage.Descendants, c.ID)
	}
}

func TestLineageSyncFromTag(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	expID := mustCreateExpInStore(t, st, "lineage-tag-sync")

	parent := &model.Run{ExperimentID: expID, StartTime: time.Now().UnixMilli()}
	if err := st.CreateRun(ctx, parent); err != nil {
		t.Fatal(err)
	}
	child := &model.Run{ExperimentID: expID, StartTime: time.Now().UnixMilli()}
	if err := st.CreateRun(ctx, child); err != nil {
		t.Fatal(err)
	}

	// Set mlflow.parentRunId tag AFTER run creation (MLflow client path).
	if err := st.SetTag(ctx, child.ID, model.KV{Key: "mlflow.parentRunId", Value: parent.ID}); err != nil {
		t.Fatalf("SetTag: %v", err)
	}

	// Column should be synced.
	got, err := st.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRunID != parent.ID {
		t.Errorf("parent_run_id after tag sync: got %q want %q", got.ParentRunID, parent.ID)
	}
}

// mustCreateExpInStore creates an experiment and returns its ID.
func mustCreateExpInStore(t *testing.T, st *store.SQLiteStore, name string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := st.CreateExperiment(ctx, &model.Experiment{Name: name})
	if err != nil {
		t.Fatalf("CreateExperiment(%q): %v", name, err)
	}
	return id
}

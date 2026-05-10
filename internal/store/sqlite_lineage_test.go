package store_test

import (
	"context"
	"strconv"
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

// TestLineageDirectionUpstream verifies that direction=upstream returns
// only ancestors with no descendants populated.
func TestLineageDirectionUpstream(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-upstream")

	gp := &model.Run{ExperimentID: expID, Name: "gp", StartTime: time.Now().UnixMilli() - 10000}
	if err := st.CreateRun(ctx, gp); err != nil {
		t.Fatal(err)
	}
	p := &model.Run{ExperimentID: expID, Name: "p", StartTime: time.Now().UnixMilli() - 5000, ParentRunID: gp.ID}
	if err := st.CreateRun(ctx, p); err != nil {
		t.Fatal(err)
	}
	c := &model.Run{ExperimentID: expID, Name: "c", StartTime: time.Now().UnixMilli() - 1000, ParentRunID: p.ID}
	if err := st.CreateRun(ctx, c); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetRunLineageWithOptions(ctx, p.ID, store.LineageOptions{Direction: store.LineageUpstream})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ancestors) != 1 || got.Ancestors[0].ID != gp.ID {
		t.Errorf("ancestors: got %d, want 1=[gp]", len(got.Ancestors))
	}
	if len(got.Descendants) != 0 {
		t.Errorf("descendants should be empty for direction=upstream, got %d", len(got.Descendants))
	}
}

// TestLineageDescendantBFS verifies depth-N walk and truncation.
func TestLineageDescendantBFS(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-bfs")

	// Build a 4-level chain plus a wide level: root → l1 → l2 (×3) → l3.
	root := &model.Run{ExperimentID: expID, Name: "root", StartTime: 1000}
	if err := st.CreateRun(ctx, root); err != nil {
		t.Fatal(err)
	}
	l1 := &model.Run{ExperimentID: expID, Name: "l1", StartTime: 2000, ParentRunID: root.ID}
	if err := st.CreateRun(ctx, l1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		l2 := &model.Run{ExperimentID: expID, Name: "l2-" + strconv.Itoa(i), StartTime: int64(3000 + i), ParentRunID: l1.ID}
		if err := st.CreateRun(ctx, l2); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			l3 := &model.Run{ExperimentID: expID, Name: "l3", StartTime: 4000, ParentRunID: l2.ID}
			if err := st.CreateRun(ctx, l3); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Depth=1 → only l1.
	got, err := st.GetRunLineageWithOptions(ctx, root.ID, store.LineageOptions{Direction: store.LineageDownstream, DescendantDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Descendants) != 1 || got.Descendants[0].ID != l1.ID {
		t.Errorf("depth=1: want [l1], got %v", runIDs(got.Descendants))
	}
	if !got.Truncated {
		t.Errorf("depth=1 should set truncated=true (more levels exist)")
	}

	// Depth=4 → root's whole subtree (l1, 3×l2, 1×l3) = 5 nodes.
	got, err = st.GetRunLineageWithOptions(ctx, root.ID, store.LineageOptions{Direction: store.LineageDownstream, DescendantDepth: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Descendants) != 5 {
		t.Errorf("depth=4: want 5 descendants, got %d (%v)", len(got.Descendants), runIDs(got.Descendants))
	}
	if got.Truncated {
		t.Errorf("depth=4 should not truncate (whole tree fits)")
	}
}

// TestLineageDescendantFanOutCap verifies that wide trees truncate at fanout.
func TestLineageDescendantFanOutCap(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-fanout")

	root := &model.Run{ExperimentID: expID, Name: "root", StartTime: 1000}
	if err := st.CreateRun(ctx, root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		c := &model.Run{ExperimentID: expID, Name: "c" + strconv.Itoa(i), StartTime: int64(2000 + i), ParentRunID: root.ID}
		if err := st.CreateRun(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.GetRunLineageWithOptions(ctx, root.ID, store.LineageOptions{
		Direction:        store.LineageDownstream,
		DescendantDepth:  2,
		MaxNodesPerLevel: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Fan-out cap = 4 — we should see at most 4 nodes returned (10 children
	// truncated to 4) and the truncated flag should be set.
	if len(got.Descendants) > 4 {
		t.Errorf("expected fan-out cap to limit to 4, got %d", len(got.Descendants))
	}
	if !got.Truncated {
		t.Errorf("expected truncated=true with 10 children at fanout=4")
	}
}

// mustCreateWorkspace inserts a workspace row so foreign-key-constrained
// experiments can reference it. Tests that exercise multi-workspace
// isolation need this.
func mustCreateWorkspace(t *testing.T, st *store.SQLiteStore, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := st.CreateWorkspace(context.Background(), &model.Workspace{
		ID: id, Name: id, CreationTime: now, LastUpdateTime: now,
	}); err != nil {
		t.Fatalf("CreateWorkspace(%q): %v", id, err)
	}
}

// TestLineageWorkspaceIsolationAncestors guards independent-review #2:
// a tag-injected parent_run_id pointing into another workspace must NOT
// be walked. The ancestor chain stops at the workspace boundary.
func TestLineageWorkspaceIsolationAncestors(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	mustCreateWorkspace(t, st, "ws-a")
	mustCreateWorkspace(t, st, "ws-b")

	// Two workspaces, each with one experiment.
	expA, err := st.CreateExperiment(ctx, &model.Experiment{Name: "ws-a-exp", WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	expB, err := st.CreateExperiment(ctx, &model.Experiment{Name: "ws-b-exp", WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatal(err)
	}

	// Run in ws-a (the parent we don't want leaked).
	parent := &model.Run{ExperimentID: expA, Name: "ws-a-secret-parent", StartTime: 1000}
	if err := st.CreateRun(ctx, parent); err != nil {
		t.Fatal(err)
	}
	// Run in ws-b whose tag-injected parent_run_id points cross-workspace.
	child := &model.Run{ExperimentID: expB, Name: "ws-b-child", StartTime: 2000, ParentRunID: parent.ID}
	if err := st.CreateRun(ctx, child); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetRunLineageWithOptions(ctx, child.ID, store.LineageOptions{Direction: store.LineageUpstream})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ancestors) != 0 {
		t.Errorf("cross-workspace ancestor leaked: got %d, want 0 (parent is in ws-a, child in ws-b)", len(got.Ancestors))
	}
}

// TestLineageWorkspaceIsolationDescendants guards independent-review #2
// for the downstream direction.
func TestLineageWorkspaceIsolationDescendants(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	mustCreateWorkspace(t, st, "ws-a")
	mustCreateWorkspace(t, st, "ws-b")
	expA, err := st.CreateExperiment(ctx, &model.Experiment{Name: "ws-a-exp", WorkspaceID: "ws-a"})
	if err != nil {
		t.Fatal(err)
	}
	expB, err := st.CreateExperiment(ctx, &model.Experiment{Name: "ws-b-exp", WorkspaceID: "ws-b"})
	if err != nil {
		t.Fatal(err)
	}

	parent := &model.Run{ExperimentID: expA, Name: "parent-in-A", StartTime: 1000}
	if err := st.CreateRun(ctx, parent); err != nil {
		t.Fatal(err)
	}
	// Hostile child in ws-b claiming parent in ws-a.
	hostileChild := &model.Run{ExperimentID: expB, Name: "hostile", StartTime: 2000, ParentRunID: parent.ID}
	if err := st.CreateRun(ctx, hostileChild); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetRunLineageWithOptions(ctx, parent.ID, store.LineageOptions{Direction: store.LineageDownstream, DescendantDepth: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Descendants) != 0 {
		t.Errorf("cross-workspace descendant leaked: got %v", runIDs(got.Descendants))
	}
}

// TestLineageDescendantCycleDefense guards walkDescendants against a
// tag-injected self-loop (parent_run_id = self) and 2-cycle (A↔B).
// The function uses a visited map; this test would catch a regression
// that drops it.
func TestLineageDescendantCycleDefense(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-cycle")

	a := &model.Run{ExperimentID: expID, Name: "a", StartTime: 1000}
	if err := st.CreateRun(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := &model.Run{ExperimentID: expID, Name: "b", StartTime: 2000, ParentRunID: a.ID}
	if err := st.CreateRun(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Force a → b → a cycle via tag mirror.
	if err := st.SetTag(ctx, a.ID, model.KV{Key: "mlflow.parentRunId", Value: b.ID}); err != nil {
		t.Fatalf("SetTag: %v", err)
	}

	done := make(chan struct{})
	go func() {
		// 5s budget — if BFS doesn't terminate, the test deadlocks here.
		_, _ = st.GetRunLineageWithOptions(ctx, a.ID, store.LineageOptions{Direction: store.LineageDownstream, DescendantDepth: 8})
		close(done)
	}()
	select {
	case <-done:
		// pass
	case <-time.After(5 * time.Second):
		t.Fatal("walkDescendants did not terminate on cycle within 5s")
	}
}

// TestLineageFanOutAndDeeperSubtree guards independent-review #5:
// when fanout cap is hit on level 1 AND the subtree extends deeper, the
// flag must report truncated=true (and the deeper levels are reachable
// from the surviving frontier).
func TestLineageFanOutAndDeeperSubtree(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-fanout-deep")

	root := &model.Run{ExperimentID: expID, Name: "root", StartTime: 1000}
	if err := st.CreateRun(ctx, root); err != nil {
		t.Fatal(err)
	}
	// 6 children of root, with the FIRST one (oldest start_time) having a grandchild.
	var firstChild *model.Run
	for i := 0; i < 6; i++ {
		c := &model.Run{ExperimentID: expID, Name: "c" + strconv.Itoa(i), StartTime: int64(2000 + i), ParentRunID: root.ID}
		if err := st.CreateRun(ctx, c); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstChild = c
		}
	}
	gc := &model.Run{ExperimentID: expID, Name: "gc", StartTime: 5000, ParentRunID: firstChild.ID}
	if err := st.CreateRun(ctx, gc); err != nil {
		t.Fatal(err)
	}

	// fanout=4, depth=3 — level 0 returns 6 children but BFS appends only 4
	// (truncated=true). nextFrontier is the kept 4 (oldest first by
	// start_time), so c0 survives → gc is reachable on level 1.
	got, err := st.GetRunLineageWithOptions(ctx, root.ID, store.LineageOptions{
		Direction:        store.LineageDownstream,
		DescendantDepth:  3,
		MaxNodesPerLevel: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Errorf("expected truncated=true (6 children > fanout=4)")
	}
	// c0..c3 + gc = 5 nodes
	if len(got.Descendants) != 5 {
		t.Errorf("want 5 nodes (4 kids + 1 grandchild), got %d (%v)", len(got.Descendants), runIDs(got.Descendants))
	}
	foundGC := false
	for _, d := range got.Descendants {
		if d.ID == gc.ID {
			foundGC = true
			break
		}
	}
	if !foundGC {
		t.Errorf("grandchild not reachable through surviving frontier")
	}
}

// TestLineageRunDatasetEdgesLegacyV03 guards independent-review #4 (deferred
// from v1.4-rc1 → closed at stable). Insert a v0.3-style dataset_inputs row
// directly (bypassing LogInputs which mirrors into datasets_v2) and verify
// the lineage response surfaces the edge with Version=0 / DatasetID=0.
func TestLineageRunDatasetEdgesLegacyV03(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-legacy-ds")

	r := &model.Run{ExperimentID: expID, Name: "r-legacy", StartTime: time.Now().UnixMilli()}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Insert into the v0.3 datasets+dataset_inputs tables directly. The
	// FOREIGN KEY (name, digest) → datasets(name, digest) means we have to
	// seed a datasets row first. v0.3 datasets is workspace-agnostic.
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO datasets(name, digest, source_type, source, schema, profile)
		VALUES ('legacy-corpus', 'cafef00d', 'legacy', '', '', '')
	`); err != nil {
		t.Fatalf("seed datasets row: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO dataset_inputs(run_id, name, digest)
		VALUES (?, 'legacy-corpus', 'cafef00d')
	`, r.ID); err != nil {
		t.Fatalf("seed dataset_inputs row: %v", err)
	}

	got, err := st.GetRunLineageWithOptions(ctx, r.ID, store.LineageOptions{Direction: store.LineageBoth})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Datasets) != 1 {
		t.Fatalf("expected 1 dataset edge, got %d", len(got.Datasets))
	}
	d := got.Datasets[0]
	if d.Name != "legacy-corpus" || d.Digest != "cafef00d" {
		t.Errorf("edge identity: got %+v", d)
	}
	if d.Version != 0 || d.DatasetID != 0 {
		t.Errorf("legacy v0.3 row should leave Version/DatasetID at 0, got version=%d id=%d", d.Version, d.DatasetID)
	}
}

// TestLineageRunDatasetEdges verifies dataset_inputs are surfaced in lineage.
func TestLineageRunDatasetEdges(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	expID := mustCreateExpInStore(t, st, "lineage-datasets")

	r := &model.Run{ExperimentID: expID, Name: "r", StartTime: time.Now().UnixMilli()}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := st.LogInputs(ctx, r.ID, []model.DatasetInput{
		{Dataset: model.Dataset{Name: "training-set", Digest: "deadbeef"}},
	}); err != nil {
		t.Fatalf("LogInputs: %v", err)
	}

	got, err := st.GetRunLineageWithOptions(ctx, r.ID, store.LineageOptions{Direction: store.LineageBoth})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Datasets) != 1 {
		t.Fatalf("expected 1 dataset edge, got %d", len(got.Datasets))
	}
	d := got.Datasets[0]
	if d.Name != "training-set" || d.Digest != "deadbeef" || d.RunID != r.ID {
		t.Errorf("edge: got %+v", d)
	}
	// LogInputs mirrors into datasets_v2 → version 1, dataset_id > 0.
	if d.Version != 1 || d.DatasetID == 0 {
		t.Errorf("expected v1.2 mirror to populate version+dataset_id, got %+v", d)
	}
}

// runIDs is a test helper to print descendant IDs in failure messages.
func runIDs(rs []*model.Run) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
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

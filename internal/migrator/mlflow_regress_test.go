package migrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// inMemStoreWithGet adds GetExperiment so the resume path can verify a
// checkpointed experiment still exists.
type inMemStoreWithGet struct{ *inMemStore }

func (s inMemStoreWithGet) GetExperiment(_ context.Context, id int64) (*model.Experiment, error) {
	for _, e := range s.experiments {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, store.ErrNotFound
}

// Regression: resuming an interrupted import re-created every experiment,
// hit the name collision with the experiment created by the first attempt and
// imported the remaining runs into a fresh "-imported-<ts>" duplicate.
func TestMLflowImporter_ResumeReusesExperiment(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()
	fake.experiments = []mlflowExperiment{{ExperimentID: "7", Name: "resume-exp", LifecycleStage: "active"}}
	fake.runs["7"] = []mlflowRun{{Info: mlflowRunInfo{RunID: "r1", ExperimentID: "7", Status: "FINISHED", StartTime: 1}}}

	st := inMemStoreWithGet{newInMemStore()}
	dir := t.TempDir()
	newImporter := func() *MLflowImporter {
		imp := &MLflowImporter{SourceURL: srv.URL, HTTP: srv.Client(), Store: st, ArtifactStore: newInMemArtifactStore()}
		imp.SetCheckpointDir(dir)
		return imp
	}
	if _, err := newImporter().Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// A second source run appears before the operator resumes.
	fake.runs["7"] = append(fake.runs["7"], mlflowRun{Info: mlflowRunInfo{RunID: "r2", ExperimentID: "7", Status: "FINISHED", StartTime: 2}})
	if _, err := newImporter().Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if len(st.experiments) != 1 {
		names := []string{}
		for _, e := range st.experiments {
			names = append(names, e.Name)
		}
		t.Fatalf("want 1 experiment after resume, got %d: %v", len(st.experiments), names)
	}
	if st.runs["r2"] == nil || st.runs["r2"].ExperimentID != st.experiments[0].ID {
		t.Fatalf("resumed run not imported into the original experiment: %+v", st.runs["r2"])
	}
}

// Regression: artifact paths were interpolated into the download URL
// unescaped, so a name containing '?' or '#' or a space was truncated.
func TestMLflowImporter_ArtifactPathEscaping(t *testing.T) {
	srv, fake := newFakeMLflow(t)
	defer srv.Close()
	fake.experiments = []mlflowExperiment{{ExperimentID: "1", Name: "esc", LifecycleStage: "active"}}
	fake.runs["1"] = []mlflowRun{{Info: mlflowRunInfo{RunID: "r1", ExperimentID: "1", Status: "FINISHED", StartTime: 1}}}
	fake.artifacts["r1/eval?v2 #1.txt"] = "payload"

	art := newInMemArtifactStore()
	imp := &MLflowImporter{SourceURL: srv.URL, HTTP: srv.Client(), Store: newInMemStore(), ArtifactStore: art}
	stats, err := imp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(art.files["r1/eval?v2 #1.txt"]); got != "payload" || stats.Artifacts != 1 {
		t.Fatalf("artifact not imported: content=%q stats.Artifacts=%d", got, stats.Artifacts)
	}
}

// Regression: MLflow lists runs newest-first, so a nested child came before its
// parent; its mlflow.parentRunId tag is mirrored into the parent_run_id FK
// column, which failed against the not-yet-imported parent and dropped the tag.
func TestMLflowImporter_ImportsParentsBeforeChildren(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sq, err := store.OpenSQLite(ctx, filepath.Join(dir, "t.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer sq.Close()
	if err := sq.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	srv, fake := newFakeMLflow(t)
	defer srv.Close()
	fake.experiments = []mlflowExperiment{{ExperimentID: "1", Name: "nested", LifecycleStage: "active"}}
	fake.runs["1"] = []mlflowRun{
		{
			Info: mlflowRunInfo{RunID: "child", ExperimentID: "1", Status: "FINISHED", StartTime: 2000},
			Data: mlflowRunData{Tags: []mlflowKV{{Key: "mlflow.parentRunId", Value: "parent"}}},
		},
		{Info: mlflowRunInfo{RunID: "parent", ExperimentID: "1", Status: "FINISHED", StartTime: 1000}},
	}
	imp := &MLflowImporter{SourceURL: srv.URL, HTTP: srv.Client(), Store: sq, ArtifactStore: newInMemArtifactStore()}
	if _, err := imp.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	child, err := sq.GetRun(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentRunID != "parent" {
		t.Fatalf("child.ParentRunID = %q, want %q", child.ParentRunID, "parent")
	}
}

package store_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func mustRun(t *testing.T, s *store.SQLiteStore, expID int64, id string) {
	t.Helper()
	if err := s.CreateRun(context.Background(), &model.Run{ID: id, ExperimentID: expID, StartTime: 1}); err != nil {
		t.Fatalf("CreateRun(%s): %v", id, err)
	}
}

// Regression: client-input validation failures in the store were plain
// errors, which every HTTP handler's writeStoreErr maps to 500. They must
// carry ErrInvalidFilter / ErrInvalidValue so they surface as 400.
func TestValidationErrorsCarrySentinels(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "v"})
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, s, expID, "run-v")
	newRegisteredModel(t, s, "m")

	for _, f := range []string{
		"metrics.acc > abc",
		"attributes.bogus = 'x'",
		"attributes.bogus IN ('a')",
		"metrics.acc BETWEEN a AND 1",
		"params.p IN ('unterminated)",
	} {
		_, err := s.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: []int64{expID}, Filter: f})
		if !errors.Is(err, store.ErrInvalidFilter) {
			t.Errorf("SearchRuns(%q): want ErrInvalidFilter, got %v", f, err)
		}
	}
	if _, err := s.SearchExperiments(ctx, store.SearchOptions{Filter: "tags.x = 'y'"}); !errors.Is(err, store.ErrInvalidFilter) {
		t.Errorf("SearchExperiments: want ErrInvalidFilter, got %v", err)
	}
	if _, err := s.SearchRegisteredModels(ctx, "default", "name ~ 'x'", 10, ""); !errors.Is(err, store.ErrInvalidFilter) {
		t.Errorf("SearchRegisteredModels: want ErrInvalidFilter, got %v", err)
	}
	if _, err := s.SearchModelVersions(ctx, "default", "source = 'x'", 10, ""); !errors.Is(err, store.ErrInvalidFilter) {
		t.Errorf("SearchModelVersions: want ErrInvalidFilter, got %v", err)
	}

	bad := "BOGUS"
	checks := map[string]error{
		"UpdateRun status":       s.UpdateRun(ctx, "run-v", &bad, nil, nil),
		"SetRunLifecycle":        s.SetRunLifecycle(ctx, "run-v", "gone"),
		"SetExperimentLifecycle": s.SetExperimentLifecycle(ctx, expID, "gone"),
		"LogMetric empty key":    s.LogMetric(ctx, "run-v", model.Metric{Key: "", Value: 1}),
		"SetTag padded key":      s.SetTag(ctx, "run-v", model.KV{Key: " k", Value: "v"}),
		"CreateWorkspace bad id": s.CreateWorkspace(ctx, &model.Workspace{ID: "Bad ID!", Name: "x"}),
		"AddMember bad role":     s.AddMember(ctx, "default", "u", "owner"),
		"CreateRegisteredModel":  s.CreateRegisteredModel(ctx, "default", &model.RegisteredModel{Name: ""}),
	}
	for label, err := range checks {
		if !errors.Is(err, store.ErrInvalidValue) {
			t.Errorf("%s: want ErrInvalidValue, got %v", label, err)
		}
	}
}

// Regression: a malformed metric-history page_token was silently ignored and
// page one returned again, so a paging client never terminated.
func TestGetMetricHistoryMalformedPageToken(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "mh"})
	mustRun(t, s, expID, "run-mh")
	if err := s.LogMetric(ctx, "run-mh", model.Metric{Key: "loss", Value: 1, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.GetMetricHistory(ctx, "run-mh", "loss", store.MetricHistoryOptions{MaxResults: 1, PageToken: "garbage"})
	if !errors.Is(err, store.ErrInvalidFilter) {
		t.Fatalf("want ErrInvalidFilter, got %v", err)
	}
}

// Regression: the model-version page token "name:version" was split on the
// FIRST colon, so a model name containing ':' produced a wrong cursor and
// paging skipped or repeated rows.
func TestSearchModelVersionsPagingWithColonInName(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	names := []string{"org:clf", "org:clf:v2"}
	want := 0
	for _, n := range names {
		newRegisteredModel(t, s, n)
		for i := 0; i < 3; i++ {
			newModelVersion(t, s, n, "s3://x")
			want++
		}
	}
	seen := map[string]bool{}
	token := ""
	for pages := 0; pages < 20; pages++ {
		res, err := s.SearchModelVersions(ctx, "default", "", 2, token)
		if err != nil {
			t.Fatal(err)
		}
		for _, mv := range res.Items {
			k := fmt.Sprintf("%s#%d", mv.Name, mv.Version)
			if seen[k] {
				t.Fatalf("duplicate %s across pages", k)
			}
			seen[k] = true
		}
		if res.NextPageToken == "" {
			break
		}
		token = res.NextPageToken
	}
	if len(seen) != want {
		t.Fatalf("paged %d versions, want %d: %v", len(seen), want, seen)
	}
	if _, err := s.SearchModelVersions(ctx, "default", "", 2, "no-colon"); !errors.Is(err, store.ErrInvalidFilter) {
		t.Fatalf("malformed token: want ErrInvalidFilter, got %v", err)
	}
}

// Regression: SearchRuns ignored SearchOptions.WorkspaceID, so a search with
// no (or foreign) experiment IDs returned runs from every workspace.
func TestSearchRunsWorkspaceScope(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	defExp, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "e"})
	othExp, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "e", WorkspaceID: "other"})
	mustRun(t, s, defExp, "run-default")
	mustRun(t, s, othExp, "run-other")

	for _, ids := range [][]int64{nil, {othExp}, {defExp, othExp}} {
		res, err := s.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: ids, WorkspaceID: "default"})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res.Items {
			if r.ID == "run-other" {
				t.Fatalf("exp ids %v: run from workspace 'other' leaked into 'default' search", ids)
			}
		}
	}
}

// Regression: RenameRegisteredModel cascaded the new name to versions, model
// tags and aliases but not model_version_tags, orphaning every version tag.
func TestRenameRegisteredModelKeepsVersionTags(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	newRegisteredModel(t, s, "old")
	mv := newModelVersion(t, s, "old", "s3://m")
	if err := s.SetModelVersionTag(ctx, "default", "old", mv.Version, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameRegisteredModel(ctx, "default", "old", "new"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelVersion(ctx, "default", "new", mv.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != 1 || got.Tags[0].Key != "k" {
		t.Fatalf("version tags after rename = %+v, want [k=v]", got.Tags)
	}
	var orphans int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM model_version_tags WHERE name = 'old'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d model_version_tags rows still reference the old name", orphans)
	}
}

// Regression: DeleteWorkspace only refused when experiments existed, but
// registered_models / prompts cascade on workspace delete, so deleting a
// workspace silently destroyed its model registry.
func TestDeleteWorkspaceRefusesWhenRegistryNonEmpty(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-reg", Name: "Reg"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRegisteredModel(ctx, "ws-reg", &model.RegisteredModel{Name: "keep-me"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorkspace(ctx, "ws-reg"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if _, err := s.GetRegisteredModel(ctx, "ws-reg", "keep-me"); err != nil {
		t.Fatalf("registered model lost: %v", err)
	}

	// An empty workspace is still deletable, taking its dashboards with it.
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-empty", Name: "Empty"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDashboard(ctx, "ws-empty", "p", "[]"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWorkspace(ctx, "ws-empty"); err != nil {
		t.Fatalf("delete empty workspace: %v", err)
	}
	if _, err := s.GetDashboard(ctx, "ws-empty", "p"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dashboard survived workspace delete: %v", err)
	}
	if err := s.DeleteWorkspace(ctx, "ws-missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing workspace: want ErrNotFound, got %v", err)
	}
}

// Regression: concurrent CreateModelVersion / CreateDatasetVersion calls for
// the same name raced on MAX(version)+1 and the loser failed with a UNIQUE /
// SQLITE_BUSY error instead of getting the next version.
func TestConcurrentVersionedCreates(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	newRegisteredModel(t, s, "race")
	const n = 8
	var wg sync.WaitGroup
	mvErrs := make(chan error, n)
	dsErrs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.CreateModelVersion(ctx, "default", &model.ModelVersion{Name: "race", Source: "s3://x"})
			mvErrs <- err
		}()
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateDatasetVersion(ctx, &model.DatasetVersion{Name: "ds", ContentHash: fmt.Sprintf("h%d", i)}, nil)
			dsErrs <- err
		}(i)
	}
	wg.Wait()
	close(mvErrs)
	close(dsErrs)
	for err := range mvErrs {
		if err != nil {
			t.Errorf("CreateModelVersion: %v", err)
		}
	}
	for err := range dsErrs {
		if err != nil {
			t.Errorf("CreateDatasetVersion: %v", err)
		}
	}
	res, err := s.SearchModelVersions(ctx, "default", "name = 'race'", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != n {
		t.Fatalf("model versions = %d, want %d", len(res.Items), n)
	}
	vs, err := s.ListDatasetVersions(ctx, "default", "ds")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != n {
		t.Fatalf("dataset versions = %d, want %d", len(vs), n)
	}
}

// Regression: a parent_run_id pointing at a missing run failed the FK on the
// runs.parent_run_id column and surfaced as a raw (500) error.
func TestMissingParentRunIsNotFound(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "p"})
	err := s.CreateRun(ctx, &model.Run{ID: "child", ExperimentID: expID, StartTime: 1, ParentRunID: "nope"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("CreateRun with missing parent: want ErrNotFound, got %v", err)
	}
	mustRun(t, s, expID, "child2")
	if err := s.SetTag(ctx, "child2", model.KV{Key: "mlflow.parentRunId", Value: "nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetTag parentRunId missing parent: want ErrNotFound, got %v", err)
	}
}

// Regression: GetRunAsOf built its tag list from a map, so tag order was
// random on every call instead of the GetTags ORDER BY key contract.
func TestGetRunAsOfTagsSorted(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "asof"})
	mustRun(t, s, expID, "run-asof")
	var tags []model.KV
	for i := 0; i < 16; i++ {
		tags = append(tags, model.KV{Key: fmt.Sprintf("k%02d", i), Value: "v"})
	}
	if err := s.SetTags(ctx, "run-asof", tags); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, got, err := s.GetRunAsOf(ctx, "run-asof", 1<<62)
		if err != nil {
			t.Fatal(err)
		}
		if !sort.SliceIsSorted(got, func(a, b int) bool { return got[a].Key < got[b].Key }) {
			t.Fatalf("as-of tags not sorted by key: %v", got)
		}
	}
}

// SearchExperiments / SearchRegisteredModels now batch-load tags; make sure
// each item still gets exactly its own tags.
func TestSearchBatchedTagsAttachToRightItem(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		id, err := s.CreateExperiment(ctx, &model.Experiment{Name: fmt.Sprintf("e%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetExperimentTag(ctx, id, "idx", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("m%d", i)
		newRegisteredModel(t, s, name)
		if err := s.SetRegisteredModelTag(ctx, "default", name, "idx", fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	exps, err := s.SearchExperiments(ctx, store.SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exps.Items {
		if e.Name == "Default" {
			continue
		}
		if len(e.Tags) != 1 || "e"+e.Tags[0].Value != e.Name {
			t.Errorf("experiment %s tags = %+v", e.Name, e.Tags)
		}
	}
	models, err := s.SearchRegisteredModels(ctx, "default", "", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models.Items {
		if len(m.Tags) != 1 || "m"+m.Tags[0].Value != m.Name {
			t.Errorf("model %s tags = %+v", m.Name, m.Tags)
		}
	}
}

// Regression: InsertSpans upserted on the span id alone, so a client could
// overwrite another run's (or workspace's) span by reusing its id.
func TestInsertSpansRejectsForeignSpanOverwrite(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "spans"})
	mustRun(t, s, expID, "run-a")
	mustRun(t, s, expID, "run-b")
	end := int64(5)
	orig := model.Span{ID: "span1", TraceID: "t1", RunID: "run-a", Name: "op", StartTimeNS: 1, EndTimeNS: &end, StatusCode: "OK"}
	if err := s.InsertSpans(ctx, []model.Span{orig}); err != nil {
		t.Fatal(err)
	}
	// Same trace + run: legitimate update (e.g. span end arrives later).
	upd := orig
	upd.StatusCode = "ERROR"
	if err := s.InsertSpans(ctx, []model.Span{upd}); err != nil {
		t.Fatalf("same-owner update: %v", err)
	}
	// Different run reusing the id must be refused and leave the span intact.
	evil := orig
	evil.RunID = "run-b"
	evil.StatusCode = "HIJACKED"
	if err := s.InsertSpans(ctx, []model.Span{evil}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign overwrite: want ErrConflict, got %v", err)
	}
	got, err := s.GetSpansByRun(ctx, "run-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StatusCode != "ERROR" {
		t.Fatalf("span after rejected overwrite = %+v", got)
	}
}

// Regression: CloneExperiment did not check the source experiment's
// workspace, so a caller could clone (and read the tags of) an experiment
// from another workspace into its own.
func TestCloneExperimentCrossWorkspace(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	src, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "secret"})
	if _, err := s.CloneExperiment(ctx, src, "stolen", "ws-b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-workspace clone: want ErrNotFound, got %v", err)
	}
	if _, err := s.CloneExperiment(ctx, src, "copy", "default"); err != nil {
		t.Fatalf("same-workspace clone: %v", err)
	}
}

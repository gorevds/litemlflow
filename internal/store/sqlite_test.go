package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// newStore creates a fresh, migrated SQLite-backed store in t.TempDir.
func newStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
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

func TestExperimentCRUD(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	id, err := s.CreateExperiment(ctx, &model.Experiment{Name: "alpha"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := s.GetExperiment(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "alpha" {
		t.Fatalf("want name alpha, got %s", got.Name)
	}
	if got.ArtifactLocation == "" {
		t.Fatal("artifact location should default to a non-empty path")
	}

	got2, err := s.GetExperimentByName(ctx, "alpha")
	if err != nil {
		t.Fatalf("get-by-name: %v", err)
	}
	if got2.ID != id {
		t.Fatalf("id mismatch: %d vs %d", got2.ID, id)
	}

	// Duplicate name should fail.
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "alpha"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}

	// Rename.
	newName := "alpha-renamed"
	if err := s.UpdateExperiment(ctx, id, &newName); err != nil {
		t.Fatalf("update: %v", err)
	}
	got3, _ := s.GetExperiment(ctx, id)
	if got3.Name != newName {
		t.Fatalf("rename failed, got %s", got3.Name)
	}

	// Delete (lifecycle).
	if err := s.SetExperimentLifecycle(ctx, id, model.LifecycleDeleted); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got4, _ := s.GetExperiment(ctx, id)
	if got4.LifecycleStage != model.LifecycleDeleted {
		t.Fatalf("want deleted, got %s", got4.LifecycleStage)
	}

	// Tag.
	if err := s.SetExperimentTag(ctx, id, "team", "vision"); err != nil {
		t.Fatalf("tag: %v", err)
	}
	got5, _ := s.GetExperiment(ctx, id)
	if len(got5.Tags) != 1 || got5.Tags[0].Key != "team" || got5.Tags[0].Value != "vision" {
		t.Fatalf("tag missing, got %+v", got5.Tags)
	}

	// Tag upsert.
	if err := s.SetExperimentTag(ctx, id, "team", "platform"); err != nil {
		t.Fatalf("tag upsert: %v", err)
	}
	got6, _ := s.GetExperiment(ctx, id)
	if got6.Tags[0].Value != "platform" {
		t.Fatal("tag should have been overwritten")
	}
}

func TestRunCRUD(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "exp"})
	run := &model.Run{ExperimentID: expID, Name: "first"}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == "" {
		t.Fatal("run ID should be auto-generated")
	}

	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Name != "first" || got.Status != model.StatusRunning || got.Kind != model.KindClassic {
		t.Fatalf("unexpected fields %+v", got)
	}

	// Update status.
	finished := model.StatusFinished
	endTime := int64(99999)
	if err := s.UpdateRun(ctx, run.ID, &finished, &endTime, nil); err != nil {
		t.Fatalf("update run: %v", err)
	}
	got2, _ := s.GetRun(ctx, run.ID)
	if got2.Status != model.StatusFinished {
		t.Fatalf("status not updated, got %s", got2.Status)
	}
	if got2.EndTime == nil || *got2.EndTime != 99999 {
		t.Fatalf("end_time not updated, got %v", got2.EndTime)
	}

	// Run on missing experiment fails.
	bad := &model.Run{ExperimentID: 99999, ArtifactURI: "x"}
	bad.ID = "deadbeef"
	if err := s.CreateRun(ctx, bad); err == nil {
		t.Fatal("expected FK error for missing experiment")
	}
}

func TestMetricsParamsTags(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "exp"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Metrics
	for i := 0; i < 5; i++ {
		if err := s.LogMetric(ctx, r.ID, model.Metric{Key: "loss", Value: 1.0 - float64(i)*0.1, Timestamp: int64(1000 + i), Step: int64(i)}); err != nil {
			t.Fatalf("log metric: %v", err)
		}
	}
	hist, _, err := s.GetMetricHistory(ctx, r.ID, "loss", store.MetricHistoryOptions{})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 5 {
		t.Fatalf("want 5 metrics, got %d", len(hist))
	}
	if hist[0].Step != 0 || hist[4].Step != 4 {
		t.Fatal("metrics should be ordered by timestamp/step ASC")
	}

	// Re-logging same (key, ts, step) is idempotent (no error).
	if err := s.LogMetric(ctx, r.ID, model.Metric{Key: "loss", Value: 999.0, Timestamp: 1000, Step: 0}); err != nil {
		t.Fatalf("idempotent metric: %v", err)
	}

	// Latest metrics
	latest, err := s.GetLatestMetrics(ctx, r.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if len(latest) != 1 || latest[0].Step != 4 {
		t.Fatalf("want latest step=4, got %+v", latest)
	}

	// Params
	if err := s.LogParam(ctx, r.ID, model.Param{Key: "lr", Value: "0.01"}); err != nil {
		t.Fatalf("log param: %v", err)
	}
	// Same value: idempotent.
	if err := s.LogParam(ctx, r.ID, model.Param{Key: "lr", Value: "0.01"}); err != nil {
		t.Fatalf("idempotent param: %v", err)
	}
	// Different value: error.
	if err := s.LogParam(ctx, r.ID, model.Param{Key: "lr", Value: "0.5"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}

	// Tags
	if err := s.SetTag(ctx, r.ID, model.KV{Key: "stage", Value: "dev"}); err != nil {
		t.Fatalf("tag: %v", err)
	}
	if err := s.SetTag(ctx, r.ID, model.KV{Key: "stage", Value: "prod"}); err != nil {
		t.Fatalf("tag overwrite: %v", err)
	}
	tags, _ := s.GetTags(ctx, r.ID)
	if len(tags) != 1 || tags[0].Value != "prod" {
		t.Fatalf("want overwritten, got %+v", tags)
	}
	if err := s.DeleteTag(ctx, r.ID, "stage"); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	if err := s.DeleteTag(ctx, r.ID, "stage"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing tag: want ErrNotFound, got %v", err)
	}
}

func TestSearchRuns(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "exp"})

	runIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		r := &model.Run{ExperimentID: expID, Name: "run-" + string(rune('a'+i))}
		_ = s.CreateRun(ctx, r)
		runIDs[i] = r.ID
		_ = s.LogParam(ctx, r.ID, model.Param{Key: "lr", Value: []string{"0.01", "0.05", "0.1", "0.5", "1.0"}[i]})
		_ = s.LogMetric(ctx, r.ID, model.Metric{Key: "acc", Value: 0.5 + float64(i)*0.1, Timestamp: 1, Step: 0})
	}

	// All
	res, err := s.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: []int64{expID}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("want 5, got %d", len(res.Items))
	}

	// Filter by param.
	res, err = s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        "params.lr = '0.5'",
	})
	if err != nil {
		t.Fatalf("filter param: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1, got %d", len(res.Items))
	}

	// Filter by metric.
	res, err = s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        "metrics.acc > 0.75",
	})
	if err != nil {
		t.Fatalf("filter metric: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("want 2 with acc > 0.75, got %d", len(res.Items))
	}

	// Pagination.
	res, err = s.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: []int64{expID}, MaxResults: 2})
	if err != nil {
		t.Fatalf("paginated: %v", err)
	}
	if len(res.Items) != 2 || res.NextPageToken == "" {
		t.Fatalf("expected token + 2 items, got %+v", res)
	}
}

func TestPromptVersioning(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	v1, err := s.CreatePrompt(ctx, &model.Prompt{Name: "sys", Content: "hello"})
	if err != nil {
		t.Fatalf("create v1: %v", err)
	}
	if v1 != 1 {
		t.Fatalf("want version 1, got %d", v1)
	}
	v2, _ := s.CreatePrompt(ctx, &model.Prompt{Name: "sys", Content: "hello v2"})
	if v2 != 2 {
		t.Fatalf("want version 2, got %d", v2)
	}
	// Identical content → same version reused.
	v1again, _ := s.CreatePrompt(ctx, &model.Prompt{Name: "sys", Content: "hello"})
	if v1again != 1 {
		t.Fatalf("want reused v1, got %d", v1again)
	}

	if err := s.SetPromptAlias(ctx, "sys", "production", 2); err != nil {
		t.Fatalf("alias: %v", err)
	}
	got, err := s.GetPromptByAlias(ctx, "sys", "production")
	if err != nil {
		t.Fatalf("get alias: %v", err)
	}
	if got.Version != 2 {
		t.Fatalf("want v2 via alias, got %d", got.Version)
	}
}

func TestSpansInsertAndQuery(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "exp"})
	r := &model.Run{ExperimentID: expID, Kind: model.KindTrace}
	_ = s.CreateRun(ctx, r)

	rootID := model.NewSpanID()
	traceID := model.NewTraceID()
	end := int64(2000)
	spans := []model.Span{
		{ID: rootID, TraceID: traceID, RunID: r.ID, Name: "root", StartTimeNS: 1000, EndTimeNS: &end},
		{ID: model.NewSpanID(), TraceID: traceID, ParentID: rootID, RunID: r.ID, Name: "child", StartTimeNS: 1100, EndTimeNS: &end},
	}
	if err := s.InsertSpans(ctx, spans); err != nil {
		t.Fatalf("insert spans: %v", err)
	}
	got, err := s.GetSpansByRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 spans, got %d", len(got))
	}
	got2, err := s.GetSpansByTrace(ctx, traceID)
	if err != nil || len(got2) != 2 {
		t.Fatalf("by trace: %v / %d", err, len(got2))
	}
}

func TestConcurrentWrites(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "exp"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	const writers = 8
	const each = 50
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if err := s.LogMetric(ctx, r.ID, model.Metric{
					Key: "loss", Value: float64(i*1000 + j), Timestamp: int64(i*1000 + j), Step: int64(i*1000 + j),
				}); err != nil {
					t.Errorf("write w=%d j=%d: %v", i, j, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	hist, _, _ := s.GetMetricHistory(ctx, r.ID, "loss", store.MetricHistoryOptions{})
	if len(hist) != writers*each {
		t.Fatalf("want %d metrics, got %d", writers*each, len(hist))
	}
}

func TestGetMetricHistoryDownsampled(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "ds-test"})
	if err != nil {
		t.Fatalf("create experiment: %v", err)
	}
	r := &model.Run{ExperimentID: expID}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatalf("create run: %v", err)
	}

	const total = 5000
	ms := make([]model.Metric, total)
	for i := 0; i < total; i++ {
		ms[i] = model.Metric{Key: "loss", Value: float64(i) * 0.001, Timestamp: int64(i + 1), Step: int64(i)}
	}
	if err := s.LogMetrics(ctx, r.ID, ms); err != nil {
		t.Fatalf("log metrics: %v", err)
	}

	const target = 200
	got, rawCount, err := s.GetMetricHistoryDownsampled(ctx, r.ID, "loss", target)
	if err != nil {
		t.Fatalf("downsample: %v", err)
	}
	if rawCount != total {
		t.Errorf("want rawCount=%d, got %d", total, rawCount)
	}
	if len(got) != target {
		t.Errorf("want %d downsampled points, got %d", target, len(got))
	}
	// Verify monotonically non-decreasing timestamps.
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp < got[i-1].Timestamp {
			t.Errorf("timestamps not monotonic at index %d: %d < %d", i, got[i].Timestamp, got[i-1].Timestamp)
		}
	}
	// First and last preserved.
	if got[0].Timestamp != ms[0].Timestamp {
		t.Errorf("first point not preserved: want ts=%d got ts=%d", ms[0].Timestamp, got[0].Timestamp)
	}
	if got[len(got)-1].Timestamp != ms[total-1].Timestamp {
		t.Errorf("last point not preserved: want ts=%d got ts=%d", ms[total-1].Timestamp, got[len(got)-1].Timestamp)
	}
}

func TestGetMetricHistoryDownsampled_SmallSeries(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "ds-small"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// 50 points, target 200 → all 50 returned (identity).
	for i := 0; i < 50; i++ {
		_ = s.LogMetric(ctx, r.ID, model.Metric{Key: "acc", Value: float64(i), Timestamp: int64(i + 1), Step: int64(i)})
	}

	got, rawCount, err := s.GetMetricHistoryDownsampled(ctx, r.ID, "acc", 200)
	if err != nil {
		t.Fatalf("downsample: %v", err)
	}
	if rawCount != 50 {
		t.Errorf("want rawCount=50, got %d", rawCount)
	}
	if len(got) != 50 {
		t.Errorf("want all 50 points returned, got %d", len(got))
	}
}

func TestMigrationIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	s, _ := store.OpenSQLite(context.Background(), path, dir)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}
	_ = s.Close()
}

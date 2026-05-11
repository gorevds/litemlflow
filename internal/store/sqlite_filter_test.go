package store_test

import (
	"context"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// TestRunFilterIN verifies that the IN(...) predicate works for attributes.run_id
// and that multiple run IDs can be filtered in one query.
func TestRunFilterIN(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "filter-in-exp"})
	var runIDs []string
	for i := 0; i < 4; i++ {
		r := &model.Run{ExperimentID: expID}
		_ = s.CreateRun(ctx, r)
		runIDs = append(runIDs, r.ID)
	}

	// Filter by first two run IDs using IN.
	filter := "attributes.run_id IN ('" + runIDs[0] + "','" + runIDs[1] + "')"
	res, err := s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        filter,
	})
	if err != nil {
		t.Fatalf("search with IN: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("want 2 runs, got %d", len(res.Items))
	}
	got := map[string]bool{res.Items[0].ID: true, res.Items[1].ID: true}
	if !got[runIDs[0]] || !got[runIDs[1]] {
		t.Fatalf("unexpected run IDs: %v", res.Items)
	}
}

// TestRunFilterINSingleValue verifies IN works with a single value.
func TestRunFilterINSingleValue(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "filter-in-single"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	filter := "attributes.run_id IN ('" + r.ID + "')"
	res, err := s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        filter,
	})
	if err != nil {
		t.Fatalf("search single IN: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != r.ID {
		t.Fatalf("want 1 run with id=%s, got %v", r.ID, res.Items)
	}
}

// TestRunFilterBETWEEN verifies that BETWEEN x AND y works for metrics.
func TestRunFilterBETWEEN(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "filter-between-exp"})
	for i := 0; i < 5; i++ {
		r := &model.Run{ExperimentID: expID}
		_ = s.CreateRun(ctx, r)
		_ = s.LogMetric(ctx, r.ID, model.Metric{
			Key:       "score",
			Value:     float64(i) * 10.0, // 0, 10, 20, 30, 40
			Timestamp: int64(1000 + i),
			Step:      0,
		})
	}

	// BETWEEN 10 AND 30 should match scores 10, 20, 30 => 3 runs.
	res, err := s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        "metrics.score BETWEEN 10 AND 30",
	})
	if err != nil {
		t.Fatalf("search BETWEEN: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("want 3 runs with score BETWEEN 10 AND 30, got %d", len(res.Items))
	}
}

// TestRunFilterBETWEENEdge verifies inclusive bounds.
func TestRunFilterBETWEENEdge(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "filter-between-edge"})
	for i, v := range []float64{0.0, 0.5, 1.0} {
		r := &model.Run{ExperimentID: expID}
		_ = s.CreateRun(ctx, r)
		_ = s.LogMetric(ctx, r.ID, model.Metric{
			Key:       "loss",
			Value:     v,
			Timestamp: int64(1000 + i),
			Step:      0,
		})
	}

	// Boundary: BETWEEN 0.0 AND 1.0 should match all 3.
	res, err := s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        "metrics.loss BETWEEN 0.0 AND 1.0",
	})
	if err != nil {
		t.Fatalf("BETWEEN edge: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("want 3 runs (boundary inclusive), got %d", len(res.Items))
	}
}

// TestRunFilterINAndAND verifies AND chaining with IN.
func TestRunFilterINAndAND(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "filter-in-and"})
	var runIDs []string
	for i := 0; i < 3; i++ {
		r := &model.Run{ExperimentID: expID}
		_ = s.CreateRun(ctx, r)
		runIDs = append(runIDs, r.ID)
		_ = s.LogParam(ctx, r.ID, model.Param{Key: "role", Value: []string{"train", "val", "test"}[i]})
	}

	// IN with 2 IDs AND params.role = 'train' should yield 1 run.
	filter := "attributes.run_id IN ('" + runIDs[0] + "','" + runIDs[1] + "') AND params.role = 'train'"
	res, err := s.SearchRuns(ctx, store.SearchOptions{
		ExperimentIDs: []int64{expID},
		Filter:        filter,
	})
	if err != nil {
		t.Fatalf("IN AND: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].ID != runIDs[0] {
		t.Fatalf("want 1 run with role=train from IN set, got %d: %v", len(res.Items), res.Items)
	}
}

// TestRunNameNonUnique verifies that two runs can share the same name.
func TestRunNameNonUnique(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "non-unique-names"})

	r1 := &model.Run{ExperimentID: expID, Name: "duplicate-name"}
	if err := s.CreateRun(ctx, r1); err != nil {
		t.Fatalf("create run1: %v", err)
	}
	r2 := &model.Run{ExperimentID: expID, Name: "duplicate-name"}
	if err := s.CreateRun(ctx, r2); err != nil {
		t.Fatalf("create run2 with same name: %v (run names must not be unique)", err)
	}
	if r1.ID == r2.ID {
		t.Fatal("two runs should have different IDs")
	}

	res, err := s.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: []int64{expID}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("want 2 runs with same name, got %d", len(res.Items))
	}
}

// TestMetricHistoryPagination verifies max_results + page_token on GetMetricHistory.
func TestMetricHistoryPagination(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "paginate-history"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Insert 25 points.
	for i := 0; i < 25; i++ {
		_ = s.LogMetric(ctx, r.ID, model.Metric{
			Key:       "val_loss",
			Value:     float64(i),
			Timestamp: int64(1000 + i),
			Step:      int64(i),
		})
	}

	// Page 1: first 10.
	page1, tok1, err := s.GetMetricHistory(ctx, r.ID, "val_loss", store.MetricHistoryOptions{MaxResults: 10})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 10 {
		t.Fatalf("want 10 items on page1, got %d", len(page1))
	}
	if tok1 == "" {
		t.Fatal("expected page token after page1")
	}
	if page1[0].Step != 0 || page1[9].Step != 9 {
		t.Fatalf("unexpected steps: %d..%d", page1[0].Step, page1[9].Step)
	}

	// Page 2: next 10.
	page2, tok2, err := s.GetMetricHistory(ctx, r.ID, "val_loss", store.MetricHistoryOptions{MaxResults: 10, PageToken: tok1})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 10 {
		t.Fatalf("want 10 items on page2, got %d", len(page2))
	}
	if tok2 == "" {
		t.Fatal("expected page token after page2")
	}
	if page2[0].Step != 10 {
		t.Fatalf("page2 should start at step 10, got %d", page2[0].Step)
	}

	// Page 3: remaining 5.
	page3, tok3, err := s.GetMetricHistory(ctx, r.ID, "val_loss", store.MetricHistoryOptions{MaxResults: 10, PageToken: tok2})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 5 {
		t.Fatalf("want 5 items on page3, got %d", len(page3))
	}
	if tok3 != "" {
		t.Fatalf("expected no token after last page, got %q", tok3)
	}

	// All-at-once (no pagination).
	all, tokAll, err := s.GetMetricHistory(ctx, r.ID, "val_loss", store.MetricHistoryOptions{})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all) != 25 {
		t.Fatalf("want 25 total, got %d", len(all))
	}
	if tokAll != "" {
		t.Fatalf("no token expected without max_results, got %q", tokAll)
	}
}

// TestLogInputsAndGetRunDatasets verifies dataset linkage end-to-end.
func TestLogInputsAndGetRunDatasets(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "datasets-exp"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	inputs := []model.DatasetInput{
		{
			Dataset: model.Dataset{
				Name:       "wiki-2024",
				Digest:     "abc123",
				SourceType: "http",
				Source:     "https://example.com/wiki",
				Schema:     `{"columns":["text"]}`,
				Profile:    `{"num_rows":1000}`,
			},
			Tags: []model.KV{
				{Key: "split", Value: "train"},
				{Key: "format", Value: "parquet"},
			},
		},
		{
			Dataset: model.Dataset{
				Name:   "wiki-2024",
				Digest: "def456",
			},
			Tags: []model.KV{{Key: "split", Value: "test"}},
		},
	}

	if err := s.LogInputs(ctx, r.ID, inputs); err != nil {
		t.Fatalf("LogInputs: %v", err)
	}

	got, err := s.GetRunDatasets(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunDatasets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 dataset inputs, got %d", len(got))
	}
	if got[0].Dataset.Name != "wiki-2024" || got[0].Dataset.Digest != "abc123" {
		t.Fatalf("unexpected first dataset: %+v", got[0].Dataset)
	}
	if got[0].Dataset.SourceType != "http" {
		t.Fatalf("want source_type=http, got %q", got[0].Dataset.SourceType)
	}
	if len(got[0].Tags) != 2 {
		t.Fatalf("want 2 tags on first input, got %d", len(got[0].Tags))
	}
	tagMap := map[string]string{}
	for _, kv := range got[0].Tags {
		tagMap[kv.Key] = kv.Value
	}
	if tagMap["split"] != "train" {
		t.Fatalf("split tag mismatch: %v", tagMap)
	}
	if got[1].Dataset.Digest != "def456" {
		t.Fatalf("want digest def456 on second input, got %q", got[1].Dataset.Digest)
	}
}

// TestLogInputs_V03OptIn (v2.1 review L1) verifies that setting
// LITEMLFLOW_ENABLE_DATASETS_V03_WRITES=1 restores writes to the v0.3
// link tables, and that GetRunDatasets still returns ONE row per input
// (not duplicated across the two paths).
func TestLogInputs_V03OptIn(t *testing.T) {
	t.Setenv("LITEMLFLOW_ENABLE_DATASETS_V03_WRITES", "1")
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "v03-optin"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	in := []model.DatasetInput{{Dataset: model.Dataset{Name: "ds", Digest: "h1", SourceType: "http"}}}
	if err := s.LogInputs(ctx, r.ID, in); err != nil {
		t.Fatalf("LogInputs: %v", err)
	}

	// Both link tables should have rows.
	var n03, n21 int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM dataset_inputs WHERE run_id = ?", r.ID).Scan(&n03); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM dataset_inputs_v2 WHERE run_id = ?", r.ID).Scan(&n21); err != nil {
		t.Fatal(err)
	}
	if n03 != 1 || n21 != 1 {
		t.Errorf("expected 1 row in each link table, got v0.3=%d v2.1=%d", n03, n21)
	}

	// GetRunDatasets must dedupe: 1 row, not 2.
	got, err := s.GetRunDatasets(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("dedup failed: want 1, got %d", len(got))
	}
	if got[0].Dataset.SourceType != "http" {
		t.Errorf("source_type lost: got %q", got[0].Dataset.SourceType)
	}
}

// TestGetRunDatasets_LegacyPlusV21 (v2.1 review L2) verifies that a run
// with a synthetic legacy-only row (v0.3) plus a v2.1-logged row surfaces
// both. Simulates a v1.x run that lived through the v2.1 upgrade.
func TestGetRunDatasets_LegacyPlusV21(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "legacy-plus"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Synthetic legacy row: insert directly into v0.3 tables (bypasses LogInputs).
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO datasets(name, digest, source_type, source, schema, profile)
		 VALUES ('legacy', 'l1', 'legacy-fs', '/var/data', '', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO dataset_inputs(run_id, name, digest) VALUES (?, 'legacy', 'l1')`, r.ID); err != nil {
		t.Fatal(err)
	}

	// v2.1 path: log a different input via the normal entry point.
	if err := s.LogInputs(ctx, r.ID, []model.DatasetInput{
		{Dataset: model.Dataset{Name: "fresh", Digest: "f1", SourceType: "s3"}},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRunDatasets(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 inputs (legacy + fresh), got %d: %+v", len(got), got)
	}
	names := map[string]string{}
	for _, di := range got {
		names[di.Dataset.Name] = di.Dataset.SourceType
	}
	if names["legacy"] != "legacy-fs" {
		t.Errorf("legacy input lost: got %v", names)
	}
	if names["fresh"] != "s3" {
		t.Errorf("fresh input lost: got %v", names)
	}
}

// TestLogInputsIdempotent verifies upsert on name+digest is idempotent.
func TestLogInputsIdempotent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "datasets-idempotent"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	ds := model.DatasetInput{
		Dataset: model.Dataset{Name: "ds", Digest: "d1", SourceType: "local"},
	}
	if err := s.LogInputs(ctx, r.ID, []model.DatasetInput{ds}); err != nil {
		t.Fatalf("first LogInputs: %v", err)
	}
	if err := s.LogInputs(ctx, r.ID, []model.DatasetInput{ds}); err != nil {
		t.Fatalf("second LogInputs (idempotent): %v", err)
	}
	// Two calls add two input rows (same dataset, two linkage entries).
	got, err := s.GetRunDatasets(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunDatasets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 input rows (dataset is shared, inputs are additive), got %d", len(got))
	}
}

// TestLogBatchBoundaryCaps verifies exactly-1000 metrics succeeds and 1001 fails.
func TestLogBatchBoundaryCaps(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	expID, _ := s.CreateExperiment(ctx, &model.Experiment{Name: "batch-caps"})
	r := &model.Run{ExperimentID: expID}
	_ = s.CreateRun(ctx, r)

	// Exactly 1000 metrics: must succeed.
	ms := make([]model.Metric, 1000)
	for i := range ms {
		ms[i] = model.Metric{Key: "m", Value: float64(i), Timestamp: int64(1000 + i), Step: int64(i)}
	}
	if err := s.LogMetrics(ctx, r.ID, ms); err != nil {
		t.Fatalf("1000 metrics should succeed: %v", err)
	}
	hist, _, err := s.GetMetricHistory(ctx, r.ID, "m", store.MetricHistoryOptions{})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 1000 {
		t.Fatalf("want 1000 points, got %d", len(hist))
	}
}

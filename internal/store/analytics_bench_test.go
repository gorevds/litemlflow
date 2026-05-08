package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// TestAnalyticsLatencyBudget verifies that the headline acceptance query
// "best metric per param last 30 days" answers in under 200 ms on a synthetic
// 100,000-run dataset.
//
// Skipped by default to keep -short builds fast. Run with:
//
//	go test -tags=bench -run TestAnalyticsLatency -timeout 5m ./internal/store
func TestAnalyticsLatencyBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skip in -short")
	}
	if !budgetTestEnabled() {
		t.Skip("set LITEMLFLOW_BENCH=1 to run; this test seeds 100k runs and may take 30+ seconds")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	s, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	const numExperiments = 50
	const numRuns = 100_000
	const numParams = 4
	rng := rand.New(rand.NewSource(42))

	expIDs := make([]int64, numExperiments)
	for i := 0; i < numExperiments; i++ {
		id, err := s.CreateExperiment(ctx, &model.Experiment{Name: fmt.Sprintf("bench-exp-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		expIDs[i] = id
	}

	models := []string{"gpt-4o-mini", "claude-3-5-haiku", "claude-3-5-sonnet", "llama-3.1-70b", "qwen-2.5"}
	now := time.Now().UnixMilli()

	t0 := time.Now()
	t.Logf("seeding %d runs across %d experiments…", numRuns, numExperiments)
	for i := 0; i < numRuns; i++ {
		id := fmt.Sprintf("r%07d", i)
		expID := expIDs[i%numExperiments]
		startTime := now - int64(rng.Intn(60*24*3600*1000)) // up to 60 days back

		r := &model.Run{
			ID: id, ExperimentID: expID,
			Status: "FINISHED", StartTime: startTime,
			LifecycleStage: model.LifecycleActive, Kind: model.KindClassic,
			ArtifactURI: "mlflow-artifacts:/" + id,
		}
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		// One param "model" with one of N values.
		if err := s.LogParam(ctx, id, model.Param{Key: "model", Value: models[i%len(models)]}); err != nil {
			t.Fatal(err)
		}
		_ = numParams // keeps the constant referenced
		// One metric eval/f1 with a value.
		if err := s.LogMetric(ctx, id, model.Metric{
			Key: "eval/f1", Value: 0.5 + rng.Float64()*0.5, Timestamp: startTime, Step: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seed: %s", time.Since(t0))

	// Now run the headline query: "best eval/f1 per model in the last 30 days".
	thirtyDaysMs := int64(30 * 24 * 3600 * 1000)
	q := store.AnalyticsQuery{
		Metric:  "eval/f1",
		Agg:     "max",
		GroupBy: "params.model",
		Where: store.AnalyticsWhere{
			TimeAfter: now - thirtyDaysMs,
			Status:    []string{"FINISHED"},
		},
	}

	// Run ANALYZE so the planner has stats. (This is one-time at backfill
	// time in production; we issue it here too so the bench reflects how a
	// long-lived production DB performs.)
	if _, err := s.DB().ExecContext(ctx, "ANALYZE"); err != nil {
		t.Logf("ANALYZE: %v", err)
	}

	// Warm cache, then time.
	if _, err := s.AnalyticsQuery(ctx, q); err != nil {
		t.Fatal(err)
	}
	tStart := time.Now()
	res, err := s.AnalyticsQuery(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	wallMS := time.Since(tStart).Milliseconds()

	t.Logf("headline query: %d groups, %d runs scanned, server-reported %dms, wall %dms",
		len(res.Rows), res.TotalRunsScanned, res.ExecutionMS, wallMS)
	for _, r := range res.Rows {
		t.Logf("  %-20s f1=%.4f runs=%d best=%s", r.Group, r.AggValue, r.RunCount, r.BestRunID)
	}

	const budget = int64(200)
	if wallMS > budget {
		t.Errorf("wall latency %dms exceeds %dms budget", wallMS, budget)
	}
}

func budgetTestEnabled() bool {
	return os.Getenv("LITEMLFLOW_BENCH") == "1"
}

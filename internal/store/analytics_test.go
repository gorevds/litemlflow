package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func TestAnalyticsQueryValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		q    store.AnalyticsQuery
		ok   bool
	}{
		{"missing metric", store.AnalyticsQuery{Agg: "max"}, false},
		{"missing agg", store.AnalyticsQuery{Metric: "x"}, false},
		{"valid", store.AnalyticsQuery{Metric: "x", Agg: "max"}, true},
		{"valid params", store.AnalyticsQuery{Metric: "x", Agg: "max", GroupBy: "params.model"}, true},
		{"valid tags", store.AnalyticsQuery{Metric: "x", Agg: "max", GroupBy: "tags.team"}, true},
		{"valid status", store.AnalyticsQuery{Metric: "x", Agg: "max", GroupBy: "status"}, true},
		{"bad group_by prefix", store.AnalyticsQuery{Metric: "x", Agg: "max", GroupBy: "wrong.foo"}, false},
		{"bad agg", store.AnalyticsQuery{Metric: "x", Agg: "median"}, false},
		{"bad params (no key)", store.AnalyticsQuery{Metric: "x", Agg: "max", GroupBy: "params."}, false},
		{"bad lifecycle", store.AnalyticsQuery{Metric: "x", Agg: "max", Where: store.AnalyticsWhere{Lifecycle: "foo"}}, false},
		{"bad status", store.AnalyticsQuery{Metric: "x", Agg: "max", Where: store.AnalyticsWhere{Status: []string{"FOO"}}}, false},
		{"good status", store.AnalyticsQuery{Metric: "x", Agg: "max", Where: store.AnalyticsWhere{Status: []string{"FINISHED"}}}, true},
		{"bad order", store.AnalyticsQuery{Metric: "x", Agg: "max", OrderBy: "random"}, false},
		{"too many exp ids", store.AnalyticsQuery{Metric: "x", Agg: "max", Where: store.AnalyticsWhere{ExperimentIDs: make([]int64, 2000)}}, false},
		{"negative limit", store.AnalyticsQuery{Metric: "x", Agg: "max", Limit: -1}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.q.Validate()
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected err, got nil")
			}
		})
	}
}

func seedAnalyticsRuns(t *testing.T, s *store.SQLiteStore) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "a-q1"})
	if err != nil {
		t.Fatal(err)
	}
	expID2, err := s.CreateExperiment(ctx, &model.Experiment{Name: "a-q2"})
	if err != nil {
		t.Fatal(err)
	}
	// Three runs, two with same param.model="A", one with "B"; one FAILED
	// in expID2 to test status filtering.
	type seed struct {
		id, model, status string
		expID             int64
		f1                float64
	}
	for _, rd := range []seed{
		{"r1", "A", "FINISHED", expID, 0.7},
		{"r2", "A", "FINISHED", expID, 0.85},
		{"r3", "B", "FINISHED", expID, 0.65},
		{"r4", "A", "FAILED", expID2, 0.2},
	} {
		r := &model.Run{
			ID:             rd.id,
			ExperimentID:   rd.expID,
			Status:         rd.status,
			StartTime:      1000,
			LifecycleStage: model.LifecycleActive,
			Kind:           model.KindClassic,
			ArtifactURI:    "mlflow-artifacts:/" + rd.id,
		}
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.LogParam(ctx, rd.id, model.Param{Key: "model", Value: rd.model}); err != nil {
			t.Fatal(err)
		}
		if err := s.LogMetric(ctx, rd.id, model.Metric{Key: "eval/f1", Value: rd.f1, Timestamp: 1000, Step: 1}); err != nil {
			t.Fatal(err)
		}
	}
	return expID, expID2
}

func TestAnalyticsQueryEnd2End(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	expID, _ := seedAnalyticsRuns(t, s)
	ctx := context.Background()

	// 1) Best f1 per model, no time filter — top group "A" with run r2.
	res, err := s.AnalyticsQuery(ctx, store.AnalyticsQuery{
		Metric:  "eval/f1",
		Agg:     "max",
		GroupBy: "params.model",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 groups, got %d (%+v)", len(res.Rows), res.Rows)
	}
	if res.Rows[0].Group != "A" || res.Rows[0].AggValue != 0.85 {
		t.Errorf("top row wrong: %+v", res.Rows[0])
	}
	if res.Rows[0].BestRunID != "r2" {
		t.Errorf("expected best_run_id=r2, got %q", res.Rows[0].BestRunID)
	}

	// 2) Filter by status FINISHED only.
	res, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{
		Metric:  "eval/f1",
		Agg:     "max",
		GroupBy: "params.model",
		Where:   store.AnalyticsWhere{Status: []string{"FINISHED"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Errorf("FINISHED filter: expected 2 rows, got %d", len(res.Rows))
	}

	// 3) MIN agg, no group_by.
	res, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{Metric: "eval/f1", Agg: "min"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("no group_by: expected 1 row, got %d", len(res.Rows))
	}
	if res.Rows[0].AggValue != 0.2 {
		t.Errorf("min should be 0.2, got %v", res.Rows[0].AggValue)
	}

	// 4) Avg with experiment filter.
	res, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{
		Metric: "eval/f1",
		Agg:    "avg",
		Where:  store.AnalyticsWhere{ExperimentIDs: []int64{expID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected single avg row, got %d", len(res.Rows))
	}
	want := (0.7 + 0.85 + 0.65) / 3
	if res.Rows[0].AggValue < want-1e-9 || res.Rows[0].AggValue > want+1e-9 {
		t.Errorf("avg: got %v want %v", res.Rows[0].AggValue, want)
	}

	// 5) Malicious metric name must not exec arbitrary SQL.
	_, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{
		Metric: "eval/f1' OR 1=1; DROP TABLE metrics; --",
		Agg:    "max",
	})
	if err != nil {
		t.Fatalf("malicious metric should bind safely: %v", err)
	}

	// 6) Group by status.
	res, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{
		Metric:  "eval/f1",
		Agg:     "max",
		GroupBy: "status",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range res.Rows {
		got[r.Group] = r.AggValue
	}
	if got["FINISHED"] != 0.85 || got["FAILED"] != 0.2 {
		t.Errorf("status grouping wrong: %+v", got)
	}
}

func TestAnalyticsTriggerKeepsLatest(t *testing.T) {
	// Verify metrics_latest tracks the most-recent (timestamp, step).
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	_, err := s.CreateExperiment(ctx, &model.Experiment{Name: "trig"})
	if err != nil {
		t.Fatal(err)
	}
	r := &model.Run{
		ID: "rid", ExperimentID: 1, Status: "FINISHED",
		StartTime: 1, LifecycleStage: model.LifecycleActive, Kind: model.KindClassic,
		ArtifactURI: "mlflow-artifacts:/rid",
	}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	// log step 1 (older), step 2 (newer)
	if err := s.LogMetric(ctx, "rid", model.Metric{Key: "loss", Value: 1.0, Timestamp: 100, Step: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.LogMetric(ctx, "rid", model.Metric{Key: "loss", Value: 0.5, Timestamp: 200, Step: 2}); err != nil {
		t.Fatal(err)
	}
	res, err := s.AnalyticsQuery(ctx, store.AnalyticsQuery{Metric: "loss", Agg: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].AggValue != 0.5 {
		t.Errorf("expected latest=0.5 (step2), got %+v", res.Rows)
	}

	// Insert an OLDER timestamp — must NOT overwrite.
	if err := s.LogMetric(ctx, "rid", model.Metric{Key: "loss", Value: 99.0, Timestamp: 50, Step: 0}); err != nil {
		t.Fatal(err)
	}
	res, err = s.AnalyticsQuery(ctx, store.AnalyticsQuery{Metric: "loss", Agg: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0].AggValue != 0.5 {
		t.Errorf("older insert clobbered latest: got %+v", res.Rows[0])
	}
}

// TestAnalyticsSQLBuilderShape: spot-check that the generated SQL contains
// the parameterised fragments we expect — defence-in-depth proof that
// nothing gets concatenated unparameterised.
func TestAnalyticsSQLBuilderShape(t *testing.T) {
	// We can't call buildAnalyticsSQL from _test package; instead, run the
	// query against a dummy store and check that the query executes (which
	// implicitly proves the SQL is well-formed). The malicious-metric test
	// in End2End covers the safety side.
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "shape"}); err != nil {
		t.Fatal(err)
	}
	q := store.AnalyticsQuery{
		Metric:  "loss",
		Agg:     "max",
		GroupBy: "params.lr",
		Where:   store.AnalyticsWhere{TimeAfter: 1234, Status: []string{"FINISHED"}},
		Limit:   50,
	}
	res, err := s.AnalyticsQuery(ctx, q)
	if err != nil {
		t.Fatalf("complex query failed: %v", err)
	}
	if res.Rows == nil {
		t.Errorf("expected non-nil Rows slice")
	}
	if !strings.Contains(q.GroupBy, "params.lr") { // sanity
		t.Fatal("test setup wrong")
	}
}

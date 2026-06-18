package store_test

// Guards the P1 follow-up to keyset pagination (independent-review): SearchRuns
// must support order_by on metrics./params./tags. keys, not only run attributes.
// MLflow clients routinely send order_by=["metrics.accuracy DESC"]; before this
// change those requests errored with "unsupported order_by column".
//
// The hard part is keyset pagination over a nullable computed column: runs that
// never logged the ordered key must sort LAST (in both ASC and DESC) and paging
// across them must still return every run exactly once.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// orderedIDs pages through SearchRuns with the given order_by and returns the
// run IDs in result order, asserting paging terminates and never duplicates.
func orderedIDs(t *testing.T, st *store.SQLiteStore, exp int64, orderBy []string, pageSize int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	seen := map[string]bool{}
	token := ""
	for iters := 0; ; iters++ {
		if iters > 100 {
			t.Fatal("pagination did not terminate")
		}
		res, err := st.SearchRuns(ctx, store.SearchOptions{
			ExperimentIDs: []int64{exp}, MaxResults: pageSize, OrderBy: orderBy, PageToken: token,
		})
		if err != nil {
			t.Fatalf("search runs (order=%v): %v", orderBy, err)
		}
		for _, r := range res.Items {
			if seen[r.ID] {
				t.Fatalf("run %s returned more than once paging order=%v", r.ID, orderBy)
			}
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
		token = res.NextPageToken
		if token == "" {
			break
		}
	}
	return ids
}

func TestSearchRunsOrderByMetric(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "order-metric")

	// Four runs with distinct accuracy; a fifth with no accuracy metric.
	type rc struct {
		name string
		acc  *float64
	}
	f := func(v float64) *float64 { return &v }
	runs := []rc{
		{"a", f(0.10)},
		{"b", f(0.90)},
		{"c", f(0.50)},
		{"d", f(0.30)},
		{"e", nil}, // no accuracy → must sort last
	}
	byName := map[string]string{}
	for _, c := range runs {
		r := &model.Run{ExperimentID: exp, Name: c.name, StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		byName[c.name] = r.ID
		if c.acc != nil {
			if err := st.LogMetric(ctx, r.ID, model.Metric{Key: "accuracy", Value: *c.acc, Timestamp: 1, Step: 0}); err != nil {
				t.Fatalf("log metric: %v", err)
			}
		}
	}

	// DESC: 0.90, 0.50, 0.30, 0.10, then the run with no metric last.
	got := orderedIDs(t, st, exp, []string{"metrics.accuracy DESC"}, 2)
	wantDesc := []string{byName["b"], byName["c"], byName["d"], byName["a"], byName["e"]}
	if fmt.Sprint(got) != fmt.Sprint(wantDesc) {
		t.Errorf("DESC order: got %v want %v", got, wantDesc)
	}

	// ASC: 0.10, 0.30, 0.50, 0.90, then the run with no metric last.
	got = orderedIDs(t, st, exp, []string{"metrics.accuracy ASC"}, 2)
	wantAsc := []string{byName["a"], byName["d"], byName["c"], byName["b"], byName["e"]}
	if fmt.Sprint(got) != fmt.Sprint(wantAsc) {
		t.Errorf("ASC order: got %v want %v", got, wantAsc)
	}
}

func TestSearchRunsOrderByParam(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "order-param")

	vals := map[string]string{"a": "alpha", "b": "gamma", "c": "beta"}
	byName := map[string]string{}
	for name, v := range vals {
		r := &model.Run{ExperimentID: exp, Name: name, StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		byName[name] = r.ID
		if err := st.LogParam(ctx, r.ID, model.Param{Key: "optimizer", Value: v}); err != nil {
			t.Fatalf("log param: %v", err)
		}
	}
	got := orderedIDs(t, st, exp, []string{"params.optimizer ASC"}, 2)
	want := []string{byName["a"], byName["c"], byName["b"]} // alpha, beta, gamma
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("param ASC order: got %v want %v", got, want)
	}
}

func TestSearchRunsOrderByRunName(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "order-runname")

	names := []string{"charlie", "alpha", "bravo"}
	byName := map[string]string{}
	for _, n := range names {
		r := &model.Run{ExperimentID: exp, Name: n, StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		byName[n] = r.ID
	}
	// MLflow supports ordering by run_name; the filter whitelist already
	// accepts it, so order_by must too (parity with parseRunFilter).
	got := orderedIDs(t, st, exp, []string{"attributes.run_name ASC"}, 2)
	want := []string{byName["alpha"], byName["bravo"], byName["charlie"]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("run_name ASC order: got %v want %v", got, want)
	}
}

func TestSearchRunsOrderByTag(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "order-tag")

	vals := map[string]string{"a": "v3", "b": "v1", "c": "v2"}
	byName := map[string]string{}
	for name, v := range vals {
		r := &model.Run{ExperimentID: exp, Name: name, StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		byName[name] = r.ID
		if err := st.SetTag(ctx, r.ID, model.KV{Key: "stage", Value: v}); err != nil {
			t.Fatalf("set tag: %v", err)
		}
	}
	got := orderedIDs(t, st, exp, []string{"tags.stage DESC"}, 2)
	want := []string{byName["a"], byName["c"], byName["b"]} // v3, v2, v1
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tag DESC order: got %v want %v", got, want)
	}
}

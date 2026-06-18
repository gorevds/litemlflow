package store_test

// Guards independent-review finding 2.4: SearchRuns/SearchExperiments emitted a
// NextPageToken but never consumed PageToken, so a paging client looped on page
// one and duplicated rows. These tests page through more rows than a page holds
// and assert every row is returned exactly once and that paging terminates.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

func TestSearchRunsPaginationKeyset(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "pag-runs")

	// Five runs; two share a start_time to exercise the id tie-break.
	starts := []int64{100, 100, 200, 300, 400}
	want := map[string]bool{}
	for i, s := range starts {
		r := &model.Run{ExperimentID: exp, Name: fmt.Sprintf("r%d", i), StartTime: s, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		want[r.ID] = true
	}

	seen := map[string]int{}
	token := ""
	for iters := 0; ; iters++ {
		if iters > 10 {
			t.Fatal("pagination did not terminate (token never cleared)")
		}
		res, err := st.SearchRuns(ctx, store.SearchOptions{ExperimentIDs: []int64{exp}, MaxResults: 2, PageToken: token})
		if err != nil {
			t.Fatalf("search runs: %v", err)
		}
		for _, r := range res.Items {
			seen[r.ID]++
		}
		token = res.NextPageToken
		if token == "" {
			break
		}
	}
	if len(seen) != len(want) {
		t.Errorf("want %d distinct runs across pages, got %d", len(want), len(seen))
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("run %s returned %d times across pages (expected exactly once)", id, c)
		}
	}
}

func TestSearchExperimentsPaginationKeyset(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	want := map[int64]bool{}
	for i := 0; i < 5; i++ {
		id := mustCreateExpInStore(t, st, fmt.Sprintf("pag-exp-%d", i))
		want[id] = true
	}

	seen := map[int64]int{}
	token := ""
	for iters := 0; ; iters++ {
		if iters > 12 {
			t.Fatal("pagination did not terminate (token never cleared)")
		}
		res, err := st.SearchExperiments(ctx, store.SearchOptions{MaxResults: 2, PageToken: token, LifecycleStage: "all"})
		if err != nil {
			t.Fatalf("search experiments: %v", err)
		}
		for _, e := range res.Items {
			seen[e.ID]++
		}
		token = res.NextPageToken
		if token == "" {
			break
		}
	}
	// >= want because a 'default' workspace may carry seed experiments; the key
	// assertion is no duplicates and termination.
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("experiment %d returned %d times across pages (expected exactly once)", id, seen[id])
		}
	}
}

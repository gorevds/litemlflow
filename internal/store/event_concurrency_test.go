package store_test

// Stresses concurrent run mutations after the event-log transactionalization.
// Each mutation now opens its own txn; we capture the `before` state before
// BeginTx so the txn writes first, avoiding a read→write lock upgrade that
// could fail with SQLITE_BUSY_SNAPSHOT under contention. This test fails (with
// "database is locked" / busy errors) if that ordering regresses.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

func TestConcurrentRunMutations(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "concurrent")

	const nRuns = 8
	runIDs := make([]string, nRuns)
	for i := range runIDs {
		r := &model.Run{ExperimentID: exp, Name: fmt.Sprintf("r%d", i), StartTime: 100, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
		if err := st.CreateRun(ctx, r); err != nil {
			t.Fatalf("create run: %v", err)
		}
		runIDs[i] = r.ID
	}

	var wg sync.WaitGroup
	errCh := make(chan error, nRuns*3)
	for _, id := range runIDs {
		id := id
		for k := 0; k < 3; k++ {
			wg.Add(1)
			go func(iter int) {
				defer wg.Done()
				status := model.StatusFinished
				if err := st.UpdateRun(ctx, id, &status, nil, nil); err != nil {
					errCh <- fmt.Errorf("update %s: %w", id, err)
				}
				if err := st.SetTag(ctx, id, model.KV{Key: fmt.Sprintf("k%d", iter), Value: "v"}); err != nil {
					errCh <- fmt.Errorf("settag %s: %w", id, err)
				}
			}(k)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

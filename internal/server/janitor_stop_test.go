package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorevds/litemlflow/internal/server"
)

// countingJanitorStore counts janitor sweep calls.
type countingJanitorStore struct {
	mu    sync.Mutex
	calls int
}

func (c *countingJanitorStore) ArchiveStaleRuns(_ context.Context, _ int64) (int, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return 0, nil
}
func (c *countingJanitorStore) PruneEventsBefore(_ context.Context, _ int64) (int, error) {
	return 0, nil
}
func (c *countingJanitorStore) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// TestStartJanitorStopHalts guards independent-review: the stop function must
// cancel the janitor and wait for its goroutine to exit, so no sweep runs
// after stop() returns (the caller then closes the store safely).
func TestStartJanitorStopHalts(t *testing.T) {
	st := &countingJanitorStore{}
	stop := server.StartJanitor(context.Background(), st, 10*time.Millisecond, time.Hour, 0, nil)

	time.Sleep(60 * time.Millisecond) // let it tick a few times
	stop()                            // cancel + await goroutine exit
	after := st.n()
	if after == 0 {
		t.Fatal("janitor never ran before stop()")
	}

	time.Sleep(60 * time.Millisecond) // no further sweeps must happen
	if final := st.n(); final != after {
		t.Errorf("janitor kept sweeping after stop(): %d -> %d", after, final)
	}
}

package server

import (
	"context"
	"log/slog"
	"time"
)

// JanitorStore is the minimal store interface the janitor needs.
type JanitorStore interface {
	ArchiveStaleRuns(ctx context.Context, staleBefore int64) (int, error)
	// PruneEventsBefore deletes time-travel event rows older than the
	// cutoff (unix ms). Returns the number of rows deleted. Optional —
	// only invoked when retention is configured.
	PruneEventsBefore(ctx context.Context, beforeMs int64) (int, error)
}

// StartJanitor launches a background goroutine that scans for stale RUNNING
// runs and transitions them to FAILED every interval. The goroutine exits when
// ctx is canceled. staleAfter is the maximum age of a RUNNING run before it is
// considered stale (e.g. 24h).
//
// eventsRetention > 0 enables the v1.5 event-log pruner; older events are
// deleted on the same tick. eventsRetention == 0 disables pruning (events
// grow monotonically).
//
// Backpressure / error handling: each tick runs both sweeps sequentially.
// If one returns an error it is logged and the other still runs.
// The returned stop function cancels the janitor and BLOCKS until its
// goroutine has fully exited. Callers must invoke it before closing the store
// (independent-review): otherwise a tick in flight during shutdown runs a
// query against an already-closed database, logging "database is closed" on
// every SIGTERM mid-tick.
func StartJanitor(ctx context.Context, st JanitorStore, interval, staleAfter, eventsRetention time.Duration, logger *slog.Logger) (stop func()) {
	if logger == nil {
		logger = slog.Default()
	}
	jctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-jctx.Done():
				return
			case tick := <-t.C:
				staleBefore := tick.Add(-staleAfter).UnixMilli()
				if n, err := st.ArchiveStaleRuns(jctx, staleBefore); err != nil {
					logger.Error("janitor: archive stale runs failed", slog.String("err", err.Error()))
				} else if n > 0 {
					logger.Info("janitor: archived stale runs", slog.Int("count", n))
				}
				if eventsRetention > 0 {
					eventsBefore := tick.Add(-eventsRetention).UnixMilli()
					if n, err := st.PruneEventsBefore(jctx, eventsBefore); err != nil {
						logger.Error("janitor: prune events failed", slog.String("err", err.Error()))
					} else if n > 0 {
						logger.Info("janitor: pruned events", slog.Int("count", n))
					}
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

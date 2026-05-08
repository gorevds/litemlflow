package server

import (
	"context"
	"log/slog"
	"time"
)

// JanitorStore is the minimal store interface the janitor needs.
type JanitorStore interface {
	ArchiveStaleRuns(ctx context.Context, staleBefore int64) (int, error)
}

// StartJanitor launches a background goroutine that scans for stale RUNNING
// runs and transitions them to FAILED every interval. The goroutine exits when
// ctx is canceled. staleAfter is the maximum age of a RUNNING run before it is
// considered stale (e.g. 24h).
//
// Backpressure / error handling: each tick is a single store call. If the store
// returns an error the tick is logged and skipped; the next tick will retry.
func StartJanitor(ctx context.Context, st JanitorStore, interval, staleAfter time.Duration, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case tick := <-t.C:
				staleBefore := tick.Add(-staleAfter).UnixMilli()
				n, err := st.ArchiveStaleRuns(ctx, staleBefore)
				if err != nil {
					logger.Error("janitor: archive stale runs failed", slog.String("err", err.Error()))
					continue
				}
				if n > 0 {
					logger.Info("janitor: archived stale runs", slog.Int("count", n))
				}
			}
		}
	}()
}

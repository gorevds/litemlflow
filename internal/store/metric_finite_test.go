package store_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// TestLogMetricRejectsNonFinite guards independent-review 2.5: NaN/Inf metric
// values cannot be JSON-encoded on read, so they must be rejected on write with
// a validation error (mapped to 400), not stored.
func TestLogMetricRejectsNonFinite(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	exp := mustCreateExpInStore(t, st, "finite")
	r := &model.Run{ExperimentID: exp, Name: "r", StartTime: 1, Status: "RUNNING", LifecycleStage: "active", Kind: "classic"}
	if err := st.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		err := st.LogMetric(ctx, r.ID, model.Metric{Key: "loss", Value: v, Timestamp: 1, Step: 0})
		if !errors.Is(err, store.ErrInvalidValue) {
			t.Errorf("LogMetric(%v): want ErrInvalidValue, got %v", v, err)
		}
	}
	// A finite value still succeeds.
	if err := st.LogMetric(ctx, r.ID, model.Metric{Key: "loss", Value: 0.5, Timestamp: 1, Step: 0}); err != nil {
		t.Errorf("LogMetric(finite): unexpected error %v", err)
	}
}

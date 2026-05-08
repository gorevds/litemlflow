package store_test

import (
	"math/rand"
	"testing"

	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
)

// makeMetrics builds a slice of model.Metric with sequential timestamps and
// the provided values. Step is set to the index.
func makeMetrics(vals []float64) []model.Metric {
	ms := make([]model.Metric, len(vals))
	for i, v := range vals {
		ms[i] = model.Metric{
			Key:       "m",
			Value:     v,
			Timestamp: int64(i + 1),
			Step:      int64(i),
		}
	}
	return ms
}

func TestDownsampleLTTB_Identity(t *testing.T) {
	t.Parallel()
	pts := makeMetrics([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	got := store.DownsampleLTTB(pts, 10)
	if len(got) != 10 {
		t.Fatalf("want 10 points (identity), got %d", len(got))
	}
}

func TestDownsampleLTTB_TargetLessThanInput(t *testing.T) {
	t.Parallel()
	pts := makeMetrics([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	got := store.DownsampleLTTB(pts, 3)
	if len(got) != 3 {
		t.Fatalf("want 3 points, got %d", len(got))
	}
	// First and last must be preserved.
	if got[0].Timestamp != pts[0].Timestamp {
		t.Errorf("first point not preserved: want ts=%d got ts=%d", pts[0].Timestamp, got[0].Timestamp)
	}
	if got[len(got)-1].Timestamp != pts[len(pts)-1].Timestamp {
		t.Errorf("last point not preserved: want ts=%d got ts=%d", pts[len(pts)-1].Timestamp, got[len(got)-1].Timestamp)
	}
}

func TestDownsampleLTTB_1000to100(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(42))
	vals := make([]float64, 1000)
	for i := range vals {
		vals[i] = rng.Float64() * 100
	}
	pts := makeMetrics(vals)
	got := store.DownsampleLTTB(pts, 100)
	if len(got) != 100 {
		t.Fatalf("want exactly 100 points, got %d", len(got))
	}
}

func TestDownsampleLTTB_MonotonicPreservesFirstLast(t *testing.T) {
	t.Parallel()
	vals := make([]float64, 1000)
	for i := range vals {
		vals[i] = float64(i)
	}
	pts := makeMetrics(vals)
	got := store.DownsampleLTTB(pts, 100)
	if got[0].Timestamp != pts[0].Timestamp || got[0].Value != pts[0].Value {
		t.Error("first point not preserved in monotonic series")
	}
	last := pts[len(pts)-1]
	gotLast := got[len(got)-1]
	if gotLast.Timestamp != last.Timestamp || gotLast.Value != last.Value {
		t.Error("last point not preserved in monotonic series")
	}
}

func TestDownsampleLTTB_TargetBelowThree(t *testing.T) {
	t.Parallel()
	pts := makeMetrics([]float64{1, 2, 3, 4, 5})
	// target < 3 → return all points unchanged.
	for _, tgt := range []int{0, 1, 2} {
		got := store.DownsampleLTTB(pts, tgt)
		if len(got) != len(pts) {
			t.Errorf("target=%d: want all %d points, got %d", tgt, len(pts), len(got))
		}
	}
}

func TestDownsampleLTTB_OrderPreserved(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(7))
	vals := make([]float64, 500)
	for i := range vals {
		vals[i] = rng.Float64()
	}
	pts := makeMetrics(vals)
	got := store.DownsampleLTTB(pts, 50)
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp < got[i-1].Timestamp {
			t.Errorf("order violated at index %d: ts[%d]=%d < ts[%d]=%d",
				i, i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

func TestDownsampleLTTB_EmptyAndSingle(t *testing.T) {
	t.Parallel()
	// Empty slice.
	got := store.DownsampleLTTB(nil, 100)
	if got != nil && len(got) != 0 {
		t.Errorf("empty: want nil/empty, got len=%d", len(got))
	}
	// Single point.
	pts := makeMetrics([]float64{42})
	got2 := store.DownsampleLTTB(pts, 100)
	if len(got2) != 1 {
		t.Errorf("single: want 1, got %d", len(got2))
	}
}

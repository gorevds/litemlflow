package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

// Regression for v1.2 review CRITICAL #1: concurrent MlflowClient.log_input
// calls with identical (workspace, name, digest) used to race on the
// "MAX(version) + 1" path inside mirrorIntoDatasetsV2 and either error out
// (UNIQUE constraint failed, aborting LogInputs entirely) or create
// duplicate rows.
//
// Fix: SAVEPOINT + retry on UNIQUE failure. The expected outcome is
// idempotent — exactly one datasets_v2 row exists after N concurrent
// log_inputs.
func TestLogInputsConcurrentMirror(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	expID, err := s.CreateExperiment(ctx, &model.Experiment{Name: "race"})
	if err != nil {
		t.Fatal(err)
	}
	// Need a single run to call log_inputs against.
	r := &model.Run{
		ID: "race-run", ExperimentID: expID, Status: "FINISHED", StartTime: 1,
		LifecycleStage: model.LifecycleActive, Kind: model.KindClassic,
		ArtifactURI: "x",
	}
	if err := s.CreateRun(ctx, r); err != nil {
		t.Fatal(err)
	}

	const N = 8
	input := []model.DatasetInput{{
		Dataset: model.Dataset{
			Name: "shared",
			Digest: "deadbeef" + "deadbeef" + "deadbeef" + "deadbeef" +
				"deadbeef" + "deadbeef" + "deadbeef" + "deadbeef",
			Source: "s3://x/y",
		},
	}}

	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.LogInputs(ctx, "race-run", input)
		}(i)
	}
	wg.Wait()

	// All N calls should succeed (the savepoint + retry swallows UNIQUE
	// races on the mirror without poisoning the LogInputs txn).
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", i, err)
		}
	}

	// Exactly one datasets_v2 row should exist for (workspace, name).
	versions, err := s.ListDatasetVersions(ctx, "default", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 {
		t.Errorf("expected 1 mirrored version after %d concurrent log_inputs, got %d", N, len(versions))
		for _, v := range versions {
			t.Logf("  v%d hash=%s", v.Version, v.ContentHash)
		}
	}
}

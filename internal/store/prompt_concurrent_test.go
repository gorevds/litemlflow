package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gorevds/litemlflow/internal/model"
)

// Concurrent CreatePrompt calls for the same name used to race on
// MAX(version)+1 and fail with a UNIQUE violation / SQLITE_BUSY. With the
// retry every call succeeds and versions are dense and unique.
func TestCreatePromptConcurrentVersions(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	const N = 16
	var wg sync.WaitGroup
	errs := make([]error, N)
	versions := make([]int64, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			versions[i], errs[i] = s.CreatePrompt(ctx, "default",
				&model.Prompt{Name: "racy", Content: fmt.Sprintf("content %d", i)})
		}(i)
	}
	wg.Wait()

	seen := map[int64]bool{}
	for i := 0; i < N; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if seen[versions[i]] {
			t.Fatalf("duplicate version %d returned", versions[i])
		}
		seen[versions[i]] = true
	}
	all, err := s.ListPromptVersions(ctx, "default", "racy")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != N {
		t.Fatalf("want %d versions, got %d", N, len(all))
	}
	for v := int64(1); v <= N; v++ {
		if !seen[v] {
			t.Errorf("version %d missing", v)
		}
	}
}

// Concurrent creates of identical content dedupe to a single version.
func TestCreatePromptConcurrentSameContent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	const N = 8
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CreatePrompt(ctx, "default", &model.Prompt{Name: "same", Content: "identical"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	all, err := s.ListPromptVersions(ctx, "default", "same")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 version for identical content, got %d", len(all))
	}
}

package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
)

// newRegisteredModel is a test helper that creates a registered model and fails
// the test if there is an error.
func newRegisteredModel(t *testing.T, s *store.SQLiteStore, name string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateRegisteredModel(ctx, &model.RegisteredModel{Name: name}); err != nil {
		t.Fatalf("CreateRegisteredModel(%q): %v", name, err)
	}
}

func newModelVersion(t *testing.T, s *store.SQLiteStore, name, source string) *model.ModelVersion {
	t.Helper()
	ctx := context.Background()
	mv, err := s.CreateModelVersion(ctx, &model.ModelVersion{
		Name:   name,
		Source: source,
	})
	if err != nil {
		t.Fatalf("CreateModelVersion(%q, %q): %v", name, source, err)
	}
	return mv
}

// ---------- Registered Model CRUD --------------------------------------------

func TestRegisteredModelCRUD(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	// Create.
	if err := s.CreateRegisteredModel(ctx, &model.RegisteredModel{
		Name:        "mymodel",
		Description: "desc",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Duplicate name.
	if err := s.CreateRegisteredModel(ctx, &model.RegisteredModel{Name: "mymodel"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}

	// Get.
	m, err := s.GetRegisteredModel(ctx, "mymodel")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if m.Name != "mymodel" {
		t.Fatalf("want name=mymodel, got %s", m.Name)
	}
	if m.Description != "desc" {
		t.Fatalf("want description=desc, got %s", m.Description)
	}
	if m.CreationTime == 0 {
		t.Fatal("creation_time should be non-zero")
	}

	// Not found.
	if _, err := s.GetRegisteredModel(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// Update description.
	desc := "updated"
	m2, err := s.UpdateRegisteredModel(ctx, "mymodel", &desc)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if m2.Description != "updated" {
		t.Fatalf("want description=updated, got %s", m2.Description)
	}

	// Delete.
	if err := s.DeleteRegisteredModel(ctx, "mymodel"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetRegisteredModel(ctx, "mymodel"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}
	// Double-delete.
	if err := s.DeleteRegisteredModel(ctx, "mymodel"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound on double delete, got %v", err)
	}
}

// ---------- Rename -----------------------------------------------------------

func TestRenameRegisteredModel(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "alpha")
	// Create a version under the model to test cascade.
	newModelVersion(t, s, "alpha", "s3://bucket/model")

	// Rename.
	m, err := s.RenameRegisteredModel(ctx, "alpha", "beta")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if m.Name != "beta" {
		t.Fatalf("want name=beta, got %s", m.Name)
	}

	// Old name gone.
	if _, err := s.GetRegisteredModel(ctx, "alpha"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old name should not exist after rename")
	}

	// Version should be under new name.
	res, err := s.SearchModelVersions(ctx, "name = 'beta'", 10, "")
	if err != nil {
		t.Fatalf("search after rename: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 version under beta, got %d", len(res.Items))
	}

	// Rename to existing name fails.
	newRegisteredModel(t, s, "gamma")
	if _, err := s.RenameRegisteredModel(ctx, "beta", "gamma"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}

	// Rename non-existent.
	if _, err := s.RenameRegisteredModel(ctx, "ghost", "delta"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// ---------- Version auto-increment -------------------------------------------

func TestVersionAutoIncrement(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "versioned")

	for i := 1; i <= 5; i++ {
		mv, err := s.CreateModelVersion(ctx, &model.ModelVersion{
			Name:   "versioned",
			Source: "s3://bucket/v" + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatalf("create version %d: %v", i, err)
		}
		if mv.Version != int64(i) {
			t.Fatalf("want version=%d, got %d", i, mv.Version)
		}
	}

	// First version is 1.
	mv1, err := s.GetModelVersion(ctx, "versioned", 1)
	if err != nil {
		t.Fatalf("get v1: %v", err)
	}
	if mv1.Version != 1 {
		t.Fatalf("want version=1, got %d", mv1.Version)
	}

	// Create for a different model starts at 1 again.
	newRegisteredModel(t, s, "another")
	mv, err := s.CreateModelVersion(ctx, &model.ModelVersion{Name: "another", Source: "s3://x"})
	if err != nil {
		t.Fatalf("create another v1: %v", err)
	}
	if mv.Version != 1 {
		t.Fatalf("want version=1 for different model, got %d", mv.Version)
	}
}

// ---------- Alias roundtrip --------------------------------------------------

func TestAliasRoundtrip(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "aliased")
	mv1 := newModelVersion(t, s, "aliased", "s3://b/v1")
	mv2 := newModelVersion(t, s, "aliased", "s3://b/v2")

	// Set alias on v1.
	if err := s.SetModelAlias(ctx, "aliased", "champion", mv1.Version); err != nil {
		t.Fatalf("set alias: %v", err)
	}

	// Resolve alias.
	resolved, err := s.GetModelByAlias(ctx, "aliased", "champion")
	if err != nil {
		t.Fatalf("get by alias: %v", err)
	}
	if resolved.Version != mv1.Version {
		t.Fatalf("want version=%d, got %d", mv1.Version, resolved.Version)
	}

	// Re-point alias to v2 (upsert).
	if err := s.SetModelAlias(ctx, "aliased", "champion", mv2.Version); err != nil {
		t.Fatalf("update alias: %v", err)
	}
	resolved2, err := s.GetModelByAlias(ctx, "aliased", "champion")
	if err != nil {
		t.Fatalf("get updated alias: %v", err)
	}
	if resolved2.Version != mv2.Version {
		t.Fatalf("want version=%d after update, got %d", mv2.Version, resolved2.Version)
	}

	// Delete alias.
	if err := s.DeleteModelAlias(ctx, "aliased", "champion"); err != nil {
		t.Fatalf("delete alias: %v", err)
	}
	if _, err := s.GetModelByAlias(ctx, "aliased", "champion"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}

	// Alias on non-existent version.
	if err := s.SetModelAlias(ctx, "aliased", "ghost", 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown version, got %v", err)
	}
}

// ---------- Latest versions per stage ----------------------------------------

func TestGetLatestModelVersionsPerStage(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "stages")
	v1 := newModelVersion(t, s, "stages", "s3://b/v1")
	v2 := newModelVersion(t, s, "stages", "s3://b/v2")
	v3 := newModelVersion(t, s, "stages", "s3://b/v3")
	v4 := newModelVersion(t, s, "stages", "s3://b/v4")

	// v1 -> Production, v2 -> Production, v3 -> Staging, v4 -> Production
	for _, pair := range []struct {
		ver   int64
		stage string
	}{
		{v1.Version, model.StageProduction},
		{v2.Version, model.StageProduction},
		{v3.Version, model.StageStaging},
		{v4.Version, model.StageProduction},
	} {
		if _, err := s.TransitionModelStage(ctx, "stages", pair.ver, pair.stage, false); err != nil {
			t.Fatalf("transition v%d -> %s: %v", pair.ver, pair.stage, err)
		}
	}

	// GetLatestModelVersions with no stage filter: one per stage.
	latest, err := s.GetLatestModelVersions(ctx, "stages", nil)
	if err != nil {
		t.Fatalf("get latest: %v", err)
	}
	byStage := map[string]int64{}
	for _, mv := range latest {
		byStage[mv.CurrentStage] = mv.Version
	}
	if byStage[model.StageProduction] != v4.Version {
		t.Fatalf("want latest production = v%d, got v%d", v4.Version, byStage[model.StageProduction])
	}
	if byStage[model.StageStaging] != v3.Version {
		t.Fatalf("want latest staging = v%d, got v%d", v3.Version, byStage[model.StageStaging])
	}

	// Filter to Production only.
	prod, err := s.GetLatestModelVersions(ctx, "stages", []string{model.StageProduction})
	if err != nil {
		t.Fatalf("get latest production: %v", err)
	}
	if len(prod) != 1 {
		t.Fatalf("want 1 production version, got %d", len(prod))
	}
	if prod[0].Version != v4.Version {
		t.Fatalf("want v%d, got v%d", v4.Version, prod[0].Version)
	}
}

// ---------- Transition with archive -----------------------------------------

func TestTransitionWithArchive(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "archive-test")
	v1 := newModelVersion(t, s, "archive-test", "s3://b/v1")
	v2 := newModelVersion(t, s, "archive-test", "s3://b/v2")
	v3 := newModelVersion(t, s, "archive-test", "s3://b/v3")

	// Put v1 and v2 into Production.
	if _, err := s.TransitionModelStage(ctx, "archive-test", v1.Version, model.StageProduction, false); err != nil {
		t.Fatalf("transition v1: %v", err)
	}
	if _, err := s.TransitionModelStage(ctx, "archive-test", v2.Version, model.StageProduction, false); err != nil {
		t.Fatalf("transition v2: %v", err)
	}

	// Transition v3 to Production WITH archive_existing_versions=true.
	if _, err := s.TransitionModelStage(ctx, "archive-test", v3.Version, model.StageProduction, true); err != nil {
		t.Fatalf("transition v3 with archive: %v", err)
	}

	// v1 and v2 should now be Archived; v3 should be Production.
	got1, _ := s.GetModelVersion(ctx, "archive-test", v1.Version)
	got2, _ := s.GetModelVersion(ctx, "archive-test", v2.Version)
	got3, _ := s.GetModelVersion(ctx, "archive-test", v3.Version)

	if got1.CurrentStage != model.StageArchived {
		t.Fatalf("v1: want Archived, got %s", got1.CurrentStage)
	}
	if got2.CurrentStage != model.StageArchived {
		t.Fatalf("v2: want Archived, got %s", got2.CurrentStage)
	}
	if got3.CurrentStage != model.StageProduction {
		t.Fatalf("v3: want Production, got %s", got3.CurrentStage)
	}
}

// ---------- Tag upsert -------------------------------------------------------

func TestTagUpsert(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "tagged")
	mv := newModelVersion(t, s, "tagged", "s3://b/v1")

	// Set model tag.
	if err := s.SetRegisteredModelTag(ctx, "tagged", "team", "mlops"); err != nil {
		t.Fatalf("set model tag: %v", err)
	}
	m, err := s.GetRegisteredModel(ctx, "tagged")
	if err != nil {
		t.Fatalf("get after tag: %v", err)
	}
	if len(m.Tags) != 1 || m.Tags[0].Key != "team" || m.Tags[0].Value != "mlops" {
		t.Fatalf("unexpected model tags: %v", m.Tags)
	}

	// Upsert (same key, different value).
	if err := s.SetRegisteredModelTag(ctx, "tagged", "team", "platform"); err != nil {
		t.Fatalf("upsert model tag: %v", err)
	}
	m2, _ := s.GetRegisteredModel(ctx, "tagged")
	if m2.Tags[0].Value != "platform" {
		t.Fatalf("want value=platform, got %s", m2.Tags[0].Value)
	}

	// Delete model tag.
	if err := s.DeleteRegisteredModelTag(ctx, "tagged", "team"); err != nil {
		t.Fatalf("delete model tag: %v", err)
	}
	m3, _ := s.GetRegisteredModel(ctx, "tagged")
	if len(m3.Tags) != 0 {
		t.Fatalf("expected no tags, got %v", m3.Tags)
	}

	// Version tags.
	if err := s.SetModelVersionTag(ctx, "tagged", mv.Version, "env", "prod"); err != nil {
		t.Fatalf("set version tag: %v", err)
	}
	got, err := s.GetModelVersion(ctx, "tagged", mv.Version)
	if err != nil {
		t.Fatalf("get version: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0].Value != "prod" {
		t.Fatalf("unexpected version tags: %v", got.Tags)
	}

	// Upsert version tag.
	if err := s.SetModelVersionTag(ctx, "tagged", mv.Version, "env", "staging"); err != nil {
		t.Fatalf("upsert version tag: %v", err)
	}
	got2, _ := s.GetModelVersion(ctx, "tagged", mv.Version)
	if got2.Tags[0].Value != "staging" {
		t.Fatalf("want value=staging, got %s", got2.Tags[0].Value)
	}

	// Delete version tag.
	if err := s.DeleteModelVersionTag(ctx, "tagged", mv.Version, "env"); err != nil {
		t.Fatalf("delete version tag: %v", err)
	}
	got3, _ := s.GetModelVersion(ctx, "tagged", mv.Version)
	if len(got3.Tags) != 0 {
		t.Fatalf("expected no version tags, got %v", got3.Tags)
	}
}

// ---------- Search -----------------------------------------------------------

func TestSearchRegisteredModels(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		newRegisteredModel(t, s, name)
	}

	// Empty filter returns all.
	res, err := s.SearchRegisteredModels(ctx, "", 100, "")
	if err != nil {
		t.Fatalf("search empty: %v", err)
	}
	if len(res.Items) < 3 {
		t.Fatalf("want >= 3, got %d", len(res.Items))
	}

	// name LIKE filter.
	res2, err := s.SearchRegisteredModels(ctx, "name LIKE 'a%'", 100, "")
	if err != nil {
		t.Fatalf("search LIKE: %v", err)
	}
	if len(res2.Items) != 1 || res2.Items[0].Name != "alpha" {
		t.Fatalf("unexpected results: %v", res2.Items)
	}

	// Tag filter.
	if err := s.SetRegisteredModelTag(ctx, "beta", "owner", "alice"); err != nil {
		t.Fatalf("set tag: %v", err)
	}
	res3, err := s.SearchRegisteredModels(ctx, "tags.owner = 'alice'", 100, "")
	if err != nil {
		t.Fatalf("search tag: %v", err)
	}
	if len(res3.Items) != 1 || res3.Items[0].Name != "beta" {
		t.Fatalf("unexpected tag search results: %v", res3.Items)
	}
}

func TestSearchModelVersions(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "mvsearch")
	v1 := newModelVersion(t, s, "mvsearch", "s3://b/v1")
	v2 := newModelVersion(t, s, "mvsearch", "s3://b/v2")
	_ = v2

	// Tag v1.
	if err := s.SetModelVersionTag(ctx, "mvsearch", v1.Version, "approved", "yes"); err != nil {
		t.Fatalf("set version tag: %v", err)
	}

	// Search by name.
	res, err := s.SearchModelVersions(ctx, "name = 'mvsearch'", 100, "")
	if err != nil {
		t.Fatalf("search by name: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("want 2 versions, got %d", len(res.Items))
	}

	// Search by tag.
	res2, err := s.SearchModelVersions(ctx, "tags.approved = 'yes'", 100, "")
	if err != nil {
		t.Fatalf("search by tag: %v", err)
	}
	if len(res2.Items) != 1 || res2.Items[0].Version != v1.Version {
		t.Fatalf("unexpected tag search results: %v", res2.Items)
	}
}

// ---------- Delete cascades --------------------------------------------------

func TestDeleteModelCascadesVersions(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "cascade")
	mv := newModelVersion(t, s, "cascade", "s3://b/v1")

	// Set an alias and tag so we test full cascade.
	_ = s.SetModelAlias(ctx, "cascade", "prod", mv.Version)
	_ = s.SetModelVersionTag(ctx, "cascade", mv.Version, "k", "v")

	// Delete the model; versions, aliases, tags should go with it.
	if err := s.DeleteRegisteredModel(ctx, "cascade"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Version should be gone.
	if _, err := s.GetModelVersion(ctx, "cascade", mv.Version); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("version should not exist after model delete, got %v", err)
	}
}

// ---------- Stage validation -------------------------------------------------

func TestInvalidStageRejected(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	newRegisteredModel(t, s, "invalid-stage")
	mv := newModelVersion(t, s, "invalid-stage", "s3://b/v1")

	if _, err := s.TransitionModelStage(ctx, "invalid-stage", mv.Version, "Banana", false); err == nil {
		t.Fatal("expected error for invalid stage, got nil")
	}
}

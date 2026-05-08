package migrations_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/gorevds/litemlflow/internal/migrations"

	_ "modernc.org/sqlite"
)

func TestLoadIsContiguous(t *testing.T) {
	t.Parallel()
	all, err := migrations.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("expected at least one migration")
	}
	for i, m := range all {
		if m.Version != i+1 {
			t.Fatalf("non-contiguous version at index %d: got %d", i, m.Version)
		}
		if m.Up == "" {
			t.Fatalf("migration %d %s has empty UP block", m.Version, m.Name)
		}
		if m.Down == "" {
			t.Fatalf("migration %d %s has empty DOWN block (every migration must be reversible)", m.Version, m.Name)
		}
	}
}

func TestApplyAndRollback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// First apply: should reach the latest version.
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	v1, _ := migrations.CurrentVersion(ctx, db)
	if v1 == 0 {
		t.Fatal("expected non-zero version after apply")
	}

	// Apply again: idempotent.
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply again: %v", err)
	}
	v2, _ := migrations.CurrentVersion(ctx, db)
	if v2 != v1 {
		t.Fatalf("version changed unexpectedly: %d -> %d", v1, v2)
	}

	// Rollback: down to v-1.
	if err := migrations.Rollback(ctx, db); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	v3, _ := migrations.CurrentVersion(ctx, db)
	if v3 != v1-1 {
		t.Fatalf("rollback should bring %d -> %d, got %d", v1, v1-1, v3)
	}

	// Re-apply.
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	v4, _ := migrations.CurrentVersion(ctx, db)
	if v4 != v1 {
		t.Fatalf("re-apply should restore version, got %d", v4)
	}
}

// Tests for the v2.1 rollback safety guard (independent-review C1).
//
// Rollback() must refuse to drop a non-empty table; RollbackForce() bypasses
// the check. The guard exists because DOWN scripts of recent migrations
// (e.g. 014_dataset_inputs_v2.sql) silently destroy production data when
// run against a busy DB without warning.
package migrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/gorevds/litemlflow/internal/migrations"

	_ "modernc.org/sqlite"
)

// TestRollbackRefusesNonEmpty: applies all migrations, inserts a row,
// calls Rollback() — expects the error message to mention the table.
func TestRollbackRefusesNonEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Seed enough rows for the latest migration's DOWN to actually drop.
	//
	// 015 is the latest migration; its DOWN drops the workspace-scoped
	// prompt_aliases table first (prompt_aliases has an FK to prompts), so we
	// seed a prompt row then an alias row — enough for the guard to fire and
	// name "prompt_aliases".
	if _, err := db.ExecContext(ctx, `
		INSERT INTO prompts(name, version, workspace_id, content, content_hash, created_at)
		VALUES ('p', 1, 'default', 'c', 'h', 0)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO prompt_aliases(workspace_id, name, alias, version)
		VALUES ('default', 'p', 'prod', 1)
	`); err != nil {
		t.Fatal(err)
	}

	// Plain Rollback must refuse.
	err = migrations.Rollback(ctx, db)
	if err == nil {
		t.Fatal("expected rollback to fail on non-empty prompt_aliases")
	}
	if !strings.Contains(err.Error(), "prompt_aliases") {
		t.Errorf("error should name the table; got %v", err)
	}
	if !strings.Contains(err.Error(), "force=true") &&
		!strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention force override; got %v", err)
	}

	// RollbackForce must succeed.
	if err := migrations.RollbackForce(ctx, db); err != nil {
		t.Fatalf("force rollback should succeed, got %v", err)
	}
}

// TestRollbackAllowsEmpty: a clean rollback on an empty table works.
func TestRollbackAllowsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := migrations.Rollback(ctx, db); err != nil {
		t.Errorf("rollback on empty DB should succeed, got %v", err)
	}
}

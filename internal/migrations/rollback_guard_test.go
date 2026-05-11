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
	// 014's DOWN drops dataset_inputs_v2. We need a parent (datasets_v2 row)
	// to satisfy the FK before inserting the link row.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO experiments(name, artifact_location, lifecycle_stage, workspace_id, creation_time, last_update_time)
		VALUES ('exp', '/tmp', 'active', 'default', 0, 0)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO runs(id, experiment_id, status, start_time, artifact_uri, lifecycle_stage, run_kind)
		VALUES ('r1', 1, 'FINISHED', 1, 'x', 'active', 'classic')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO datasets_v2(name, version, content_hash, size_bytes, workspace_id, created_at, lifecycle_stage)
		VALUES ('ds', 1, 'h', 0, 'default', 0, 'active')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO dataset_inputs_v2(run_id, dataset_id, tags_json) VALUES ('r1', 1, '[]')
	`); err != nil {
		t.Fatal(err)
	}

	// Plain Rollback must refuse.
	err = migrations.Rollback(ctx, db)
	if err == nil {
		t.Fatal("expected rollback to fail on non-empty dataset_inputs_v2")
	}
	if !strings.Contains(err.Error(), "dataset_inputs_v2") {
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

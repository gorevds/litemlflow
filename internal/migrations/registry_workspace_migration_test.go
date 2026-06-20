package migrations_test

// Regression test for migration 015_registry_workspace: pre-existing prompt,
// prompt-alias and model-registry rows must SURVIVE the upgrade and be assigned
// to the 'default' workspace. The danger this guards: prompt_aliases (and the
// registry tag/alias tables) have ON DELETE CASCADE foreign keys into their
// parent tables, so rebuilding a parent without first rebuilding/copying the
// child would cascade-delete the child rows during the migration.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gorevds/litemlflow/internal/migrations"

	_ "modernc.org/sqlite"
)

func TestMigration015PreservesRegistryAndPromptData(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Bring the schema up, then force-roll-back the latest migration (015) so
	// we are on the pre-015 (global) registry/prompts schema to seed legacy data.
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := migrations.RollbackForce(ctx, db); err != nil {
		t.Fatalf("rollback to pre-015: %v", err)
	}

	// Seed legacy (un-scoped) rows.
	seed := []string{
		`INSERT INTO prompts(name, version, content, content_hash, created_at) VALUES ('greet', 1, 'hi', 'h1', 0)`,
		`INSERT INTO prompt_aliases(name, alias, version) VALUES ('greet', 'prod', 1)`,
		`INSERT INTO registered_models(name, creation_time, last_update_time) VALUES ('clf', 0, 0)`,
		`INSERT INTO model_versions(name, version, source, creation_time, last_update_time) VALUES ('clf', 1, 's3://m', 0, 0)`,
		`INSERT INTO model_version_tags(name, version, key, value) VALUES ('clf', 1, 'stage', 'prod')`,
		`INSERT INTO model_aliases(name, alias, version) VALUES ('clf', 'champion', 1)`,
	}
	for _, q := range seed {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// Re-apply 015.
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("re-apply 015: %v", err)
	}

	// Every legacy row must survive and be assigned to the default workspace.
	checks := []struct {
		label string
		query string
	}{
		{"prompt", `SELECT count(*) FROM prompts WHERE name='greet' AND version=1 AND workspace_id='default'`},
		{"prompt_alias", `SELECT count(*) FROM prompt_aliases WHERE name='greet' AND alias='prod' AND workspace_id='default'`},
		{"registered_model", `SELECT count(*) FROM registered_models WHERE name='clf' AND workspace_id='default'`},
		{"model_version", `SELECT count(*) FROM model_versions WHERE name='clf' AND version=1 AND workspace_id='default'`},
		{"model_version_tag", `SELECT count(*) FROM model_version_tags WHERE name='clf' AND key='stage' AND workspace_id='default'`},
		{"model_alias", `SELECT count(*) FROM model_aliases WHERE name='clf' AND alias='champion' AND workspace_id='default'`},
	}
	for _, c := range checks {
		var n int
		if err := db.QueryRowContext(ctx, c.query).Scan(&n); err != nil {
			t.Fatalf("%s check query: %v", c.label, err)
		}
		if n != 1 {
			t.Errorf("%s: expected 1 surviving default-workspace row, got %d", c.label, n)
		}
	}

	// The FK graph must be intact after the rebuild.
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Errorf("foreign_key_check reported violations after migration 015")
	}
}

// TestMigration015DropsOrphanedRegistryRows guards a real production failure:
// the prod DB had 12 model_version_tags rows referencing model_versions that no
// longer existed (a pre-015 schema tolerated them). The 015 rebuild adds a FK
// from the tags table to model_versions, so copying the orphans verbatim hit
// "FOREIGN KEY constraint failed" and crash-looped the server on startup. The
// migration must instead drop orphan child rows and complete cleanly.
func TestMigration015DropsOrphanedRegistryRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Pin one connection so the foreign_keys toggle below is reliable.
	db.SetMaxOpenConns(1)

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := migrations.RollbackForce(ctx, db); err != nil {
		t.Fatalf("rollback to pre-015: %v", err)
	}

	// Seed a valid chain plus orphan child rows (parents missing). Orphans can
	// only be inserted with FK enforcement off — mirrors how the prod data got
	// into this state (rows that predate the FK / a non-cascading deletion).
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	seed := []string{
		// Valid chain — must survive.
		`INSERT INTO registered_models(name, creation_time, last_update_time) VALUES ('keep', 0, 0)`,
		`INSERT INTO model_versions(name, version, source, creation_time, last_update_time) VALUES ('keep', 1, 's', 0, 0)`,
		`INSERT INTO model_version_tags(name, version, key, value) VALUES ('keep', 1, 'stage', 'prod')`,
		`INSERT INTO prompts(name, version, content, content_hash, created_at) VALUES ('p', 1, 'c', 'h', 0)`,
		`INSERT INTO prompt_aliases(name, alias, version) VALUES ('p', 'prod', 1)`,
		// Orphans — no parent version/prompt. Must be DROPPED, not error.
		`INSERT INTO model_version_tags(name, version, key, value) VALUES ('ghost', 99, 'k', 'v')`,
		`INSERT INTO model_aliases(name, alias, version) VALUES ('ghost', 'a', 99)`,
		`INSERT INTO prompt_aliases(name, alias, version) VALUES ('ghostp', 'a', 7)`,
	}
	for _, q := range seed {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	// 015 must now COMPLETE (previously it failed with a FK violation).
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("re-apply 015 with orphan rows present: %v", err)
	}

	count := func(q string) int {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		return n
	}
	// Valid rows survive.
	if got := count(`SELECT count(*) FROM model_version_tags WHERE name='keep' AND workspace_id='default'`); got != 1 {
		t.Errorf("valid model_version_tag: want 1, got %d", got)
	}
	if got := count(`SELECT count(*) FROM prompt_aliases WHERE name='p' AND workspace_id='default'`); got != 1 {
		t.Errorf("valid prompt_alias: want 1, got %d", got)
	}
	// Orphans dropped.
	if got := count(`SELECT count(*) FROM model_version_tags WHERE name='ghost'`); got != 0 {
		t.Errorf("orphan model_version_tag should be dropped, got %d", got)
	}
	if got := count(`SELECT count(*) FROM model_aliases WHERE name='ghost'`); got != 0 {
		t.Errorf("orphan model_alias should be dropped, got %d", got)
	}
	if got := count(`SELECT count(*) FROM prompt_aliases WHERE name='ghostp'`); got != 0 {
		t.Errorf("orphan prompt_alias should be dropped, got %d", got)
	}
	// FK graph intact.
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reported violations after migration 015")
	}
}

package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/gorevds/litemlflow/internal/migrations"
)

// Regression: migrations ran with foreign_keys=ON, so table rebuilds
// (CREATE x_v2; copy; DROP x; RENAME) fired the implicit DELETE of DROP TABLE
// and cascaded into every child table. Upgrading a populated v3 database
// through 004 (which rebuilds `experiments`) deleted all runs and
// experiment tags.
func TestMigrationTableRebuildDoesNotCascadeDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // one in-memory database
	defer db.Close()

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for {
		v, err := migrations.CurrentVersion(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if v == 3 {
			break
		}
		if err := migrations.RollbackForce(ctx, db); err != nil {
			t.Fatalf("rollback from %d: %v", v, err)
		}
	}

	for _, q := range []string{
		`INSERT INTO experiments(id, name, artifact_location, creation_time, last_update_time) VALUES (1, 'e', 'x', 0, 0)`,
		`INSERT INTO experiment_tags(experiment_id, key, value) VALUES (1, 'k', 'v')`,
		`INSERT INTO runs(id, experiment_id, start_time, artifact_uri) VALUES ('r1', 1, 0, 'x')`,
		`INSERT INTO metrics(run_id, key, value, timestamp, step) VALUES ('r1', 'loss', 0.5, 1, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("re-apply: %v", err)
	}

	for table, want := range map[string]int{"experiments": 1, "experiment_tags": 1, "runs": 1, "metrics": 1} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s: %d rows after upgrade, want %d", table, n, want)
		}
	}
	var fk int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys left at %d on the pooled connection after migrating", fk)
	}
}

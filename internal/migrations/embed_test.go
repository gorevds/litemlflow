package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// TestSquashEquivalence (v2.1 T4.18) verifies that applying the
// hand-checked-in baseline file produces the same schema as applying
// 001..NN sequentially. Any drift fails CI so the squash candidate
// stays accurate; regenerate via `go run ./scripts/gen-baseline-schema`.
func TestSquashEquivalence(t *testing.T) {
	t.Parallel()

	// Schema A: apply migrations 001..NN in sequence.
	dbA, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	if err := migrations.Apply(context.Background(), dbA); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	schemaA, err := dumpSchema(dbA)
	if err != nil {
		t.Fatal(err)
	}

	// Schema B: apply the baseline file to a fresh DB.
	baseline, err := os.ReadFile("baseline/v2_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	dbB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	if _, err := dbB.ExecContext(context.Background(), string(baseline)); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	schemaB, err := dumpSchema(dbB)
	if err != nil {
		t.Fatal(err)
	}

	// Compare object-by-object so the test message points to the
	// specific drift rather than dumping the whole schema.
	if len(schemaA) != len(schemaB) {
		t.Fatalf("schema object count differs: migrations=%d baseline=%d\n"+
			"hint: regenerate baseline via `go run ./scripts/gen-baseline-schema > internal/migrations/baseline/v2_baseline.sql`",
			len(schemaA), len(schemaB))
	}
	for name, sqlA := range schemaA {
		sqlB, ok := schemaB[name]
		if !ok {
			t.Errorf("baseline missing %q (present in migrations)", name)
			continue
		}
		if normalizeSchemaSQL(sqlA) != normalizeSchemaSQL(sqlB) {
			t.Errorf("schema mismatch for %q:\n  migrations: %s\n  baseline:   %s",
				name, sqlA, sqlB)
		}
	}
	for name := range schemaB {
		if _, ok := schemaA[name]; !ok {
			t.Errorf("baseline has %q absent from migrations", name)
		}
	}
}

// dumpSchema returns a name → CREATE statement map for the live DB,
// excluding sqlite internal tables and per-connection temp probes.
func dumpSchema(db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(context.Background(), `
		SELECT name, sql FROM sqlite_schema
		WHERE sql IS NOT NULL
		  AND name NOT LIKE 'sqlite_%'
		  AND name NOT LIKE '__lmf_%'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, err
		}
		out[name] = ddl
	}
	return out, rows.Err()
}

// normalizeSchemaSQL collapses whitespace and the order of
// whitespace-separated column-list contents so semantically equivalent
// DDL compares equal.
func normalizeSchemaSQL(s string) string {
	// Replace runs of whitespace with single space, strip leading/trailing.
	fields := strings.Fields(s)
	// Within parenthesized lists, sort comma-separated items so column
	// ordering differences (which are NOT semantically meaningful when
	// applied to a fresh DB) don't trigger drift alerts. But cancel that:
	// column order IS semantically meaningful when CREATE TABLE binds
	// positional defaults. So just normalize whitespace.
	_ = sort.StringSlice(nil)
	return strings.Join(fields, " ")
}

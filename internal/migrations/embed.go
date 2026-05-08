// Package migrations contains the embedded SQL migration scripts and a runner.
//
// Each migration file is named NNN_description.sql and contains an UP block
// followed by an optional DOWN block separated by the literal line "-- DOWN".
// The runner records applied versions in the schema_migrations table.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed *.sql
var files embed.FS

// Migration is one parsed migration script.
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
}

// Load returns all migrations sorted ascending by version.
func Load() ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// Expect NNN_description.sql
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed migration name %q", e.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("migration %q: invalid version: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(files, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		up, down := splitUpDown(string(body))
		name := strings.TrimSuffix(parts[1], ".sql")
		out = append(out, Migration{
			Version: version,
			Name:    name,
			Up:      up,
			Down:    down,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	// Sanity: versions must be contiguous starting at 1.
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("non-contiguous migration versions: expected %d, got %d (%s)", i+1, m.Version, m.Name)
		}
	}
	return out, nil
}

// splitUpDown splits a migration body into UP and DOWN sections.
// The body is expected to contain "-- UP" optionally and "-- DOWN" once.
// Anything before "-- DOWN" (skipping any leading "-- UP" line) is UP;
// anything after is DOWN.
func splitUpDown(body string) (up, down string) {
	lines := strings.Split(body, "\n")
	var upLines, downLines []string
	mode := "up"
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		switch trimmed {
		case "-- UP":
			mode = "up"
			continue
		case "-- DOWN":
			mode = "down"
			continue
		}
		switch mode {
		case "up":
			upLines = append(upLines, l)
		case "down":
			downLines = append(downLines, l)
		}
	}
	return strings.TrimSpace(strings.Join(upLines, "\n")),
		strings.TrimSpace(strings.Join(downLines, "\n"))
}

// EnsureSchemaTable creates schema_migrations if missing.
func EnsureSchemaTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`)
	return err
}

// CurrentVersion returns the highest applied version, or 0 if none.
func CurrentVersion(ctx context.Context, db *sql.DB) (int, error) {
	if err := EnsureSchemaTable(ctx, db); err != nil {
		return 0, err
	}
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// Apply runs all pending UP migrations in order. Idempotent.
func Apply(ctx context.Context, db *sql.DB) error {
	if err := EnsureSchemaTable(ctx, db); err != nil {
		return err
	}
	all, err := Load()
	if err != nil {
		return err
	}
	cur, err := CurrentVersion(ctx, db)
	if err != nil {
		return err
	}

	// Detect future-schema scenario early.
	highest := 0
	for _, m := range all {
		if m.Version > highest {
			highest = m.Version
		}
	}
	if cur > highest {
		return fmt.Errorf("database is at version %d but binary only knows up to %d (downgrade not allowed)", cur, highest)
	}

	for _, m := range all {
		if m.Version <= cur {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return fmt.Errorf("apply migration %d %s: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	// SQLite does not support DDL inside arbitrary transactions for
	// every statement type, but our migrations are pure DDL/DML and we
	// want atomicity per migration. Use BEGIN IMMEDIATE.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.Up); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// Rollback runs the DOWN of the latest applied migration. Use for disaster
// recovery only; DOWN scripts are tested but not part of the normal flow.
func Rollback(ctx context.Context, db *sql.DB) error {
	all, err := Load()
	if err != nil {
		return err
	}
	cur, err := CurrentVersion(ctx, db)
	if err != nil {
		return err
	}
	if cur == 0 {
		return errors.New("nothing to rollback")
	}
	var target *Migration
	for i := range all {
		if all[i].Version == cur {
			target = &all[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("cannot find migration for version %d", cur)
	}
	if target.Down == "" {
		return fmt.Errorf("migration %d has no DOWN block", cur)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, target.Down); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, cur); err != nil {
		return err
	}
	return tx.Commit()
}

// Package migrations contains the embedded SQL migration scripts and a runner.
//
// Each migration file is named NNN_description.sql and contains an UP block
// followed by an optional DOWN block separated by the literal line "-- DOWN".
// The runner records applied versions in the schema_migrations table.
package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
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
	return runScript(ctx, db, m.Up, `INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, time.Now().UnixMilli())
}

// runScript executes one migration script plus its schema_migrations
// bookkeeping statement atomically, with foreign-key enforcement disabled.
//
// Several scripts rebuild a table (CREATE x_v2; copy; DROP x; RENAME). With
// foreign_keys=ON, DROP TABLE performs an implicit DELETE that fires the
// ON DELETE CASCADE actions of every child table, so e.g. 004 rebuilding
// `experiments` wiped all runs, metrics and experiment tags of an existing
// database. SQLite's documented procedure for schema changes is to turn FK
// enforcement off (outside a transaction — the PRAGMA is a no-op inside one),
// make the change, verify with foreign_key_check, then turn it back on.
func runScript(ctx context.Context, db *sql.DB, script, bookkeeping string, args ...any) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer func() {
		// Never hand a connection with FK enforcement off back to the pool.
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Only violations introduced by this script are fatal: a pre-existing
	// orphan must not make the binary refuse to start after an upgrade.
	before, err := countFKViolations(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	after, err := countFKViolations(ctx, tx)
	if err != nil {
		return err
	}
	if after > before {
		return fmt.Errorf("script would leave %d new foreign key violation(s)", after-before)
	}
	if _, err := tx.ExecContext(ctx, bookkeeping, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// countFKViolations returns the number of rows PRAGMA foreign_key_check
// reports, i.e. rows referencing a missing parent.
func countFKViolations(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	return n, nil
}

// Rollback runs the DOWN of the latest applied migration. Use for disaster
// recovery only; DOWN scripts are tested but not part of the normal flow.
//
// Safety: refuses to rollback a migration whose DOWN drops tables that
// currently hold rows the application uses, unless force is true.
// Without the guard an operator running rollback against a busy DB
// would silently destroy production data (v2.1 review C1).
func Rollback(ctx context.Context, db *sql.DB) error {
	return rollbackImpl(ctx, db, false)
}

// RollbackForce is the destructive-allowed variant. Used by the
// "litemlflow rollback --force" CLI path. Existing tests use this
// because they reset clean DBs.
func RollbackForce(ctx context.Context, db *sql.DB) error {
	return rollbackImpl(ctx, db, true)
}

func rollbackImpl(ctx context.Context, db *sql.DB, force bool) error {
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

	// Safety check: if the DOWN drops a table that has rows, refuse
	// unless the caller passed --force. Parse "DROP TABLE [IF EXISTS] <name>"
	// out of the DOWN script and count rows.
	if !force {
		dropped := extractDroppedTables(target.Down)
		for _, tbl := range dropped {
			var n int64
			row := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(tbl))
			if scanErr := row.Scan(&n); scanErr != nil {
				// Table doesn't exist (already rolled back) → not a hazard.
				continue
			}
			if n > 0 {
				return fmt.Errorf(
					"rollback aborted: DOWN of migration %d (%s) would drop table %q with %d rows. "+
						"Use Rollback(force=true) / `litemlflow rollback --force` to override. "+
						"v2.1 added this guard (v2.1-rc1 review C1)",
					cur, target.Name, tbl, n)
			}
		}
	}

	return runScript(ctx, db, target.Down, `DELETE FROM schema_migrations WHERE version = ?`, cur)
}

// extractDroppedTables returns the table names referenced by `DROP TABLE`
// statements in the DOWN script. Best-effort regex match: matches both
// `DROP TABLE name;` and `DROP TABLE IF EXISTS name;`, case-insensitively.
func extractDroppedTables(downSQL string) []string {
	var out []string
	for _, line := range strings.Split(downSQL, ";") {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(s), "DROP TABLE") {
			continue
		}
		// Strip the "DROP TABLE [IF EXISTS]" prefix.
		s = s[len("DROP TABLE"):]
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "IF EXISTS ")
		s = strings.TrimPrefix(s, "if exists ")
		s = strings.TrimSpace(s)
		// Drop a trailing comment or anything after the first whitespace.
		if i := strings.IndexAny(s, " \t\n("); i >= 0 {
			s = s[:i]
		}
		s = strings.Trim(s, `"`+"`'")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// quoteIdent wraps an identifier in double quotes and escapes any
// embedded double-quote with a doubled one. Used so the row-count
// safety check can't be tricked by a table name that contains weird
// characters (defense-in-depth — table names are operator-controlled).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

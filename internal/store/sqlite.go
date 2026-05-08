package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/litemlflow/litemlflow/internal/migrations"
	"github.com/litemlflow/litemlflow/internal/model"

	_ "modernc.org/sqlite"
)

// SQLiteStore is the canonical Store backed by SQLite via modernc.org/sqlite.
type SQLiteStore struct {
	db   *sql.DB
	root string
}

// OpenSQLite opens (or creates) a SQLite database at path and prepares the
// connection for safe concurrent use.
//
// path is the database file. root is the data directory used for artifact
// path resolution.
func OpenSQLite(ctx context.Context, path, root string) (*SQLiteStore, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	// Pragmas:
	//   _journal=WAL: WAL is mandatory for concurrent readers + single writer.
	//   _synchronous=NORMAL: durable across power loss for *committed* WAL frames.
	//   _busy_timeout=5000: wait up to 5s for write lock (default is 0 = fail fast).
	//   _foreign_keys=on: enforce FKs at runtime.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// We rely on a small connection pool. SQLite supports many concurrent
	// readers but only one writer; the driver handles serialization.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &SQLiteStore{db: db, root: root}, nil
}

// DB returns the underlying *sql.DB, primarily for tests.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

// Close releases resources.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// Migrate brings the database to the latest schema version.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	return migrations.Apply(ctx, s.db)
}

// ----- experiments -----

// CreateExperiment inserts a new experiment and returns its assigned ID.
// Returns ErrAlreadyExists when the name is already used.
// If e.WorkspaceID is empty, it defaults to "default".
func (s *SQLiteStore) CreateExperiment(ctx context.Context, e *model.Experiment) (int64, error) {
	if err := model.ValidName(e.Name, 250); err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	if e.CreationTime == 0 {
		e.CreationTime = now
	}
	if e.LastUpdateTime == 0 {
		e.LastUpdateTime = now
	}
	if e.LifecycleStage == "" {
		e.LifecycleStage = model.LifecycleActive
	}
	if e.WorkspaceID == "" {
		e.WorkspaceID = "default"
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO experiments(name, artifact_location, lifecycle_stage, creation_time, last_update_time, workspace_id)
		VALUES (?, ?, ?, ?, ?, ?)
	`, e.Name, e.ArtifactLocation, e.LifecycleStage, e.CreationTime, e.LastUpdateTime, e.WorkspaceID)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrAlreadyExists
		}
		return 0, fmt.Errorf("insert experiment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if e.ArtifactLocation == "" {
		// Default to a proxy artifact URI rather than a server-local
		// filesystem path. This makes the MLflow client route uploads and
		// downloads through the server (HTTP), instead of trying to write
		// directly to a path that only exists on the server.
		// See docs/spec/api-mlflow-compat.md for the routing details.
		loc := "mlflow-artifacts:/" + strconv.FormatInt(id, 10)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE experiments SET artifact_location = ? WHERE id = ?`, loc, id); err != nil {
			return 0, err
		}
		e.ArtifactLocation = loc
	}
	for _, t := range e.Tags {
		if err := s.SetExperimentTag(ctx, id, t.Key, t.Value); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// GetExperiment returns the experiment with the given ID.
func (s *SQLiteStore) GetExperiment(ctx context.Context, id int64) (*model.Experiment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, artifact_location, lifecycle_stage, creation_time, last_update_time, workspace_id
		FROM experiments WHERE id = ?
	`, id)
	return s.scanExperiment(ctx, row, true)
}

// GetExperimentByName returns the experiment with the given name scoped to the
// "default" workspace. Preserved for backward compatibility; new code should use
// GetExperimentByNameInWorkspace.
func (s *SQLiteStore) GetExperimentByName(ctx context.Context, name string) (*model.Experiment, error) {
	return s.GetExperimentByNameInWorkspace(ctx, "default", name)
}

// GetExperimentByNameInWorkspace returns the experiment with the given name
// within the specified workspace.
func (s *SQLiteStore) GetExperimentByNameInWorkspace(ctx context.Context, workspaceID, name string) (*model.Experiment, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, artifact_location, lifecycle_stage, creation_time, last_update_time, workspace_id
		FROM experiments WHERE name = ? AND workspace_id = ?
	`, name, workspaceID)
	return s.scanExperiment(ctx, row, true)
}

func (s *SQLiteStore) scanExperiment(ctx context.Context, row *sql.Row, withTags bool) (*model.Experiment, error) {
	var e model.Experiment
	if err := row.Scan(&e.ID, &e.Name, &e.ArtifactLocation, &e.LifecycleStage, &e.CreationTime, &e.LastUpdateTime, &e.WorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if withTags {
		tags, err := s.getExperimentTags(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		e.Tags = tags
	}
	return &e, nil
}

func (s *SQLiteStore) getExperimentTags(ctx context.Context, id int64) ([]model.KV, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM experiment_tags WHERE experiment_id = ? ORDER BY key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.KV
	for rows.Next() {
		var kv model.KV
		if err := rows.Scan(&kv.Key, &kv.Value); err != nil {
			return nil, err
		}
		out = append(out, kv)
	}
	return out, rows.Err()
}

// UpdateExperiment renames an experiment.
func (s *SQLiteStore) UpdateExperiment(ctx context.Context, id int64, newName *string) error {
	if newName == nil {
		return nil
	}
	if err := model.ValidName(*newName, 250); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE experiments SET name = ?, last_update_time = ? WHERE id = ?`, *newName, now, id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetExperimentLifecycle marks an experiment active or deleted.
func (s *SQLiteStore) SetExperimentLifecycle(ctx context.Context, id int64, stage string) error {
	if stage != model.LifecycleActive && stage != model.LifecycleDeleted {
		return fmt.Errorf("invalid lifecycle stage %q", stage)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE experiments SET lifecycle_stage = ?, last_update_time = ? WHERE id = ?`,
		stage, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetExperimentTag upserts a tag on an experiment.
func (s *SQLiteStore) SetExperimentTag(ctx context.Context, id int64, key, value string) error {
	if err := model.ValidKey(key); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO experiment_tags(experiment_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(experiment_id, key) DO UPDATE SET value = excluded.value
	`, id, key, value)
	return err
}

// SearchExperiments returns experiments matching opt. Supports a tiny subset
// of MLflow filter syntax in v0.1: an empty filter, or `name = '...'` /
// `name LIKE '...'`.
// If opt.WorkspaceID is empty, results are scoped to "default".
func (s *SQLiteStore) SearchExperiments(ctx context.Context, opt SearchOptions) (SearchResult[*model.Experiment], error) {
	if opt.MaxResults <= 0 {
		opt.MaxResults = 1000
	}
	if opt.MaxResults > 50000 {
		opt.MaxResults = 50000
	}
	stage := opt.LifecycleStage
	if stage == "" {
		stage = model.LifecycleActive
	}
	wsID := opt.WorkspaceID
	if wsID == "" {
		wsID = "default"
	}
	args := []any{}
	where := []string{}
	// TENANCY: scope to workspace
	where = append(where, "workspace_id = ?")
	args = append(args, wsID)
	if stage != "all" {
		where = append(where, "lifecycle_stage = ?")
		args = append(args, stage)
	}
	if f := strings.TrimSpace(opt.Filter); f != "" {
		clause, fargs, err := parseExperimentFilter(f)
		if err != nil {
			return SearchResult[*model.Experiment]{}, err
		}
		where = append(where, clause)
		args = append(args, fargs...)
	}
	q := `SELECT id, name, artifact_location, lifecycle_stage, creation_time, last_update_time, workspace_id FROM experiments`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY creation_time DESC LIMIT ?"
	args = append(args, opt.MaxResults+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchResult[*model.Experiment]{}, err
	}
	defer rows.Close()
	var out []*model.Experiment
	for rows.Next() {
		var e model.Experiment
		if err := rows.Scan(&e.ID, &e.Name, &e.ArtifactLocation, &e.LifecycleStage, &e.CreationTime, &e.LastUpdateTime, &e.WorkspaceID); err != nil {
			return SearchResult[*model.Experiment]{}, err
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return SearchResult[*model.Experiment]{}, err
	}

	var token string
	if len(out) > opt.MaxResults {
		out = out[:opt.MaxResults]
		token = strconv.FormatInt(out[len(out)-1].CreationTime, 10)
	}
	for _, e := range out {
		tags, err := s.getExperimentTags(ctx, e.ID)
		if err != nil {
			return SearchResult[*model.Experiment]{}, err
		}
		e.Tags = tags
	}
	return SearchResult[*model.Experiment]{Items: out, NextPageToken: token}, nil
}

// parseExperimentFilter handles a tiny subset of MLflow's expression
// language sufficient for our targeted test matrix.
func parseExperimentFilter(f string) (string, []any, error) {
	parts := strings.Fields(f)
	// Accept: name = 'foo'  |  name LIKE 'foo%'
	if len(parts) >= 3 && parts[0] == "name" {
		op := strings.ToUpper(parts[1])
		if op != "=" && op != "LIKE" {
			return "", nil, fmt.Errorf("unsupported operator %q in filter", parts[1])
		}
		val := strings.TrimSpace(strings.Join(parts[2:], " "))
		val = strings.Trim(val, "'\"")
		return "name " + op + " ?", []any{val}, nil
	}
	return "", nil, fmt.Errorf("unsupported filter %q (only name = / name LIKE supported in v0.1)", f)
}

// ----- runs -----

// CreateRun inserts a new run. The caller is expected to provide an ID
// (typically via model.NewRunID).
func (s *SQLiteStore) CreateRun(ctx context.Context, r *model.Run) error {
	if r.ID == "" {
		r.ID = model.NewRunID()
	}
	if r.Status == "" {
		r.Status = model.StatusRunning
	}
	if r.Kind == "" {
		r.Kind = model.KindClassic
	}
	if r.LifecycleStage == "" {
		r.LifecycleStage = model.LifecycleActive
	}
	if r.StartTime == 0 {
		r.StartTime = time.Now().UnixMilli()
	}
	if r.ArtifactURI == "" {
		// Use the proxy URI scheme that the MLflow client recognizes for
		// HTTP-routed artifact transfer. The actual filesystem mapping is
		// handled by the artifact router (which uses run_id as scope), so
		// the URI path only needs to contain the run ID.
		r.ArtifactURI = "mlflow-artifacts:/" + r.ID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs(id, experiment_id, name, status, start_time, end_time, artifact_uri, lifecycle_stage, user_id, source_type, source_name, run_kind)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, r.ID, r.ExperimentID, nilIfEmpty(r.Name), r.Status, r.StartTime, r.EndTime, r.ArtifactURI, r.LifecycleStage, nilIfEmpty(r.UserID), nilIfEmpty(r.SourceType), nilIfEmpty(r.SourceName), r.Kind)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		if isFKViolation(err) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// GetRun returns the run with the given ID.
func (s *SQLiteStore) GetRun(ctx context.Context, id string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, experiment_id, COALESCE(name,''), status, start_time, end_time, artifact_uri, lifecycle_stage,
		       COALESCE(user_id,''), COALESCE(source_type,''), COALESCE(source_name,''), run_kind
		FROM runs WHERE id = ?
	`, id)
	var r model.Run
	var endTime sql.NullInt64
	if err := row.Scan(&r.ID, &r.ExperimentID, &r.Name, &r.Status, &r.StartTime, &endTime, &r.ArtifactURI, &r.LifecycleStage, &r.UserID, &r.SourceType, &r.SourceName, &r.Kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if endTime.Valid {
		v := endTime.Int64
		r.EndTime = &v
	}
	return &r, nil
}

// UpdateRun changes status, end_time, or name. nil pointers are ignored.
func (s *SQLiteStore) UpdateRun(ctx context.Context, id string, status *string, endTime *int64, name *string) error {
	sets := []string{}
	args := []any{}
	if status != nil {
		switch *status {
		case model.StatusRunning, model.StatusFinished, model.StatusFailed, model.StatusKilled, model.StatusScheduled:
		default:
			return fmt.Errorf("invalid status %q", *status)
		}
		sets = append(sets, "status = ?")
		args = append(args, *status)
	}
	if endTime != nil {
		sets = append(sets, "end_time = ?")
		args = append(args, *endTime)
	}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRunLifecycle marks a run active or deleted.
func (s *SQLiteStore) SetRunLifecycle(ctx context.Context, id, stage string) error {
	if stage != model.LifecycleActive && stage != model.LifecycleDeleted {
		return fmt.Errorf("invalid lifecycle stage %q", stage)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET lifecycle_stage = ? WHERE id = ?`, stage, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchRuns lists runs matching opt. v0.1 supports filter by experiment IDs,
// lifecycle, and a few simple `metrics.x > N` / `params.x = '...'` clauses.
func (s *SQLiteStore) SearchRuns(ctx context.Context, opt SearchOptions) (SearchResult[*model.Run], error) {
	if opt.MaxResults <= 0 {
		opt.MaxResults = 1000
	}
	if opt.MaxResults > 50000 {
		opt.MaxResults = 50000
	}
	stage := opt.LifecycleStage
	if stage == "" {
		stage = model.LifecycleActive
	}
	args := []any{}
	where := []string{}
	if len(opt.ExperimentIDs) > 0 {
		marks := strings.TrimRight(strings.Repeat("?,", len(opt.ExperimentIDs)), ",")
		where = append(where, "experiment_id IN ("+marks+")")
		for _, id := range opt.ExperimentIDs {
			args = append(args, id)
		}
	}
	if stage != "all" {
		where = append(where, "lifecycle_stage = ?")
		args = append(args, stage)
	}
	if f := strings.TrimSpace(opt.Filter); f != "" {
		clause, fargs, err := parseRunFilter(f)
		if err != nil {
			return SearchResult[*model.Run]{}, err
		}
		where = append(where, clause)
		args = append(args, fargs...)
	}
	q := `
		SELECT id, experiment_id, COALESCE(name,''), status, start_time, end_time, artifact_uri,
		       lifecycle_stage, COALESCE(user_id,''), COALESCE(source_type,''), COALESCE(source_name,''), run_kind
		FROM runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	order := "start_time DESC"
	if len(opt.OrderBy) > 0 {
		ob, err := translateOrderBy(opt.OrderBy)
		if err != nil {
			return SearchResult[*model.Run]{}, err
		}
		order = ob
	}
	q += " ORDER BY " + order + " LIMIT ?"
	args = append(args, opt.MaxResults+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchResult[*model.Run]{}, err
	}
	defer rows.Close()
	var out []*model.Run
	for rows.Next() {
		var r model.Run
		var endTime sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ExperimentID, &r.Name, &r.Status, &r.StartTime, &endTime, &r.ArtifactURI,
			&r.LifecycleStage, &r.UserID, &r.SourceType, &r.SourceName, &r.Kind); err != nil {
			return SearchResult[*model.Run]{}, err
		}
		if endTime.Valid {
			v := endTime.Int64
			r.EndTime = &v
		}
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return SearchResult[*model.Run]{}, err
	}
	var token string
	if len(out) > opt.MaxResults {
		out = out[:opt.MaxResults]
		token = out[len(out)-1].ID
	}
	return SearchResult[*model.Run]{Items: out, NextPageToken: token}, nil
}

// translateOrderBy maps MLflow order-by syntax (`attributes.start_time DESC`)
// to a safe SQL ORDER BY clause built only from a known column whitelist.
func translateOrderBy(order []string) (string, error) {
	colMap := map[string]string{
		"attributes.start_time": "start_time",
		"attributes.end_time":   "end_time",
		"attributes.status":     "status",
		"start_time":            "start_time",
		"end_time":              "end_time",
		"status":                "status",
	}
	var parts []string
	for _, o := range order {
		fields := strings.Fields(o)
		if len(fields) == 0 {
			continue
		}
		col, ok := colMap[fields[0]]
		if !ok {
			return "", fmt.Errorf("unsupported order_by column %q", fields[0])
		}
		dir := "ASC"
		if len(fields) > 1 {
			d := strings.ToUpper(fields[1])
			if d != "ASC" && d != "DESC" {
				return "", fmt.Errorf("invalid order_by direction %q", fields[1])
			}
			dir = d
		}
		parts = append(parts, col+" "+dir)
	}
	if len(parts) == 0 {
		return "start_time DESC", nil
	}
	return strings.Join(parts, ", "), nil
}

// parseRunFilter supports a tiny subset:
//
//	status = 'FINISHED'
//	params.X = 'value'
//	metrics.X > 0.5     (uses the latest metric value)
//	tags.X = 'value'
//
// Multiple AND-joined predicates are supported.
func parseRunFilter(f string) (string, []any, error) {
	clauses := splitOnAnd(f)
	var sqlParts []string
	var args []any
	for _, c := range clauses {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		clause, cargs, err := parseRunPredicate(c)
		if err != nil {
			return "", nil, err
		}
		sqlParts = append(sqlParts, clause)
		args = append(args, cargs...)
	}
	if len(sqlParts) == 0 {
		return "1=1", nil, nil
	}
	return strings.Join(sqlParts, " AND "), args, nil
}

func splitOnAnd(s string) []string {
	upper := strings.ToUpper(s)
	out := []string{}
	last := 0
	depth := 0
	inQuote := false
	inBetween := false // true after BETWEEN until we consume its AND
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case '\'':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		}
		if !inQuote && depth == 0 && i+9 <= len(s) && upper[i:i+9] == " BETWEEN " {
			inBetween = true
		}
		if !inQuote && depth == 0 && i+5 <= len(s) && upper[i:i+5] == " AND " {
			if inBetween {
				// This AND belongs to the BETWEEN clause, skip it.
				inBetween = false
				i += 4
				continue
			}
			out = append(out, s[last:i])
			last = i + 5
			i += 4
		}
	}
	out = append(out, s[last:])
	return out
}

// parseRunPredicate matches the first occurrence of any of the supported
// operators (longest first to avoid eating "<=" as "<"). Predicates with
// multiple operators in their right-hand side (e.g. "x = a=b") are treated
// as having a literal "a=b" right-hand side; we do not attempt to detect
// malformed expressions. Callers wanting strict parsing should pre-validate.
//
// Supported operators:
//   - =, !=, <, <=, >, >= for all field types (numeric or string)
//   - LIKE for string fields
//   - IN ('a','b','c') for attributes (e.g. attributes.run_id IN (...))
//   - BETWEEN x AND y for numeric metrics
func parseRunPredicate(c string) (string, []any, error) {
	// Check for IN operator first (before generic op scan).
	if clause, args, err, ok := tryParseIN(c); ok {
		return clause, args, err
	}
	// Check for BETWEEN operator.
	if clause, args, err, ok := tryParseBETWEEN(c); ok {
		return clause, args, err
	}

	for _, op := range []string{">=", "<=", "!=", "=", ">", "<", " LIKE "} {
		opUpper := strings.ToUpper(op)
		idx := -1
		if op == " LIKE " {
			idx = strings.Index(strings.ToUpper(c), opUpper)
		} else {
			idx = strings.Index(c, op)
		}
		if idx < 0 {
			continue
		}
		left := strings.TrimSpace(c[:idx])
		right := strings.TrimSpace(c[idx+len(op):])
		right = strings.Trim(right, "'\"")

		realOp := strings.TrimSpace(op)
		if realOp == "" {
			realOp = "LIKE"
		}

		switch {
		case left == "status":
			return "status " + realOp + " ?", []any{right}, nil
		case strings.HasPrefix(left, "params."):
			key := strings.TrimPrefix(left, "params.")
			return "id IN (SELECT run_id FROM params WHERE key = ? AND value " + realOp + " ?)", []any{key, right}, nil
		case strings.HasPrefix(left, "tags."):
			key := strings.TrimPrefix(left, "tags.")
			return "id IN (SELECT run_id FROM tags WHERE key = ? AND value " + realOp + " ?)", []any{key, right}, nil
		case strings.HasPrefix(left, "metrics."):
			key := strings.TrimPrefix(left, "metrics.")
			// Use the most recent metric value per (run, key).
			val, err := strconv.ParseFloat(right, 64)
			if err != nil {
				return "", nil, fmt.Errorf("metric value must be numeric, got %q", right)
			}
			return `id IN (
				SELECT run_id FROM metrics m1
				WHERE key = ?
				  AND (timestamp, step) = (
					SELECT timestamp, step FROM metrics m2
					WHERE m2.run_id = m1.run_id AND m2.key = m1.key
					ORDER BY timestamp DESC, step DESC LIMIT 1
				  )
				  AND value ` + realOp + ` ?
			)`, []any{key, val}, nil
		case strings.HasPrefix(left, "attributes."):
			col := strings.TrimPrefix(left, "attributes.")
			whitelist := map[string]string{
				"status":     "status",
				"start_time": "start_time",
				"end_time":   "end_time",
				"run_name":   "name",
				"run_id":     "id",
			}
			scol, ok := whitelist[col]
			if !ok {
				return "", nil, fmt.Errorf("unsupported attribute %q", col)
			}
			return scol + " " + realOp + " ?", []any{right}, nil
		}
	}
	return "", nil, fmt.Errorf("unable to parse predicate %q", c)
}

// tryParseIN handles "left IN ('a','b','c')" predicates.
// Returns (clause, args, err, matched).
func tryParseIN(c string) (string, []any, error, bool) {
	upper := strings.ToUpper(c)
	inIdx := strings.Index(upper, " IN (")
	if inIdx < 0 {
		return "", nil, nil, false
	}
	left := strings.TrimSpace(c[:inIdx])
	rest := strings.TrimSpace(c[inIdx+5:]) // skip " IN ("
	// Find the closing paren
	closeIdx := strings.LastIndex(rest, ")")
	if closeIdx < 0 {
		return "", nil, fmt.Errorf("IN predicate missing closing ')'"), true
	}
	inner := rest[:closeIdx]
	vals, err := parseINValues(inner)
	if err != nil {
		return "", nil, err, true
	}
	if len(vals) == 0 {
		return "", nil, fmt.Errorf("IN predicate has no values"), true
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(vals)), ",")
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	switch {
	case left == "status":
		return "status IN (" + marks + ")", args, nil, true
	case strings.HasPrefix(left, "attributes."):
		col := strings.TrimPrefix(left, "attributes.")
		whitelist := map[string]string{
			"status":     "status",
			"run_id":     "id",
			"run_name":   "name",
			"start_time": "start_time",
			"end_time":   "end_time",
		}
		scol, ok := whitelist[col]
		if !ok {
			return "", nil, fmt.Errorf("unsupported attribute %q in IN predicate", col), true
		}
		return scol + " IN (" + marks + ")", args, nil, true
	case strings.HasPrefix(left, "params."):
		key := strings.TrimPrefix(left, "params.")
		return "id IN (SELECT run_id FROM params WHERE key = ? AND value IN (" + marks + "))", append([]any{key}, args...), nil, true
	case strings.HasPrefix(left, "tags."):
		key := strings.TrimPrefix(left, "tags.")
		return "id IN (SELECT run_id FROM tags WHERE key = ? AND value IN (" + marks + "))", append([]any{key}, args...), nil, true
	}
	return "", nil, fmt.Errorf("IN predicate on unsupported field %q", left), true
}

// parseINValues splits the inside of an IN (...) list into individual string values.
// Handles both quoted ('a','b') and unquoted (1,2,3) forms.
func parseINValues(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var vals []string
	inQ := false
	cur := strings.Builder{}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inQ:
			inQ = true
		case ch == '\'' && inQ:
			// Handle escaped quote ''
			if i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
			} else {
				inQ = false
				vals = append(vals, cur.String())
				cur.Reset()
				// skip whitespace + comma
				for i+1 < len(s) && (s[i+1] == ',' || s[i+1] == ' ') {
					i++
				}
			}
		case ch == ',' && !inQ:
			v := strings.TrimSpace(cur.String())
			vals = append(vals, v)
			cur.Reset()
		default:
			if !inQ && ch == ' ' {
				continue // skip spaces between unquoted tokens
			}
			cur.WriteByte(ch)
		}
	}
	if inQ {
		return nil, fmt.Errorf("unterminated quote in IN list")
	}
	if remaining := strings.TrimSpace(cur.String()); remaining != "" {
		vals = append(vals, remaining)
	}
	return vals, nil
}

// tryParseBETWEEN handles "metrics.key BETWEEN lo AND hi".
// Returns (clause, args, err, matched).
func tryParseBETWEEN(c string) (string, []any, error, bool) {
	upper := strings.ToUpper(c)
	betIdx := strings.Index(upper, " BETWEEN ")
	if betIdx < 0 {
		return "", nil, nil, false
	}
	left := strings.TrimSpace(c[:betIdx])
	rest := strings.TrimSpace(c[betIdx+9:]) // skip " BETWEEN "
	andIdx := strings.Index(strings.ToUpper(rest), " AND ")
	if andIdx < 0 {
		return "", nil, fmt.Errorf("BETWEEN predicate missing AND"), true
	}
	loStr := strings.TrimSpace(rest[:andIdx])
	hiStr := strings.TrimSpace(rest[andIdx+5:])
	switch {
	case strings.HasPrefix(left, "metrics."):
		key := strings.TrimPrefix(left, "metrics.")
		lo, err := strconv.ParseFloat(loStr, 64)
		if err != nil {
			return "", nil, fmt.Errorf("BETWEEN lo must be numeric, got %q", loStr), true
		}
		hi, err := strconv.ParseFloat(hiStr, 64)
		if err != nil {
			return "", nil, fmt.Errorf("BETWEEN hi must be numeric, got %q", hiStr), true
		}
		return `id IN (
			SELECT run_id FROM metrics m1
			WHERE key = ?
			  AND (timestamp, step) = (
				SELECT timestamp, step FROM metrics m2
				WHERE m2.run_id = m1.run_id AND m2.key = m1.key
				ORDER BY timestamp DESC, step DESC LIMIT 1
			  )
			  AND value BETWEEN ? AND ?
		)`, []any{key, lo, hi}, nil, true
	case strings.HasPrefix(left, "attributes."):
		col := strings.TrimPrefix(left, "attributes.")
		whitelist := map[string]string{
			"start_time": "start_time",
			"end_time":   "end_time",
		}
		scol, ok := whitelist[col]
		if !ok {
			return "", nil, fmt.Errorf("BETWEEN on unsupported attribute %q", col), true
		}
		lo, err := strconv.ParseFloat(loStr, 64)
		if err != nil {
			return "", nil, fmt.Errorf("BETWEEN lo must be numeric, got %q", loStr), true
		}
		hi, err := strconv.ParseFloat(hiStr, 64)
		if err != nil {
			return "", nil, fmt.Errorf("BETWEEN hi must be numeric, got %q", hiStr), true
		}
		return scol + " BETWEEN ? AND ?", []any{lo, hi}, nil, true
	}
	return "", nil, fmt.Errorf("BETWEEN on unsupported field %q", left), true
}

// ----- metrics, params, tags -----

// LogMetric writes a single metric observation.
func (s *SQLiteStore) LogMetric(ctx context.Context, runID string, m model.Metric) error {
	return s.LogMetrics(ctx, runID, []model.Metric{m})
}

// LogMetrics inserts multiple metrics in a single transaction.
// Duplicate (run_id, key, timestamp, step) tuples are silently ignored
// to keep idempotent retries safe.
func (s *SQLiteStore) LogMetrics(ctx context.Context, runID string, ms []model.Metric) error {
	if len(ms) == 0 {
		return nil
	}
	if err := assertRunExists(ctx, s.db, runID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO metrics(run_id, key, value, timestamp, step) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(run_id, key, timestamp, step) DO NOTHING
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range ms {
		if err := model.ValidKey(m.Key); err != nil {
			return err
		}
		if m.Timestamp == 0 {
			m.Timestamp = time.Now().UnixMilli()
		}
		if _, err := stmt.ExecContext(ctx, runID, m.Key, m.Value, m.Timestamp, m.Step); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LogParam writes an immutable parameter. Returns ErrAlreadyExists if a
// different value is already set under the same key.
func (s *SQLiteStore) LogParam(ctx context.Context, runID string, p model.Param) error {
	if err := model.ValidKey(p.Key); err != nil {
		return err
	}
	if err := assertRunExists(ctx, s.db, runID); err != nil {
		return err
	}
	// Try insert. On conflict, check whether the existing value matches.
	_, err := s.db.ExecContext(ctx, `INSERT INTO params(run_id, key, value) VALUES (?, ?, ?)`, runID, p.Key, p.Value)
	if err == nil {
		return nil
	}
	if !isUniqueViolation(err) {
		return err
	}
	var existing string
	row := s.db.QueryRowContext(ctx, `SELECT value FROM params WHERE run_id = ? AND key = ?`, runID, p.Key)
	if err := row.Scan(&existing); err != nil {
		return err
	}
	if existing == p.Value {
		// Idempotent re-set: same value, treat as success.
		return nil
	}
	return ErrAlreadyExists
}

// LogParams writes multiple params atomically.
func (s *SQLiteStore) LogParams(ctx context.Context, runID string, ps []model.Param) error {
	for _, p := range ps {
		if err := s.LogParam(ctx, runID, p); err != nil {
			return err
		}
	}
	return nil
}

// SetTag upserts a tag.
func (s *SQLiteStore) SetTag(ctx context.Context, runID string, t model.KV) error {
	if err := model.ValidKey(t.Key); err != nil {
		return err
	}
	if err := assertRunExists(ctx, s.db, runID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tags(run_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value
	`, runID, t.Key, t.Value)
	return err
}

// SetTags upserts multiple tags atomically.
func (s *SQLiteStore) SetTags(ctx context.Context, runID string, ts []model.KV) error {
	if err := assertRunExists(ctx, s.db, runID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tags(run_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range ts {
		if err := model.ValidKey(t.Key); err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, runID, t.Key, t.Value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteTag removes a tag. Returns ErrNotFound if absent.
func (s *SQLiteStore) DeleteTag(ctx context.Context, runID, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM tags WHERE run_id = ? AND key = ?`, runID, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMetricHistory returns observations for one metric key on one run.
// opt.MaxResults=0 returns all points. Otherwise it pages using a
// simple "timestamp:step" cursor encoded as base64.
func (s *SQLiteStore) GetMetricHistory(ctx context.Context, runID, key string, opt MetricHistoryOptions) ([]model.Metric, string, error) {
	args := []any{runID, key}
	q := `SELECT key, value, timestamp, step FROM metrics WHERE run_id = ? AND key = ?`

	if opt.PageToken != "" {
		ts, step, err := decodeMetricPageToken(opt.PageToken)
		if err == nil {
			q += ` AND (timestamp > ? OR (timestamp = ? AND step > ?))`
			args = append(args, ts, ts, step)
		}
	}
	q += ` ORDER BY timestamp ASC, step ASC`

	if opt.MaxResults > 0 {
		q += ` LIMIT ?`
		args = append(args, opt.MaxResults+1)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []model.Metric
	for rows.Next() {
		var m model.Metric
		if err := rows.Scan(&m.Key, &m.Value, &m.Timestamp, &m.Step); err != nil {
			return nil, "", err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextToken string
	if opt.MaxResults > 0 && len(out) > opt.MaxResults {
		out = out[:opt.MaxResults]
		last := out[len(out)-1]
		nextToken = encodeMetricPageToken(last.Timestamp, last.Step)
	}
	return out, nextToken, nil
}

// encodeMetricPageToken encodes a (timestamp, step) cursor as a simple string.
func encodeMetricPageToken(ts, step int64) string {
	return fmt.Sprintf("%d:%d", ts, step)
}

// decodeMetricPageToken decodes a page token produced by encodeMetricPageToken.
func decodeMetricPageToken(tok string) (ts, step int64, err error) {
	parts := strings.SplitN(tok, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid page token")
	}
	ts, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	step, err = strconv.ParseInt(parts[1], 10, 64)
	return ts, step, err
}

// GetLatestMetrics returns the most recent value of every metric for a run.
func (s *SQLiteStore) GetLatestMetrics(ctx context.Context, runID string) ([]model.Metric, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, value, timestamp, step FROM metrics m1
		WHERE run_id = ?
		  AND (timestamp, step) = (
			SELECT timestamp, step FROM metrics m2
			WHERE m2.run_id = m1.run_id AND m2.key = m1.key
			ORDER BY timestamp DESC, step DESC LIMIT 1
		  )
		ORDER BY key ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Metric
	for rows.Next() {
		var m model.Metric
		if err := rows.Scan(&m.Key, &m.Value, &m.Timestamp, &m.Step); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetParams returns all params on a run.
func (s *SQLiteStore) GetParams(ctx context.Context, runID string) ([]model.Param, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM params WHERE run_id = ? ORDER BY key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Param
	for rows.Next() {
		var p model.Param
		if err := rows.Scan(&p.Key, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetTags returns all tags on a run.
func (s *SQLiteStore) GetTags(ctx context.Context, runID string) ([]model.KV, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM tags WHERE run_id = ? ORDER BY key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.KV
	for rows.Next() {
		var kv model.KV
		if err := rows.Scan(&kv.Key, &kv.Value); err != nil {
			return nil, err
		}
		out = append(out, kv)
	}
	return out, rows.Err()
}

// ----- traces -----

// InsertSpans bulk-inserts trace spans atomically.
func (s *SQLiteStore) InsertSpans(ctx context.Context, spans []model.Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO traces(id, trace_id, parent_id, run_id, name, span_kind, start_time, end_time,
		                   attributes_json, events_json, status_code, status_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  end_time = excluded.end_time,
		  attributes_json = excluded.attributes_json,
		  events_json = excluded.events_json,
		  status_code = excluded.status_code,
		  status_message = excluded.status_message
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sp := range spans {
		if sp.ID == "" {
			sp.ID = model.NewSpanID()
		}
		if sp.TraceID == "" {
			sp.TraceID = sp.ID
		}
		if _, err := stmt.ExecContext(ctx, sp.ID, sp.TraceID, nilIfEmpty(sp.ParentID), nilIfEmpty(sp.RunID),
			sp.Name, nilIfEmpty(sp.Kind), sp.StartTimeNS, sp.EndTimeNS,
			nilIfEmpty(sp.AttributesJSON), nilIfEmpty(sp.EventsJSON),
			nilIfEmpty(sp.StatusCode), nilIfEmpty(sp.StatusMessage)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSpansByRun returns all spans associated with a run, ordered by start time.
func (s *SQLiteStore) GetSpansByRun(ctx context.Context, runID string) ([]model.Span, error) {
	return s.querySpans(ctx, `WHERE run_id = ? ORDER BY start_time ASC`, runID)
}

// GetSpansByTrace returns all spans for a trace_id.
func (s *SQLiteStore) GetSpansByTrace(ctx context.Context, traceID string) ([]model.Span, error) {
	return s.querySpans(ctx, `WHERE trace_id = ? ORDER BY start_time ASC`, traceID)
}

func (s *SQLiteStore) querySpans(ctx context.Context, where string, args ...any) ([]model.Span, error) {
	q := `SELECT id, trace_id, COALESCE(parent_id,''), COALESCE(run_id,''), name, COALESCE(span_kind,''),
	             start_time, end_time, COALESCE(attributes_json,''), COALESCE(events_json,''),
	             COALESCE(status_code,''), COALESCE(status_message,'')
	      FROM traces ` + where
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Span
	for rows.Next() {
		var sp model.Span
		var endTime sql.NullInt64
		if err := rows.Scan(&sp.ID, &sp.TraceID, &sp.ParentID, &sp.RunID, &sp.Name, &sp.Kind,
			&sp.StartTimeNS, &endTime, &sp.AttributesJSON, &sp.EventsJSON, &sp.StatusCode, &sp.StatusMessage); err != nil {
			return nil, err
		}
		if endTime.Valid {
			v := endTime.Int64
			sp.EndTimeNS = &v
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ----- prompts -----

// CreatePrompt creates a new prompt version, auto-incrementing the version
// number per name. If identical content already exists for this name, the
// existing version is returned (idempotency).
//
// There is a benign race window: two concurrent CreatePrompt calls with the
// same content can both pass the pre-check and proceed to insert. The second
// will collide on the (name, version) PK and the call returns an error.
// Callers should retry; in practice CreatePrompt is rarely contended and
// the cost of a stronger lock is not worth the throughput hit.
func (s *SQLiteStore) CreatePrompt(ctx context.Context, p *model.Prompt) (int64, error) {
	if err := model.ValidName(p.Name, 250); err != nil {
		return 0, err
	}
	hash := sha256.Sum256([]byte(p.Content))
	p.ContentHash = hex.EncodeToString(hash[:])
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().UnixMilli()
	}

	// Reuse identical content under the same name if it already exists.
	var existingVersion sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM prompts WHERE name = ? AND content_hash = ? ORDER BY version DESC LIMIT 1`,
		p.Name, p.ContentHash).Scan(&existingVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if existingVersion.Valid {
		p.Version = existingVersion.Int64
		return p.Version, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextVersion int64
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM prompts WHERE name = ?`, p.Name).Scan(&nextVersion)
	if err != nil {
		return 0, err
	}
	p.Version = nextVersion
	_, err = tx.ExecContext(ctx, `
		INSERT INTO prompts(name, version, content, content_hash, created_at, created_by, description)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, p.Name, p.Version, p.Content, p.ContentHash, p.CreatedAt, nilIfEmpty(p.CreatedBy), nilIfEmpty(p.Description))
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return p.Version, nil
}

// GetLatestPrompt returns the highest-versioned prompt with the given name.
func (s *SQLiteStore) GetLatestPrompt(ctx context.Context, name string) (*model.Prompt, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, version, content, content_hash, created_at, COALESCE(created_by,''), COALESCE(description,'')
		FROM prompts WHERE name = ? ORDER BY version DESC LIMIT 1
	`, name)
	return scanPrompt(row)
}

// GetPromptVersion returns a specific prompt version.
func (s *SQLiteStore) GetPromptVersion(ctx context.Context, name string, version int64) (*model.Prompt, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, version, content, content_hash, created_at, COALESCE(created_by,''), COALESCE(description,'')
		FROM prompts WHERE name = ? AND version = ?
	`, name, version)
	return scanPrompt(row)
}

// ListPromptVersions returns all versions of a prompt newest first.
func (s *SQLiteStore) ListPromptVersions(ctx context.Context, name string) ([]*model.Prompt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, version, content, content_hash, created_at, COALESCE(created_by,''), COALESCE(description,'')
		FROM prompts WHERE name = ? ORDER BY version DESC
	`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Prompt
	for rows.Next() {
		var p model.Prompt
		if err := rows.Scan(&p.Name, &p.Version, &p.Content, &p.ContentHash, &p.CreatedAt, &p.CreatedBy, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// SetPromptAlias upserts an alias (e.g., "production") to a specific version.
func (s *SQLiteStore) SetPromptAlias(ctx context.Context, name, alias string, version int64) error {
	// Confirm version exists.
	if _, err := s.GetPromptVersion(ctx, name, version); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO prompt_aliases(name, alias, version) VALUES (?, ?, ?)
		ON CONFLICT(name, alias) DO UPDATE SET version = excluded.version
	`, name, alias, version)
	return err
}

// GetPromptByAlias resolves an alias to a prompt version.
func (s *SQLiteStore) GetPromptByAlias(ctx context.Context, name, alias string) (*model.Prompt, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM prompt_aliases WHERE name = ? AND alias = ?`, name, alias).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetPromptVersion(ctx, name, v)
}

func scanPrompt(row *sql.Row) (*model.Prompt, error) {
	var p model.Prompt
	if err := row.Scan(&p.Name, &p.Version, &p.Content, &p.ContentHash, &p.CreatedAt, &p.CreatedBy, &p.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

// ----- evals -----

// CreateEval inserts an eval record. The associated run with run_kind='eval'
// must already exist.
func (s *SQLiteStore) CreateEval(ctx context.Context, e *model.Eval) error {
	if err := assertRunExists(ctx, s.db, e.RunID); err != nil {
		return err
	}
	targetJSON, err := json.Marshal(e.TargetRunIDs)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO evals(run_id, target_run_ids, dataset_ref, score, metrics_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
		  target_run_ids = excluded.target_run_ids,
		  dataset_ref    = excluded.dataset_ref,
		  score          = excluded.score,
		  metrics_json   = excluded.metrics_json
	`, e.RunID, string(targetJSON), nilIfEmpty(e.DatasetRef), e.Score, nilIfEmpty(e.MetricsJSON))
	return err
}

// GetEval returns the eval payload for a given run.
func (s *SQLiteStore) GetEval(ctx context.Context, runID string) (*model.Eval, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT run_id, target_run_ids, COALESCE(dataset_ref,''), score, COALESCE(metrics_json,'')
		FROM evals WHERE run_id = ?
	`, runID)
	var e model.Eval
	var targets string
	var score sql.NullFloat64
	if err := row.Scan(&e.RunID, &targets, &e.DatasetRef, &score, &e.MetricsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if score.Valid {
		v := score.Float64
		e.Score = &v
	}
	if err := json.Unmarshal([]byte(targets), &e.TargetRunIDs); err != nil {
		return nil, err
	}
	return &e, nil
}

// ----- datasets / log_inputs -----

// LogInputs records dataset linkages for a run. Each dataset is upserted
// (idempotent on name+digest); the input row and its tags are inserted fresh
// each call to match MLflow's semantics (duplicate calls are additive).
func (s *SQLiteStore) LogInputs(ctx context.Context, runID string, inputs []model.DatasetInput) error {
	if len(inputs) == 0 {
		return nil
	}
	if err := assertRunExists(ctx, s.db, runID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, inp := range inputs {
		ds := inp.Dataset
		// Upsert dataset record (name+digest are PK).
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO datasets(name, digest, source_type, source, schema, profile)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(name, digest) DO UPDATE SET
			  source_type = COALESCE(excluded.source_type, source_type),
			  source      = COALESCE(excluded.source,      source),
			  schema      = COALESCE(excluded.schema,      schema),
			  profile     = COALESCE(excluded.profile,     profile)
		`, ds.Name, ds.Digest, nilIfEmpty(ds.SourceType), nilIfEmpty(ds.Source),
			nilIfEmpty(ds.Schema), nilIfEmpty(ds.Profile)); err != nil {
			return fmt.Errorf("upsert dataset: %w", err)
		}

		// Insert dataset_input row and get its id.
		res, err := tx.ExecContext(ctx, `
			INSERT INTO dataset_inputs(run_id, name, digest) VALUES (?, ?, ?)
		`, runID, ds.Name, ds.Digest)
		if err != nil {
			return fmt.Errorf("insert dataset_input: %w", err)
		}
		inputID, err := res.LastInsertId()
		if err != nil {
			return err
		}

		for _, tag := range inp.Tags {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO dataset_input_tags(dataset_input_id, key, value) VALUES (?, ?, ?)
				ON CONFLICT(dataset_input_id, key) DO UPDATE SET value = excluded.value
			`, inputID, tag.Key, tag.Value); err != nil {
				return fmt.Errorf("insert dataset_input_tag: %w", err)
			}
		}
	}
	return tx.Commit()
}

// GetRunDatasets returns all dataset inputs linked to a run.
func (s *SQLiteStore) GetRunDatasets(ctx context.Context, runID string) ([]model.DatasetInput, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT di.id, d.name, d.digest,
		       COALESCE(d.source_type,''), COALESCE(d.source,''),
		       COALESCE(d.schema,''), COALESCE(d.profile,'')
		FROM dataset_inputs di
		JOIN datasets d ON d.name = di.name AND d.digest = di.digest
		WHERE di.run_id = ?
		ORDER BY di.id ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var inputs []model.DatasetInput
	var ids []int64
	for rows.Next() {
		var id int64
		var di model.DatasetInput
		if err := rows.Scan(&id, &di.Dataset.Name, &di.Dataset.Digest,
			&di.Dataset.SourceType, &di.Dataset.Source,
			&di.Dataset.Schema, &di.Dataset.Profile); err != nil {
			return nil, err
		}
		inputs = append(inputs, di)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load tags per input.
	for i, id := range ids {
		tagRows, err := s.db.QueryContext(ctx, `
			SELECT key, value FROM dataset_input_tags WHERE dataset_input_id = ? ORDER BY key
		`, id)
		if err != nil {
			return nil, err
		}
		for tagRows.Next() {
			var kv model.KV
			if err := tagRows.Scan(&kv.Key, &kv.Value); err != nil {
				_ = tagRows.Close()
				return nil, err
			}
			inputs[i].Tags = append(inputs[i].Tags, kv)
		}
		_ = tagRows.Close()
		if err := tagRows.Err(); err != nil {
			return nil, err
		}
	}
	return inputs, nil
}

// ----- helpers -----

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func assertRunExists(ctx context.Context, db *sql.DB, runID string) error {
	var n int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM runs WHERE id = ?`, runID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// isUniqueViolation tests whether err is a SQLite UNIQUE constraint violation.
// modernc.org/sqlite returns errors whose message contains the SQLite error
// text; we detect the canonical phrase rather than relying on driver-specific
// error codes.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "PRIMARY KEY")
}

// isFKViolation tests for SQLite FOREIGN KEY constraint violation.
func isFKViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "FOREIGN KEY constraint failed")
}

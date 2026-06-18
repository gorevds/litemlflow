package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// ---- Registered Models -------------------------------------------------------

// CreateRegisteredModel inserts a new registered model.
// Returns ErrAlreadyExists when the name is already taken.
func (s *SQLiteStore) CreateRegisteredModel(ctx context.Context, workspaceID string, m *model.RegisteredModel) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	m.WorkspaceID = workspaceID
	if err := model.ValidName(m.Name, 250); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if m.CreationTime == 0 {
		m.CreationTime = now
	}
	if m.LastUpdateTime == 0 {
		m.LastUpdateTime = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO registered_models(workspace_id, name, description, creation_time, last_update_time)
		VALUES (?, ?, ?, ?, ?)
	`, workspaceID, m.Name, nilIfEmpty(m.Description), m.CreationTime, m.LastUpdateTime)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("insert registered_model: %w", err)
	}
	return nil
}

// GetRegisteredModel returns the registered model with its tags.
func (s *SQLiteStore) GetRegisteredModel(ctx context.Context, workspaceID, name string) (*model.RegisteredModel, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT name, COALESCE(description,''), creation_time, last_update_time
		FROM registered_models WHERE workspace_id = ? AND name = ?
	`, workspaceID, name)
	m, err := scanRegisteredModel(row, workspaceID)
	if err != nil {
		return nil, err
	}
	tags, err := s.getRegisteredModelTags(ctx, workspaceID, name)
	if err != nil {
		return nil, err
	}
	m.Tags = tags
	return m, nil
}

func scanRegisteredModel(row *sql.Row, workspaceID string) (*model.RegisteredModel, error) {
	var m model.RegisteredModel
	if err := row.Scan(&m.Name, &m.Description, &m.CreationTime, &m.LastUpdateTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.WorkspaceID = workspaceID
	return &m, nil
}

func (s *SQLiteStore) getRegisteredModelTags(ctx context.Context, workspaceID, name string) ([]model.KV, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM registered_model_tags WHERE workspace_id = ? AND name = ? ORDER BY key`, workspaceID, name)
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

// RenameRegisteredModel renames a model and cascades to all child tables.
//
// SQLite does not propagate ON UPDATE CASCADE for PRIMARY KEY changes; we work
// around this by acquiring a dedicated connection, disabling FK enforcement on
// that connection (PRAGMA foreign_keys is per-connection), updating the PK
// and all FK columns, running a FK integrity check, and only then committing.
func (s *SQLiteStore) RenameRegisteredModel(ctx context.Context, workspaceID, name, newName string) (*model.RegisteredModel, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if err := model.ValidName(newName, 250); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()

	// Pin to a single connection so PRAGMA state is consistent.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Check source exists.
	var dummy string
	if err := conn.QueryRowContext(ctx, `SELECT name FROM registered_models WHERE workspace_id = ? AND name = ?`, workspaceID, name).Scan(&dummy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	// Check target name doesn't already exist.
	var existCheck string
	checkErr := conn.QueryRowContext(ctx, `SELECT name FROM registered_models WHERE workspace_id = ? AND name = ?`, workspaceID, newName).Scan(&existCheck)
	if checkErr == nil {
		return nil, ErrAlreadyExists
	} else if !errors.Is(checkErr, sql.ErrNoRows) {
		return nil, checkErr
	}

	// Disable FK enforcement on this connection BEFORE starting the transaction.
	// SQLite ignores PRAGMA foreign_keys changes inside a transaction.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return nil, fmt.Errorf("disable FK: %w", err)
	}
	// Ensure we always re-enable FK enforcement when we're done.
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Update child tables first (so they don't reference the old PK once it changes).
	for _, tbl := range []string{"model_versions", "registered_model_tags", "model_aliases"} {
		if _, err := tx.ExecContext(ctx, `UPDATE `+tbl+` SET name = ? WHERE workspace_id = ? AND name = ?`, newName, workspaceID, name); err != nil {
			return nil, fmt.Errorf("rename cascade %s: %w", tbl, err)
		}
	}

	// Now update the PK.
	if _, err := tx.ExecContext(ctx,
		`UPDATE registered_models SET name = ?, last_update_time = ? WHERE workspace_id = ? AND name = ?`, newName, now, workspaceID, name); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, fmt.Errorf("rename PK: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetRegisteredModel(ctx, workspaceID, newName)
}

// UpdateRegisteredModel updates the description of a registered model.
func (s *SQLiteStore) UpdateRegisteredModel(ctx context.Context, workspaceID, name string, description *string) (*model.RegisteredModel, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if description == nil {
		return s.GetRegisteredModel(ctx, workspaceID, name)
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE registered_models SET description = ?, last_update_time = ? WHERE workspace_id = ? AND name = ?`,
		nilIfEmpty(*description), now, workspaceID, name)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.GetRegisteredModel(ctx, workspaceID, name)
}

// DeleteRegisteredModel removes a model and all its versions (via FK CASCADE).
func (s *SQLiteStore) DeleteRegisteredModel(ctx context.Context, workspaceID, name string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM registered_models WHERE workspace_id = ? AND name = ?`, workspaceID, name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchRegisteredModels lists models matching a simple filter.
// Supported filters: empty, `name LIKE '...'`, `name = '...'`, `tags.X = '...'`.
func (s *SQLiteStore) SearchRegisteredModels(ctx context.Context, workspaceID, filter string, maxResults int, pageToken string) (SearchResult[*model.RegisteredModel], error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 10000 {
		maxResults = 10000
	}
	args := []any{workspaceID}
	where := []string{"workspace_id = ?"}
	if f := strings.TrimSpace(filter); f != "" {
		clause, fargs, err := parseRegistryFilter(workspaceID, f)
		if err != nil {
			return SearchResult[*model.RegisteredModel]{}, err
		}
		where = append(where, clause)
		args = append(args, fargs...)
	}
	if pageToken != "" {
		where = append(where, "name > ?")
		args = append(args, pageToken)
	}
	q := `SELECT name, COALESCE(description,''), creation_time, last_update_time FROM registered_models`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY name ASC LIMIT ?"
	args = append(args, maxResults+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchResult[*model.RegisteredModel]{}, err
	}
	defer rows.Close()
	var out []*model.RegisteredModel
	for rows.Next() {
		var m model.RegisteredModel
		if err := rows.Scan(&m.Name, &m.Description, &m.CreationTime, &m.LastUpdateTime); err != nil {
			return SearchResult[*model.RegisteredModel]{}, err
		}
		m.WorkspaceID = workspaceID
		out = append(out, &m)
	}
	if err := rows.Err(); err != nil {
		return SearchResult[*model.RegisteredModel]{}, err
	}
	var token string
	if len(out) > maxResults {
		out = out[:maxResults]
		token = out[len(out)-1].Name
	}
	for _, m := range out {
		tags, err := s.getRegisteredModelTags(ctx, workspaceID, m.Name)
		if err != nil {
			return SearchResult[*model.RegisteredModel]{}, err
		}
		m.Tags = tags
	}
	return SearchResult[*model.RegisteredModel]{Items: out, NextPageToken: token}, nil
}

// parseRegistryFilter handles:
//
//	name = 'foo'
//	name LIKE 'foo%'
//	tags.X = 'value'
//
// MLflow 3.x auto-appends `AND tag.\`mlflow.prompt.is_prompt\` != 'true'` to
// every search to exclude its "prompt" objects from the registry result. We
// don't store prompts as registered models, so that condition is always
// satisfied; strip it before parsing so we don't have to support AND + tag.X
// + != + backticks.
func parseRegistryFilter(workspaceID, f string) (string, []any, error) {
	f = stripPromptExclusion(f)
	// tags.X = 'value'
	if strings.HasPrefix(strings.ToLower(f), "tags.") {
		idx := strings.Index(f, "=")
		if idx < 0 {
			return "", nil, fmt.Errorf("unsupported registry filter %q", f)
		}
		key := strings.TrimSpace(f[5:idx])
		val := strings.TrimSpace(f[idx+1:])
		val = strings.Trim(val, "'\"")
		return `name IN (SELECT name FROM registered_model_tags WHERE workspace_id = ? AND key = ? AND value = ?)`, []any{workspaceID, key, val}, nil
	}
	parts := strings.Fields(f)
	if len(parts) >= 3 && strings.ToLower(parts[0]) == "name" {
		op := strings.ToUpper(parts[1])
		if op != "=" && op != "LIKE" {
			return "", nil, fmt.Errorf("unsupported operator %q in registry filter", parts[1])
		}
		val := strings.TrimSpace(strings.Join(parts[2:], " "))
		val = strings.Trim(val, "'\"")
		return "name " + op + " ?", []any{val}, nil
	}
	return "", nil, fmt.Errorf("unsupported registry filter %q (supports: name = / name LIKE / tags.X = '...')", f)
}

// stripPromptExclusion removes the "AND tag.`mlflow.prompt.is_prompt` != 'true'"
// clause that MLflow 3.x clients append to every registry search. Returns the
// remainder of the filter (or empty string if that was the only clause).
//
// We use a tolerant scan rather than a strict regex because MLflow may shift
// quoting (single vs double, with/without backticks). The pattern we look for:
// "AND" + something containing "mlflow.prompt.is_prompt" + comparison + value.
func stripPromptExclusion(f string) string {
	upper := strings.ToUpper(f)
	idx := strings.Index(upper, " AND ")
	if idx < 0 {
		// Possibly the entire filter is just the prompt-exclusion (when MLflow
		// is asked for "everything"). Handle that, too.
		if strings.Contains(strings.ToLower(f), "mlflow.prompt.is_prompt") {
			return ""
		}
		return f
	}
	tail := f[idx+5:]
	if strings.Contains(strings.ToLower(tail), "mlflow.prompt.is_prompt") {
		return strings.TrimSpace(f[:idx])
	}
	return f
}

// GetLatestModelVersions returns the highest-versioned model version per stage.
// If stages is empty, one version per stage (all stages) is returned.
func (s *SQLiteStore) GetLatestModelVersions(ctx context.Context, workspaceID, name string, stages []string) ([]*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	// Confirm model exists.
	if _, err := s.GetRegisteredModel(ctx, workspaceID, name); err != nil {
		return nil, err
	}

	// Build WHERE for stage filter.
	args := []any{workspaceID, name}
	stageWhere := ""
	if len(stages) > 0 {
		marks := strings.TrimRight(strings.Repeat("?,", len(stages)), ",")
		stageWhere = " AND current_stage IN (" + marks + ")"
		for _, st := range stages {
			args = append(args, st)
		}
	}

	// For each stage, select the max version.
	q := `
		SELECT name, version, COALESCE(description,''), COALESCE(user_id,''), current_stage,
		       source, COALESCE(run_id,''), status, COALESCE(status_message,''),
		       creation_time, last_update_time
		FROM model_versions
		WHERE workspace_id = ? AND name = ?` + stageWhere + `
		  AND version = (
			SELECT MAX(version) FROM model_versions mv2
			WHERE mv2.workspace_id = model_versions.workspace_id
			  AND mv2.name = model_versions.name
			  AND mv2.current_stage = model_versions.current_stage
		  )
		ORDER BY current_stage ASC
	`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ModelVersion
	for rows.Next() {
		mv, err := scanModelVersionRow(rows)
		if err != nil {
			return nil, err
		}
		mv.WorkspaceID = workspaceID
		out = append(out, mv)
	}
	return out, rows.Err()
}

// SetRegisteredModelTag upserts a tag on a registered model.
func (s *SQLiteStore) SetRegisteredModelTag(ctx context.Context, workspaceID, name, key, value string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if _, err := s.GetRegisteredModel(ctx, workspaceID, name); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO registered_model_tags(workspace_id, name, key, value) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, name, key) DO UPDATE SET value = excluded.value
	`, workspaceID, name, key, value)
	return err
}

// DeleteRegisteredModelTag removes a tag from a registered model.
func (s *SQLiteStore) DeleteRegisteredModelTag(ctx context.Context, workspaceID, name, key string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM registered_model_tags WHERE workspace_id = ? AND name = ? AND key = ?`, workspaceID, name, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Model Aliases -----------------------------------------------------------

// SetModelAlias upserts an alias on a model version.
func (s *SQLiteStore) SetModelAlias(ctx context.Context, workspaceID, name, alias string, version int64) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	// Confirm version exists.
	if _, err := s.GetModelVersion(ctx, workspaceID, name, version); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_aliases(workspace_id, name, alias, version) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, name, alias) DO UPDATE SET version = excluded.version
	`, workspaceID, name, alias, version)
	return err
}

// DeleteModelAlias removes an alias. Returns ErrNotFound if absent.
func (s *SQLiteStore) DeleteModelAlias(ctx context.Context, workspaceID, name, alias string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM model_aliases WHERE workspace_id = ? AND name = ? AND alias = ?`, workspaceID, name, alias)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetModelByAlias resolves an alias to a model version.
func (s *SQLiteStore) GetModelByAlias(ctx context.Context, workspaceID, name, alias string) (*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	var version int64
	err := s.db.QueryRowContext(ctx,
		`SELECT version FROM model_aliases WHERE workspace_id = ? AND name = ? AND alias = ?`, workspaceID, name, alias).Scan(&version)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetModelVersion(ctx, workspaceID, name, version)
}

// ---- Model Versions ----------------------------------------------------------

// CreateModelVersion inserts a new model version with an auto-incremented
// version number per model name. The first version is 1.
func (s *SQLiteStore) CreateModelVersion(ctx context.Context, workspaceID string, mv *model.ModelVersion) (*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	mv.WorkspaceID = workspaceID
	if err := model.ValidName(mv.Name, 250); err != nil {
		return nil, err
	}
	// Confirm model exists.
	if _, err := s.GetRegisteredModel(ctx, workspaceID, mv.Name); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if mv.CreationTime == 0 {
		mv.CreationTime = now
	}
	if mv.LastUpdateTime == 0 {
		mv.LastUpdateTime = now
	}
	if mv.CurrentStage == "" {
		mv.CurrentStage = model.StageNone
	}
	if mv.Status == "" {
		mv.Status = "READY"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Auto-increment version per name.
	var nextVersion int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM model_versions WHERE workspace_id = ? AND name = ?`,
		workspaceID, mv.Name).Scan(&nextVersion); err != nil {
		return nil, err
	}
	mv.Version = nextVersion

	_, err = tx.ExecContext(ctx, `
		INSERT INTO model_versions(workspace_id, name, version, description, user_id, current_stage, source,
		                           run_id, status, status_message, creation_time, last_update_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, workspaceID, mv.Name, mv.Version, nilIfEmpty(mv.Description), nilIfEmpty(mv.UserID),
		mv.CurrentStage, mv.Source, nilIfEmpty(mv.RunID), mv.Status,
		nilIfEmpty(mv.StatusMessage), mv.CreationTime, mv.LastUpdateTime)
	if err != nil {
		return nil, fmt.Errorf("insert model_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetModelVersion(ctx, workspaceID, mv.Name, mv.Version)
}

// GetModelVersion returns a specific model version with its tags.
func (s *SQLiteStore) GetModelVersion(ctx context.Context, workspaceID, name string, version int64) (*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT name, version, COALESCE(description,''), COALESCE(user_id,''), current_stage,
		       source, COALESCE(run_id,''), status, COALESCE(status_message,''),
		       creation_time, last_update_time
		FROM model_versions WHERE workspace_id = ? AND name = ? AND version = ?
	`, workspaceID, name, version)
	mv, err := scanModelVersionSingleRow(row)
	if err != nil {
		return nil, err
	}
	mv.WorkspaceID = workspaceID
	tags, err := s.getModelVersionTags(ctx, workspaceID, name, version)
	if err != nil {
		return nil, err
	}
	mv.Tags = tags
	return mv, nil
}

// UpdateModelVersion updates the description of a model version.
func (s *SQLiteStore) UpdateModelVersion(ctx context.Context, workspaceID, name string, version int64, description *string) (*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if description == nil {
		return s.GetModelVersion(ctx, workspaceID, name, version)
	}
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`UPDATE model_versions SET description = ?, last_update_time = ? WHERE workspace_id = ? AND name = ? AND version = ?`,
		nilIfEmpty(*description), now, workspaceID, name, version)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.GetModelVersion(ctx, workspaceID, name, version)
}

// DeleteModelVersion deletes a specific model version (and cascades aliases/tags).
func (s *SQLiteStore) DeleteModelVersion(ctx context.Context, workspaceID, name string, version int64) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM model_versions WHERE workspace_id = ? AND name = ? AND version = ?`, workspaceID, name, version)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchModelVersions lists versions matching a simple filter.
// Supported filters: empty, `name = '...'`, `tags.X = '...'`.
func (s *SQLiteStore) SearchModelVersions(ctx context.Context, workspaceID, filter string, maxResults int, pageToken string) (SearchResult[*model.ModelVersion], error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if maxResults <= 0 {
		maxResults = 100
	}
	if maxResults > 10000 {
		maxResults = 10000
	}
	args := []any{workspaceID}
	where := []string{"workspace_id = ?"}
	if f := strings.TrimSpace(filter); f != "" {
		clause, fargs, err := parseModelVersionFilter(workspaceID, f)
		if err != nil {
			return SearchResult[*model.ModelVersion]{}, err
		}
		where = append(where, clause)
		args = append(args, fargs...)
	}
	if pageToken != "" {
		// pageToken is "name:version" for model versions.
		parts := strings.SplitN(pageToken, ":", 2)
		if len(parts) == 2 {
			where = append(where, "(name > ? OR (name = ? AND version > ?))")
			args = append(args, parts[0], parts[0], parts[1])
		}
	}
	q := `SELECT name, version, COALESCE(description,''), COALESCE(user_id,''), current_stage,
	             source, COALESCE(run_id,''), status, COALESCE(status_message,''),
	             creation_time, last_update_time FROM model_versions`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY name ASC, version ASC LIMIT ?"
	args = append(args, maxResults+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchResult[*model.ModelVersion]{}, err
	}
	defer rows.Close()
	var out []*model.ModelVersion
	for rows.Next() {
		mv, err := scanModelVersionRow(rows)
		if err != nil {
			return SearchResult[*model.ModelVersion]{}, err
		}
		mv.WorkspaceID = workspaceID
		out = append(out, mv)
	}
	if err := rows.Err(); err != nil {
		return SearchResult[*model.ModelVersion]{}, err
	}
	var token string
	if len(out) > maxResults {
		out = out[:maxResults]
		last := out[len(out)-1]
		token = fmt.Sprintf("%s:%d", last.Name, last.Version)
	}
	for _, mv := range out {
		tags, err := s.getModelVersionTags(ctx, workspaceID, mv.Name, mv.Version)
		if err != nil {
			return SearchResult[*model.ModelVersion]{}, err
		}
		mv.Tags = tags
	}
	return SearchResult[*model.ModelVersion]{Items: out, NextPageToken: token}, nil
}

func parseModelVersionFilter(workspaceID, f string) (string, []any, error) {
	f = stripPromptExclusion(f)
	// tags.X = 'value'
	if strings.HasPrefix(strings.ToLower(f), "tags.") {
		idx := strings.Index(f, "=")
		if idx < 0 {
			return "", nil, fmt.Errorf("unsupported model version filter %q", f)
		}
		key := strings.TrimSpace(f[5:idx])
		val := strings.TrimSpace(f[idx+1:])
		val = strings.Trim(val, "'\"")
		return `(name, version) IN (SELECT name, version FROM model_version_tags WHERE workspace_id = ? AND key = ? AND value = ?)`, []any{workspaceID, key, val}, nil
	}
	// run_id = 'value'
	if strings.HasPrefix(strings.ToLower(f), "run_id") {
		parts := strings.Fields(f)
		if len(parts) >= 3 && strings.ToUpper(parts[1]) == "=" {
			val := strings.Trim(strings.Join(parts[2:], " "), "'\"")
			return "run_id = ?", []any{val}, nil
		}
	}
	parts := strings.Fields(f)
	if len(parts) >= 3 && strings.ToLower(parts[0]) == "name" {
		op := strings.ToUpper(parts[1])
		if op != "=" && op != "LIKE" {
			return "", nil, fmt.Errorf("unsupported operator %q in model version filter", parts[1])
		}
		val := strings.TrimSpace(strings.Join(parts[2:], " "))
		val = strings.Trim(val, "'\"")
		return "name " + op + " ?", []any{val}, nil
	}
	return "", nil, fmt.Errorf("unsupported model version filter %q (supports: name = / tags.X = / run_id = '...')", f)
}

// TransitionModelStage sets the stage of a model version.
// If archiveExisting is true, existing Production versions are moved to Archived.
func (s *SQLiteStore) TransitionModelStage(ctx context.Context, workspaceID, name string, version int64, stage string, archiveExisting bool) (*model.ModelVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if !model.ValidStage(stage) {
		return nil, fmt.Errorf("%w: %q must be one of None, Staging, Production, Archived", ErrInvalidStage, stage)
	}
	now := time.Now().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Archive existing versions in the target stage when requested.
	if archiveExisting && stage == model.StageProduction {
		_, err := tx.ExecContext(ctx, `
			UPDATE model_versions SET current_stage = 'Archived', last_update_time = ?
			WHERE workspace_id = ? AND name = ? AND current_stage = 'Production' AND version != ?
		`, now, workspaceID, name, version)
		if err != nil {
			return nil, fmt.Errorf("archive existing: %w", err)
		}
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE model_versions SET current_stage = ?, last_update_time = ?
		WHERE workspace_id = ? AND name = ? AND version = ?
	`, stage, now, workspaceID, name, version)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		_ = tx.Rollback()
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetModelVersion(ctx, workspaceID, name, version)
}

// SetModelVersionTag upserts a tag on a model version.
func (s *SQLiteStore) SetModelVersionTag(ctx context.Context, workspaceID, name string, version int64, key, value string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	if _, err := s.GetModelVersion(ctx, workspaceID, name, version); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_version_tags(workspace_id, name, version, key, value) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, name, version, key) DO UPDATE SET value = excluded.value
	`, workspaceID, name, version, key, value)
	return err
}

// DeleteModelVersionTag removes a tag from a model version.
func (s *SQLiteStore) DeleteModelVersionTag(ctx context.Context, workspaceID, name string, version int64, key string) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM model_version_tags WHERE workspace_id = ? AND name = ? AND version = ? AND key = ?`,
		workspaceID, name, version, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) getModelVersionTags(ctx context.Context, workspaceID, name string, version int64) ([]model.KV, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM model_version_tags WHERE workspace_id = ? AND name = ? AND version = ? ORDER BY key`,
		workspaceID, name, version)
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

// ---- scan helpers -----------------------------------------------------------

// scanModelVersionSingleRow scans a *sql.Row into a ModelVersion.
func scanModelVersionSingleRow(row *sql.Row) (*model.ModelVersion, error) {
	var mv model.ModelVersion
	if err := row.Scan(
		&mv.Name, &mv.Version, &mv.Description, &mv.UserID, &mv.CurrentStage,
		&mv.Source, &mv.RunID, &mv.Status, &mv.StatusMessage,
		&mv.CreationTime, &mv.LastUpdateTime,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &mv, nil
}

// scanModelVersionRow scans a *sql.Rows (multi-row cursor) into a ModelVersion.
func scanModelVersionRow(rows *sql.Rows) (*model.ModelVersion, error) {
	var mv model.ModelVersion
	if err := rows.Scan(
		&mv.Name, &mv.Version, &mv.Description, &mv.UserID, &mv.CurrentStage,
		&mv.Source, &mv.RunID, &mv.Status, &mv.StatusMessage,
		&mv.CreationTime, &mv.LastUpdateTime,
	); err != nil {
		return nil, err
	}
	return &mv, nil
}

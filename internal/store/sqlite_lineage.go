package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// ----- run lineage -----

// CreateRun is extended to persist parent_run_id when set.
// The base CreateRun in sqlite.go is replaced by this version which also
// handles parent_run_id and the mlflow.parentRunId tag mirror.
//
// NOTE: because SQLite.CreateRun in sqlite.go already handles the core insert
// we extend it here by providing CreateRunWithLineage which is called from
// the mlflow handler when parent_run_id is available. The existing CreateRun
// remains backward-compatible by delegating here.

// setParentRunID writes parent_run_id into the runs row for an existing run,
// and mirrors it as a tag `mlflow.parentRunId`. Called right after insert.
func (s *SQLiteStore) setParentRunID(ctx context.Context, runID, parentRunID string) error {
	if parentRunID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET parent_run_id = ? WHERE id = ?`, parentRunID, runID)
	if err != nil {
		return fmt.Errorf("set parent_run_id: %w", err)
	}
	// Mirror as tag for MLflow client compat.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tags(run_id, key, value) VALUES (?, 'mlflow.parentRunId', ?)
		ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value
	`, runID, parentRunID)
	return err
}

// syncParentRunIDFromTag reads mlflow.parentRunId tag and writes it to the column.
// Called after a set-tag on mlflow.parentRunId.
func (s *SQLiteStore) syncParentRunIDFromTag(ctx context.Context, runID, tagValue string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET parent_run_id = ? WHERE id = ?`, tagValue, runID)
	return err
}

// GetRunLineage returns the run itself, its ancestor chain, and its direct descendants.
func (s *SQLiteStore) GetRunLineage(ctx context.Context, runID string) (*RunLineage, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	// Walk ancestors upward (iterative to avoid unbounded recursion).
	var ancestors []*model.Run
	cur := run.ParentRunID
	for cur != "" {
		p, err := s.GetRun(ctx, cur)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				break
			}
			return nil, err
		}
		ancestors = append(ancestors, p)
		cur = p.ParentRunID
	}

	// Direct descendants (immediate children only; deep tree on demand).
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, experiment_id, COALESCE(name,''), status, start_time, end_time, artifact_uri,
		       lifecycle_stage, COALESCE(user_id,''), COALESCE(source_type,''), COALESCE(source_name,''), run_kind,
		       COALESCE(parent_run_id,'')
		FROM runs WHERE parent_run_id = ? AND lifecycle_stage = 'active'
		ORDER BY start_time ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var descendants []*model.Run
	for rows.Next() {
		var r model.Run
		var endTime sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ExperimentID, &r.Name, &r.Status, &r.StartTime, &endTime,
			&r.ArtifactURI, &r.LifecycleStage, &r.UserID, &r.SourceType, &r.SourceName, &r.Kind, &r.ParentRunID); err != nil {
			return nil, err
		}
		if endTime.Valid {
			v := endTime.Int64
			r.EndTime = &v
		}
		descendants = append(descendants, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &RunLineage{Run: run, Ancestors: ancestors, Descendants: descendants}, nil
}

// ----- janitor -----

// ArchiveStaleRuns transitions all RUNNING runs whose start_time is before
// staleBefore (unix ms) to FAILED, sets end_time to now, and inserts the
// lmf.auto_archived=stale tag. Returns the number of runs archived.
func (s *SQLiteStore) ArchiveStaleRuns(ctx context.Context, staleBefore int64) (int, error) {
	now := time.Now().UnixMilli()

	// Find the stale runs first so we can insert tags.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM runs
		WHERE status = 'RUNNING' AND start_time < ? AND lifecycle_stage = 'active'
	`, staleBefore)
	if err != nil {
		return 0, fmt.Errorf("query stale runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	tagStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tags(run_id, key, value) VALUES (?, 'lmf.auto_archived', 'stale')
		ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value
	`)
	if err != nil {
		return 0, err
	}
	defer tagStmt.Close()

	updateStmt, err := tx.PrepareContext(ctx, `
		UPDATE runs SET status = 'FAILED', end_time = ? WHERE id = ?
	`)
	if err != nil {
		return 0, err
	}
	defer updateStmt.Close()

	for _, id := range ids {
		if _, err := updateStmt.ExecContext(ctx, now, id); err != nil {
			return 0, fmt.Errorf("update run %s: %w", id, err)
		}
		if _, err := tagStmt.ExecContext(ctx, id); err != nil {
			return 0, fmt.Errorf("tag run %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// ----- webhooks -----

func (s *SQLiteStore) CreateWebhook(ctx context.Context, w *model.Webhook) (int64, error) {
	if w.CreatedAt == 0 {
		w.CreatedAt = time.Now().UnixMilli()
	}
	if w.WorkspaceID == "" {
		w.WorkspaceID = "default"
	}
	enabledInt := 0
	if w.Enabled {
		enabledInt = 1
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO webhooks(name, url, events, experiment_id, workspace_id, secret, created_at, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, w.Name, w.URL, w.Events, w.ExperimentID, w.WorkspaceID, nilIfEmpty(w.Secret), w.CreatedAt, enabledInt)
	if err != nil {
		return 0, fmt.Errorf("create webhook: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) ListWebhooks(ctx context.Context, workspaceID string, expID *int64) ([]*model.Webhook, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	args := []any{workspaceID}
	q := `SELECT id, name, url, events, experiment_id, workspace_id, COALESCE(secret,''),
	             created_at, last_status, last_attempt, enabled
	      FROM webhooks WHERE workspace_id = ?`
	if expID != nil {
		q += ` AND (experiment_id IS NULL OR experiment_id = ?)`
		args = append(args, *expID)
	}
	q += ` ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWebhooks(rows)
}

func (s *SQLiteStore) GetWebhook(ctx context.Context, id int64) (*model.Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, url, events, experiment_id, workspace_id, COALESCE(secret,''),
		       created_at, last_status, last_attempt, enabled
		FROM webhooks WHERE id = ?
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	wh, err := scanWebhooks(rows)
	if err != nil {
		return nil, err
	}
	if len(wh) == 0 {
		return nil, ErrNotFound
	}
	return wh[0], nil
}

func scanWebhooks(rows *sql.Rows) ([]*model.Webhook, error) {
	var out []*model.Webhook
	for rows.Next() {
		var w model.Webhook
		var expID sql.NullInt64
		var lastStatus sql.NullInt64
		var lastAttempt sql.NullInt64
		var enabledInt int
		if err := rows.Scan(&w.ID, &w.Name, &w.URL, &w.Events, &expID, &w.WorkspaceID, &w.Secret,
			&w.CreatedAt, &lastStatus, &lastAttempt, &enabledInt); err != nil {
			return nil, err
		}
		if expID.Valid {
			v := expID.Int64
			w.ExperimentID = &v
		}
		if lastStatus.Valid {
			v := int(lastStatus.Int64)
			w.LastStatus = &v
		}
		if lastAttempt.Valid {
			v := lastAttempt.Int64
			w.LastAttempt = &v
		}
		w.Enabled = enabledInt != 0
		out = append(out, &w)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateWebhook(ctx context.Context, w *model.Webhook) error {
	enabledInt := 0
	if w.Enabled {
		enabledInt = 1
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET name = ?, url = ?, events = ?, experiment_id = ?,
		       workspace_id = ?, secret = ?, enabled = ?
		WHERE id = ?
	`, w.Name, w.URL, w.Events, w.ExperimentID, w.WorkspaceID, nilIfEmpty(w.Secret), enabledInt, w.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) DeleteWebhook(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) RecordWebhookAttempt(ctx context.Context, id int64, status int, attempt int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webhooks SET last_status = ?, last_attempt = ? WHERE id = ?
	`, status, attempt, id)
	return err
}

// ----- experiment clone -----

// CloneExperiment creates a new experiment with newName in workspaceID,
// copying all tags from srcID. Returns the new experiment.
func (s *SQLiteStore) CloneExperiment(ctx context.Context, srcID int64, newName, workspaceID string) (*model.Experiment, error) {
	src, err := s.GetExperiment(ctx, srcID)
	if err != nil {
		return nil, err
	}
	if workspaceID == "" {
		workspaceID = src.WorkspaceID
	}

	newExp := &model.Experiment{
		Name:        newName,
		Tags:        src.Tags,
		WorkspaceID: workspaceID,
	}
	id, err := s.CreateExperiment(ctx, newExp)
	if err != nil {
		return nil, err
	}
	return s.GetExperiment(ctx, id)
}

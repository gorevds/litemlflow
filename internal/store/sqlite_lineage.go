package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
//
// v1.5: emits both run_parent and tag_set events so replay can undo each
// side. The tag is inserted via raw SQL (not SetTag) to avoid the
// SetTag→syncParentRunIDFromTag→setParentRunID cycle that would
// double-write events.
func (s *SQLiteStore) setParentRunID(ctx context.Context, runID, parentRunID string) error {
	if parentRunID == "" {
		return nil
	}
	before := s.captureRunBefore(ctx, runID)
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET parent_run_id = ? WHERE id = ?`, parentRunID, runID)
	if err != nil {
		return fmt.Errorf("set parent_run_id: %w", err)
	}
	if before != nil {
		s.tryWriteRunEvent(ctx, EventRunParent, runID, map[string]any{"before": before})
	}
	// Mirror as tag for MLflow client compat. Capture pre-tag value so
	// replay can correctly undo the upsert.
	tagBefore, hadTag := s.readTagValue(ctx, runID, "mlflow.parentRunId")
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tags(run_id, key, value) VALUES (?, 'mlflow.parentRunId', ?)
		ON CONFLICT(run_id, key) DO UPDATE SET value = excluded.value
	`, runID, parentRunID)
	if err != nil {
		return err
	}
	tagPayload := map[string]any{"key": "mlflow.parentRunId", "value": parentRunID}
	if hadTag {
		tagPayload["before"] = tagBefore
	}
	s.tryWriteRunEvent(ctx, EventTagSet, runID, tagPayload)
	return nil
}

// syncParentRunIDFromTag reads mlflow.parentRunId tag and writes it to the column.
// Called after a set-tag on mlflow.parentRunId.
func (s *SQLiteStore) syncParentRunIDFromTag(ctx context.Context, runID, tagValue string) error {
	before := s.captureRunBefore(ctx, runID)
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET parent_run_id = ? WHERE id = ?`, tagValue, runID)
	if err == nil && before != nil {
		s.tryWriteRunEvent(ctx, EventRunParent, runID, map[string]any{"before": before})
	}
	return err
}

// GetRunLineage is the v1.0 entry point — both directions, immediate
// children only. Equivalent to GetRunLineageWithOptions with Direction=both,
// DescendantDepth=1.
func (s *SQLiteStore) GetRunLineage(ctx context.Context, runID string) (*RunLineage, error) {
	return s.GetRunLineageWithOptions(ctx, runID, LineageOptions{
		Direction:       LineageBoth,
		DescendantDepth: 1,
	})
}

// GetRunLineageWithOptions is the v1.4 extended walk:
//
//   - Direction=upstream  → Ancestors filled, Descendants empty
//   - Direction=downstream → Descendants filled (BFS to opt.DescendantDepth)
//   - Direction=both       → both
//
// Datasets is always populated when the run has logged inputs.
//
// Workspace isolation: every walk and join is constrained to the
// experiment's workspace_id. parent_run_id is a user-settable tag
// (mlflow.parentRunId), so without this filter an editor in workspace B
// could set parent_run_id pointing into workspace A and exfiltrate
// run names/timing/users via lineage queries. See independent-review
// findings #1 and #2 for v1.4-rc1.
func (s *SQLiteStore) GetRunLineageWithOptions(ctx context.Context, runID string, opt LineageOptions) (*RunLineage, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	exp, err := s.GetExperiment(ctx, run.ExperimentID)
	if err != nil {
		return nil, fmt.Errorf("lineage workspace lookup: %w", err)
	}
	workspaceID := exp.WorkspaceID
	if workspaceID == "" {
		workspaceID = "default"
	}

	if opt.Direction == "" {
		opt.Direction = LineageBoth
	}
	depth := opt.DescendantDepth
	if depth <= 0 {
		depth = 4
	}
	if depth > 8 {
		depth = 8
	}
	fanOut := opt.MaxNodesPerLevel
	if fanOut <= 0 {
		fanOut = 50
	}
	if fanOut > 200 {
		fanOut = 200
	}

	// Initialize empty (non-nil) slices so the JSON wire shape is always
	// {"ancestors":[], "descendants":[], "datasets":[]} — not null. Old
	// SDKs unmarshalling into typed slices choke on null.
	out := &RunLineage{
		Run:         run,
		Ancestors:   []*model.Run{},
		Descendants: []*model.Run{},
		Datasets:    []DatasetEdge{},
	}

	// ----- ancestors (upstream) -----
	if opt.Direction == LineageUpstream || opt.Direction == LineageBoth {
		out.Ancestors = s.walkAncestors(ctx, run, runID, workspaceID)
	}

	// ----- descendants (downstream BFS) -----
	if opt.Direction == LineageDownstream || opt.Direction == LineageBoth {
		desc, truncated, err := s.walkDescendants(ctx, runID, workspaceID, depth, fanOut)
		if err != nil {
			return nil, err
		}
		if desc == nil {
			desc = []*model.Run{}
		}
		out.Descendants = desc
		out.Truncated = truncated
	}

	// ----- run → dataset edges (always) -----
	edges, err := s.runDatasetEdges(ctx, runID, workspaceID)
	if err != nil {
		return nil, err
	}
	if edges != nil {
		out.Datasets = edges
	}

	return out, nil
}

// walkAncestors traces parent_run_id upward with cycle + depth defenses,
// constrained to runs in workspaceID (so a tag-injected cross-workspace
// parent_run_id stops the walk instead of leaking).
//
// Transient errors from getRunInWorkspace are logged and break the walk;
// returning a partial chain is preferable to a 500 for a read-only view,
// but operators get an audit trail via slog.
func (s *SQLiteStore) walkAncestors(ctx context.Context, run *model.Run, selfID, workspaceID string) []*model.Run {
	const maxAncestorDepth = 256
	visited := make(map[string]struct{}, maxAncestorDepth)
	visited[selfID] = struct{}{}
	ancestors := []*model.Run{}
	cur := run.ParentRunID
	for cur != "" {
		if _, seen := visited[cur]; seen {
			break
		}
		if len(ancestors) >= maxAncestorDepth {
			break
		}
		visited[cur] = struct{}{}
		p, err := s.getRunInWorkspace(ctx, cur, workspaceID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// Either the run does not exist or it's in a different
				// workspace. Treat both identically — do not leak which.
				break
			}
			slog.Warn("lineage ancestor walk aborted on transient error",
				"run_id", selfID, "next_id", cur, "depth_so_far", len(ancestors), "err", err.Error())
			break
		}
		ancestors = append(ancestors, p)
		cur = p.ParentRunID
	}
	return ancestors
}

// getRunInWorkspace returns ErrNotFound if the run is missing OR belongs
// to a different workspace. Constant-shape error means callers cannot
// distinguish the two cases, which is intentional for cross-workspace
// information leakage prevention.
func (s *SQLiteStore) getRunInWorkspace(ctx context.Context, runID, workspaceID string) (*model.Run, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT r.id, r.experiment_id, COALESCE(r.name,''), r.status,
		       r.start_time, r.end_time, r.artifact_uri, r.lifecycle_stage,
		       COALESCE(r.user_id,''), COALESCE(r.source_type,''),
		       COALESCE(r.source_name,''), r.run_kind,
		       COALESCE(r.parent_run_id,'')
		FROM runs r
		JOIN experiments e ON e.id = r.experiment_id
		WHERE r.id = ? AND e.workspace_id = ?
	`, runID, workspaceID)
	var r model.Run
	var endTime sql.NullInt64
	if err := row.Scan(&r.ID, &r.ExperimentID, &r.Name, &r.Status,
		&r.StartTime, &endTime, &r.ArtifactURI, &r.LifecycleStage,
		&r.UserID, &r.SourceType, &r.SourceName, &r.Kind, &r.ParentRunID); err != nil {
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

// walkDescendants does a BFS starting from rootID, capped at maxDepth
// levels and maxFanOut children per level. Children outside workspaceID
// are excluded so a tag-injected parent_run_id from another workspace
// cannot surface here.
func (s *SQLiteStore) walkDescendants(ctx context.Context, rootID, workspaceID string, maxDepth, maxFanOut int) ([]*model.Run, bool, error) {
	visited := map[string]struct{}{rootID: {}}
	out := []*model.Run{}
	frontier := []string{rootID}
	truncated := false
	for level := 0; level < maxDepth && len(frontier) > 0; level++ {
		// Single query per level: WHERE parent_run_id IN (...).
		// Keeps round-trips down to O(depth) instead of O(nodes).
		placeholders := make([]string, len(frontier))
		args := make([]any, 0, len(frontier)+2)
		for i, id := range frontier {
			placeholders[i] = "?"
			args = append(args, id)
		}
		args = append(args, workspaceID, int64(maxFanOut+1))
		q := `SELECT r.id, r.experiment_id, COALESCE(r.name,''), r.status, r.start_time, r.end_time,
		             r.artifact_uri, r.lifecycle_stage, COALESCE(r.user_id,''),
		             COALESCE(r.source_type,''), COALESCE(r.source_name,''), r.run_kind,
		             COALESCE(r.parent_run_id,'')
		      FROM runs r
		      JOIN experiments e ON e.id = r.experiment_id
		      WHERE r.parent_run_id IN (` + strings.Join(placeholders, ",") + `)
		            AND r.lifecycle_stage = 'active'
		            AND e.workspace_id = ?
		      ORDER BY r.start_time ASC
		      LIMIT ?`
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, false, err
		}
		nextFrontier := make([]string, 0, len(frontier))
		for rows.Next() {
			var r model.Run
			var endTime sql.NullInt64
			if err := rows.Scan(&r.ID, &r.ExperimentID, &r.Name, &r.Status, &r.StartTime, &endTime,
				&r.ArtifactURI, &r.LifecycleStage, &r.UserID, &r.SourceType, &r.SourceName, &r.Kind, &r.ParentRunID); err != nil {
				_ = rows.Close()
				return nil, false, err
			}
			if endTime.Valid {
				v := endTime.Int64
				r.EndTime = &v
			}
			if _, seen := visited[r.ID]; seen {
				continue
			}
			// We over-fetched by 1 (LIMIT maxFanOut+1) precisely to detect
			// truncation. If we already have maxFanOut new nodes at this
			// level, stop appending — the extra row only signals "more
			// existed."
			if len(nextFrontier) >= maxFanOut {
				truncated = true
				continue
			}
			visited[r.ID] = struct{}{}
			out = append(out, &r)
			nextFrontier = append(nextFrontier, r.ID)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		frontier = nextFrontier
	}
	if len(frontier) > 0 {
		// Frontier still has nodes but we hit maxDepth — there is more to walk.
		truncated = true
	}
	return out, truncated, nil
}

// runDatasetEdges joins dataset_inputs (run→name+digest) with datasets_v2
// (name+content_hash → version + id) so the lineage response can render
// run→dataset edges.
//
// The datasets_v2 mirror is workspace-scoped: the same (name, digest) pair
// can exist in multiple workspaces, so we MUST filter by the run's
// workspace to avoid (a) row explosion on the LEFT JOIN and (b) leaking
// a sibling workspace's dataset version. Falls back to Version=0 /
// DatasetID=0 when no v1.2 mirror exists for this workspace (legacy
// v0.3 data) — UI degrades the link to the dataset list page.
func (s *SQLiteStore) runDatasetEdges(ctx context.Context, runID, workspaceID string) ([]DatasetEdge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT di.run_id,
		       di.name,
		       di.digest,
		       COALESCE(d2.version, 0)  AS version,
		       COALESCE(d2.id, 0)       AS dataset_id
		FROM dataset_inputs AS di
		LEFT JOIN datasets_v2 AS d2
		       ON d2.name = di.name
		      AND d2.content_hash = di.digest
		      AND d2.workspace_id = ?
		WHERE di.run_id = ?
		ORDER BY di.id ASC
	`, workspaceID, runID)
	if err != nil {
		return nil, fmt.Errorf("run dataset edges: %w", err)
	}
	defer rows.Close()
	out := []DatasetEdge{}
	for rows.Next() {
		var e DatasetEdge
		if err := rows.Scan(&e.RunID, &e.Name, &e.Digest, &e.Version, &e.DatasetID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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

	// v1.5 time-travel: capture pre-state per run BEFORE the bulk
	// UPDATE so the post-commit event writes have a meaningful `before`.
	// Also capture pre-tag values so the auto-archive tag_set is undoable.
	beforeState := make(map[string]map[string]any, len(ids))
	beforeTag := make(map[string]string, len(ids))
	for _, id := range ids {
		beforeState[id] = s.captureRunBefore(ctx, id)
		if v, ok := s.readTagValue(ctx, id, "lmf.auto_archived"); ok {
			beforeTag[id] = v
		}
	}

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

	// Events are written after commit so a rollback doesn't leave
	// orphan event rows pointing at no-op state. Failures here surface
	// in slog; the bulk operation itself has already succeeded.
	for _, id := range ids {
		if before := beforeState[id]; before != nil {
			if err := s.writeRunEvent(ctx, EventRunUpdate, id,
				map[string]any{"before": before}); err != nil {
				slog.Warn("janitor: event write failed", "run_id", id, "kind", EventRunUpdate, "err", err)
			}
		}
		tagPayload := map[string]any{"key": "lmf.auto_archived", "value": "stale"}
		if v, ok := beforeTag[id]; ok {
			tagPayload["before"] = v
		}
		if err := s.writeRunEvent(ctx, EventTagSet, id, tagPayload); err != nil {
			slog.Warn("janitor: event write failed", "run_id", id, "kind", EventTagSet, "err", err)
		}
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

// ----- dashboards -----

// GetDashboard returns the dashboard for (workspace, project), or ErrNotFound.
func (s *SQLiteStore) GetDashboard(ctx context.Context, workspaceID, project string) (*model.Dashboard, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, workspace_id, project, widgets, created_at, updated_at
		FROM dashboards WHERE workspace_id = ? AND project = ?
	`, workspaceID, project)
	var d model.Dashboard
	if err := row.Scan(&d.ID, &d.WorkspaceID, &d.Project, &d.Widgets, &d.CreatedAt, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

// SaveDashboard upserts the widgets JSON for (workspace, project). Returns
// the resulting dashboard row.
func (s *SQLiteStore) SaveDashboard(ctx context.Context, workspaceID, project, widgetsJSON string) (*model.Dashboard, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	now := time.Now().UnixMilli()
	if widgetsJSON == "" {
		widgetsJSON = "[]"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dashboards(workspace_id, project, widgets, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, project) DO UPDATE SET
			widgets = excluded.widgets,
			updated_at = excluded.updated_at
	`, workspaceID, project, widgetsJSON, now, now)
	if err != nil {
		return nil, fmt.Errorf("save dashboard: %w", err)
	}
	return s.GetDashboard(ctx, workspaceID, project)
}

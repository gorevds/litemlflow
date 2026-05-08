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

// CreateDatasetVersion inserts a new version of a named dataset and writes
// any parent edges.
//
// Versioning: the next version is `MAX(version) + 1` per (workspace, name).
// We compute it inside a single transaction so concurrent uploads of the
// same name don't collide on the UNIQUE (workspace_id, name, version)
// index. Parent edges are validated to belong to the same workspace and
// reject any edge that would form a cycle (parent transitively references
// the new id, which is impossible at insert time since the id was just
// created — but we double-check for parent_id == self).
//
// Returns the populated DatasetVersion with ID + Version set.
func (s *SQLiteStore) CreateDatasetVersion(ctx context.Context, d *model.DatasetVersion, parents []int64) (*model.DatasetVersion, error) {
	if d.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := model.ValidName(d.Name, 250); err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if d.ContentHash == "" {
		return nil, fmt.Errorf("content_hash is required")
	}
	if d.WorkspaceID == "" {
		d.WorkspaceID = "default"
	}
	if d.CreatedAt == 0 {
		d.CreatedAt = time.Now().UnixMilli()
	}
	if d.LifecycleStage == "" {
		d.LifecycleStage = "active"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Validate parent edges before inserting the child row.
	for _, pid := range parents {
		var ws string
		err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM datasets_v2 WHERE id = ?`, pid).Scan(&ws)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("parent dataset %d not found", pid)
			}
			return nil, err
		}
		if ws != d.WorkspaceID {
			return nil, fmt.Errorf("parent %d is in workspace %q, child is in %q", pid, ws, d.WorkspaceID)
		}
	}

	// Compute next version.
	var maxVer sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT MAX(version) FROM datasets_v2 WHERE workspace_id = ? AND name = ?`,
		d.WorkspaceID, d.Name,
	).Scan(&maxVer)
	if err != nil {
		return nil, err
	}
	d.Version = maxVer.Int64 + 1

	res, err := tx.ExecContext(ctx, `
		INSERT INTO datasets_v2(name, version, content_hash, size_bytes, schema_json,
		                        description, workspace_id, created_at, created_by, lifecycle_stage)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, d.Name, d.Version, d.ContentHash, d.SizeBytes,
		nilIfEmpty(d.SchemaJSON), nilIfEmpty(d.Description),
		d.WorkspaceID, d.CreatedAt, nilIfEmpty(d.CreatedBy), d.LifecycleStage)
	if err != nil {
		return nil, fmt.Errorf("insert dataset_v2: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	d.ID = id

	// Cycle defense: reject parent_id == self (can't happen at insert,
	// but defensive — and sets the contract for future edits).
	for _, pid := range parents {
		if pid == id {
			return nil, fmt.Errorf("dataset cannot be its own parent")
		}
	}

	for _, pid := range parents {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dataset_lineage(child_id, parent_id) VALUES (?, ?)
			 ON CONFLICT(child_id, parent_id) DO NOTHING`,
			id, pid,
		); err != nil {
			return nil, fmt.Errorf("insert lineage edge %d→%d: %w", pid, id, err)
		}
	}
	d.Parents = append([]int64(nil), parents...)

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d, nil
}

// ListDatasets returns the latest active version of each dataset name in
// the workspace, newest-created-at first.
func (s *SQLiteStore) ListDatasets(ctx context.Context, workspaceID string) ([]*model.DatasetVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.name, d.version, d.content_hash, d.size_bytes,
		       COALESCE(d.schema_json,''), COALESCE(d.description,''),
		       d.workspace_id, d.created_at, COALESCE(d.created_by,''), d.lifecycle_stage
		FROM datasets_v2 d
		JOIN (
			SELECT name, MAX(version) AS v
			FROM datasets_v2
			WHERE workspace_id = ? AND lifecycle_stage = 'active'
			GROUP BY name
		) latest ON latest.name = d.name AND latest.v = d.version
		WHERE d.workspace_id = ?
		ORDER BY d.created_at DESC
	`, workspaceID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDatasetVersions(rows)
}

// ListDatasetVersions returns all versions of one dataset name in the
// workspace, newest version first (active and deleted).
func (s *SQLiteStore) ListDatasetVersions(ctx context.Context, workspaceID, name string) ([]*model.DatasetVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, version, content_hash, size_bytes,
		       COALESCE(schema_json,''), COALESCE(description,''),
		       workspace_id, created_at, COALESCE(created_by,''), lifecycle_stage
		FROM datasets_v2
		WHERE workspace_id = ? AND name = ?
		ORDER BY version DESC
	`, workspaceID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDatasetVersions(rows)
}

// GetDatasetVersion fetches one (workspace, name, version) row plus its
// parent edges.
func (s *SQLiteStore) GetDatasetVersion(ctx context.Context, workspaceID, name string, version int64) (*model.DatasetVersion, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, size_bytes,
		       COALESCE(schema_json,''), COALESCE(description,''),
		       workspace_id, created_at, COALESCE(created_by,''), lifecycle_stage
		FROM datasets_v2
		WHERE workspace_id = ? AND name = ? AND version = ?
	`, workspaceID, name, version)
	d, err := scanDatasetVersion(row)
	if err != nil {
		return nil, err
	}
	// Pull parents.
	parents, err := s.datasetParents(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	d.Parents = parents
	return d, nil
}

func (s *SQLiteStore) datasetParents(ctx context.Context, id int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT parent_id FROM dataset_lineage WHERE child_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var p int64
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetDatasetLineage walks ancestors and descendants of one dataset row.
// Walks are iterative with a visited set so a malformed lineage edge
// cannot DoS the request (same defense as run lineage).
//
// The maxLineageNodes cap is a *node* cap — total returned ancestors or
// descendants — not a depth cap. We check it inside the inner frontier
// walk so a single wide level doesn't blow past the bound.
func (s *SQLiteStore) GetDatasetLineage(ctx context.Context, workspaceID, name string, version int64) (*model.DatasetLineage, error) {
	self, err := s.GetDatasetVersion(ctx, workspaceID, name, version)
	if err != nil {
		return nil, err
	}
	const maxLineageNodes = 256
	visited := map[int64]struct{}{self.ID: {}}

	// Ancestors: BFS over dataset_lineage(child_id = current → parent_id).
	var ancestors []*model.DatasetVersion
	frontier := append([]int64(nil), self.Parents...)
ancLoop:
	for len(frontier) > 0 {
		next := []int64{}
		for _, pid := range frontier {
			if len(ancestors) >= maxLineageNodes {
				break ancLoop
			}
			if _, seen := visited[pid]; seen {
				continue
			}
			visited[pid] = struct{}{}
			p, err := s.getDatasetVersionByID(ctx, pid)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return nil, err
			}
			ancestors = append(ancestors, p)
			next = append(next, p.Parents...)
		}
		frontier = next
	}

	// Descendants: walk dataset_lineage(parent_id = current → child_id).
	var descendants []*model.DatasetVersion
	desc := []int64{self.ID}
descLoop:
	for len(desc) > 0 {
		next := []int64{}
		for _, pid := range desc {
			if len(descendants) >= maxLineageNodes {
				break descLoop
			}
			rows, err := s.db.QueryContext(ctx, `SELECT child_id FROM dataset_lineage WHERE parent_id = ?`, pid)
			if err != nil {
				return nil, err
			}
			var cids []int64
			for rows.Next() {
				var cid int64
				if err := rows.Scan(&cid); err != nil {
					rows.Close()
					return nil, err
				}
				cids = append(cids, cid)
			}
			rows.Close()
			for _, cid := range cids {
				if len(descendants) >= maxLineageNodes {
					break descLoop
				}
				if _, seen := visited[cid]; seen {
					continue
				}
				visited[cid] = struct{}{}
				c, err := s.getDatasetVersionByID(ctx, cid)
				if err != nil {
					if errors.Is(err, ErrNotFound) {
						continue
					}
					return nil, err
				}
				descendants = append(descendants, c)
				next = append(next, cid)
			}
		}
		desc = next
	}

	return &model.DatasetLineage{Self: self, Ancestors: ancestors, Descendants: descendants}, nil
}

func (s *SQLiteStore) getDatasetVersionByID(ctx context.Context, id int64) (*model.DatasetVersion, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, size_bytes,
		       COALESCE(schema_json,''), COALESCE(description,''),
		       workspace_id, created_at, COALESCE(created_by,''), lifecycle_stage
		FROM datasets_v2 WHERE id = ?
	`, id)
	d, err := scanDatasetVersion(row)
	if err != nil {
		return nil, err
	}
	parents, err := s.datasetParents(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Parents = parents
	return d, nil
}

// SoftDeleteDatasetVersion marks one version inactive. The CAS object is
// not removed — content addressing means another dataset may reference
// the same hash, and the operator can run a separate GC pass when ready.
func (s *SQLiteStore) SoftDeleteDatasetVersion(ctx context.Context, workspaceID, name string, version int64) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE datasets_v2 SET lifecycle_stage = 'deleted'
		WHERE workspace_id = ? AND name = ? AND version = ?
	`, workspaceID, name, version)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DatasetHashStillReferenced is a helper for offline GC: returns true if
// any active row references the given content_hash (across all workspaces).
func (s *SQLiteStore) DatasetHashStillReferenced(ctx context.Context, hash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM datasets_v2 WHERE content_hash = ? AND lifecycle_stage = 'active'`,
		hash,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ----- shared scanners -----

type datasetScanner interface {
	Scan(dest ...any) error
}

func scanDatasetVersion(row datasetScanner) (*model.DatasetVersion, error) {
	var d model.DatasetVersion
	err := row.Scan(&d.ID, &d.Name, &d.Version, &d.ContentHash, &d.SizeBytes,
		&d.SchemaJSON, &d.Description, &d.WorkspaceID, &d.CreatedAt,
		&d.CreatedBy, &d.LifecycleStage)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func scanDatasetVersions(rows *sql.Rows) ([]*model.DatasetVersion, error) {
	var out []*model.DatasetVersion
	for rows.Next() {
		var d model.DatasetVersion
		if err := rows.Scan(&d.ID, &d.Name, &d.Version, &d.ContentHash, &d.SizeBytes,
			&d.SchemaJSON, &d.Description, &d.WorkspaceID, &d.CreatedAt,
			&d.CreatedBy, &d.LifecycleStage); err != nil {
			return nil, err
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// debug helper — prevents linter warnings about unused imports if the
// strings package is referenced.
var _ = strings.HasPrefix

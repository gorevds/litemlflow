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

// ----- workspaces -----

// CreateWorkspace inserts a new workspace.
// Returns ErrAlreadyExists when the id or name is already taken.
func (s *SQLiteStore) CreateWorkspace(ctx context.Context, w *model.Workspace) error {
	if err := validateWorkspaceID(w.ID); err != nil {
		return err
	}
	if w.Name == "" {
		return errors.New("workspace name cannot be empty")
	}
	now := time.Now().UnixMilli()
	if w.CreationTime == 0 {
		w.CreationTime = now
	}
	if w.LastUpdateTime == 0 {
		w.LastUpdateTime = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workspaces(id, name, description, creation_time, last_update_time)
		VALUES (?, ?, ?, ?, ?)
	`, w.ID, w.Name, nilIfEmpty(w.Description), w.CreationTime, w.LastUpdateTime)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("insert workspace: %w", err)
	}
	return nil
}

// GetWorkspace returns the workspace with the given id.
func (s *SQLiteStore) GetWorkspace(ctx context.Context, id string) (*model.Workspace, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, COALESCE(description,''), creation_time, last_update_time
		FROM workspaces WHERE id = ?
	`, id)
	return scanWorkspace(row)
}

// ListWorkspaces returns all workspaces ordered by creation_time ascending.
func (s *SQLiteStore) ListWorkspaces(ctx context.Context) ([]*model.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, COALESCE(description,''), creation_time, last_update_time
		FROM workspaces ORDER BY creation_time ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Workspace
	for rows.Next() {
		var w model.Workspace
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.CreationTime, &w.LastUpdateTime); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// UpdateWorkspace modifies the name and/or description of a workspace.
// nil pointers are left unchanged.
func (s *SQLiteStore) UpdateWorkspace(ctx context.Context, id string, name *string, description *string) error {
	sets := []string{}
	args := []any{}
	if name != nil {
		if *name == "" {
			return errors.New("workspace name cannot be empty")
		}
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		sets = append(sets, "description = ?")
		args = append(args, nilIfEmpty(*description))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "last_update_time = ?")
	args = append(args, time.Now().UnixMilli())
	args = append(args, id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE workspaces SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorkspace removes a workspace. Returns ErrConflict if the workspace is
// "default" or still has experiments assigned to it.
func (s *SQLiteStore) DeleteWorkspace(ctx context.Context, id string) error {
	if id == "default" {
		return fmt.Errorf("%w: cannot delete the default workspace", ErrConflict)
	}
	// Check for experiments in this workspace (any lifecycle stage).
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM experiments WHERE workspace_id = ?`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: workspace %q has %d experiment(s); move or delete them first", ErrConflict, id, count)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanWorkspace(row *sql.Row) (*model.Workspace, error) {
	var w model.Workspace
	if err := row.Scan(&w.ID, &w.Name, &w.Description, &w.CreationTime, &w.LastUpdateTime); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &w, nil
}

// validateWorkspaceID checks that the id is a non-empty slug containing only
// lowercase letters, digits, and hyphens, max 64 characters.
func validateWorkspaceID(id string) error {
	if id == "" {
		return errors.New("workspace id cannot be empty")
	}
	if len(id) > 64 {
		return errors.New("workspace id exceeds 64 characters")
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return fmt.Errorf("workspace id %q contains invalid character %q (only lowercase letters, digits, hyphens allowed)", id, r)
		}
	}
	return nil
}

// ----- workspace members -----

// AddMember sets or updates the role of a user in a workspace.
func (s *SQLiteStore) AddMember(ctx context.Context, workspaceID, userID, role string) error {
	if role != "viewer" && role != "editor" && role != "admin" {
		return fmt.Errorf("invalid role %q: must be viewer, editor, or admin", role)
	}
	// Verify workspace exists.
	if _, err := s.GetWorkspace(ctx, workspaceID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workspace_members(workspace_id, user_id, role) VALUES (?, ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role
	`, workspaceID, userID, role)
	return err
}

// RemoveMember revokes a user's membership in a workspace.
func (s *SQLiteStore) RemoveMember(ctx context.Context, workspaceID, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListMembers returns all members of a workspace.
func (s *SQLiteStore) ListMembers(ctx context.Context, workspaceID string) ([]*model.WorkspaceMember, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workspace_id, user_id, role FROM workspace_members
		WHERE workspace_id = ? ORDER BY user_id ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WorkspaceMember
	for rows.Next() {
		var m model.WorkspaceMember
		if err := rows.Scan(&m.WorkspaceID, &m.UserID, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// GetMemberRole returns the role of a user in a workspace.
// Returns ErrNotFound if the user is not a member.
func (s *SQLiteStore) GetMemberRole(ctx context.Context, workspaceID, userID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		workspaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

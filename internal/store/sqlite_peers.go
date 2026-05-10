package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// CreatePeer inserts a new federation peer. UNIQUE(workspace_id, name) so
// re-adding under the same name returns ErrAlreadyExists.
func (s *SQLiteStore) CreatePeer(ctx context.Context, p *model.Peer) (int64, error) {
	if p.Name == "" {
		return 0, fmt.Errorf("peer name is required")
	}
	if p.URL == "" {
		return 0, fmt.Errorf("peer url is required")
	}
	if p.Secret == "" {
		return 0, fmt.Errorf("peer secret is required")
	}
	if p.WorkspaceID == "" {
		p.WorkspaceID = "default"
	}
	if p.AddedAt == 0 {
		p.AddedAt = time.Now().UnixMilli()
	}
	if p.Status == "" {
		p.Status = "pending"
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO peers(name, url, secret, workspace_id, added_at, status, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, p.Name, p.URL, p.Secret, p.WorkspaceID, p.AddedAt, p.Status, nilIfEmpty(p.LastError))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrAlreadyExists
		}
		return 0, fmt.Errorf("insert peer: %w", err)
	}
	return res.LastInsertId()
}

// ListPeers returns all peers for a workspace, oldest first.
func (s *SQLiteStore) ListPeers(ctx context.Context, workspaceID string) ([]*model.Peer, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, url, secret, workspace_id, added_at, last_seen,
		       status, COALESCE(last_error, '')
		FROM peers
		WHERE workspace_id = ?
		ORDER BY added_at ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPeers(rows)
}

// GetPeer fetches one peer by ID, scoped to workspace.
func (s *SQLiteStore) GetPeer(ctx context.Context, workspaceID string, id int64) (*model.Peer, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, secret, workspace_id, added_at, last_seen,
		       status, COALESCE(last_error, '')
		FROM peers WHERE id = ? AND workspace_id = ?
	`, id, workspaceID)
	return scanPeerRow(row)
}

// GetPeerByName is what the receiving side uses to look up the peer's
// secret when validating an inbound HMAC. Workspace fan-out is by name —
// peers across workspaces with the same name would collide; UNIQUE
// constraint prevents that within a workspace.
func (s *SQLiteStore) GetPeerByName(ctx context.Context, workspaceID, name string) (*model.Peer, error) {
	if workspaceID == "" {
		workspaceID = "default"
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, url, secret, workspace_id, added_at, last_seen,
		       status, COALESCE(last_error, '')
		FROM peers WHERE name = ? AND workspace_id = ?
	`, name, workspaceID)
	return scanPeerRow(row)
}

// DeletePeer removes a peer row. Returns ErrNotFound if no row matched.
func (s *SQLiteStore) DeletePeer(ctx context.Context, workspaceID string, id int64) error {
	if workspaceID == "" {
		workspaceID = "default"
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM peers WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePeerStatus records the outcome of an echo (or other liveness probe).
// status is one of pending/connected/error; lastError is empty for success.
func (s *SQLiteStore) UpdatePeerStatus(ctx context.Context, id int64, status, lastError string, lastSeen int64) error {
	if status != "pending" && status != "connected" && status != "error" {
		return fmt.Errorf("invalid peer status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE peers SET status = ?, last_error = ?, last_seen = ?
		WHERE id = ?
	`, status, nilIfEmpty(lastError), nilIfNonPositive(lastSeen), id)
	return err
}

// nilIfNonPositive — hide zero/negative timestamps from the DB.
func nilIfNonPositive(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func scanPeers(rows *sql.Rows) ([]*model.Peer, error) {
	var out []*model.Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type peerScanner interface {
	Scan(...any) error
}

func scanPeer(s peerScanner) (*model.Peer, error) {
	var p model.Peer
	var lastSeen sql.NullInt64
	if err := s.Scan(&p.ID, &p.Name, &p.URL, &p.Secret, &p.WorkspaceID,
		&p.AddedAt, &lastSeen, &p.Status, &p.LastError); err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		v := lastSeen.Int64
		p.LastSeen = &v
	}
	return &p, nil
}

func scanPeerRow(row *sql.Row) (*model.Peer, error) {
	p, err := scanPeer(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

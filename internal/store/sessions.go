// Session persistence methods on SQLiteStore.
//
// This file adds session CRUD to *SQLiteStore without touching sqlite.go,
// which is managed separately. The sessions table is created by migration 002.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gorevds/litemlflow/internal/model"
)

// CreateSession inserts a new session row. The session ID must be pre-set.
func (s *SQLiteStore) CreateSession(ctx context.Context, sess *model.Session) error {
	if sess.ID == "" {
		return errors.New("session ID must not be empty")
	}
	now := time.Now().UnixMilli()
	if sess.CreatedAt == 0 {
		sess.CreatedAt = now
	}
	if sess.LastSeen == 0 {
		sess.LastSeen = now
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(id, user_id, user_email, user_name, auth_method, created_at, expires_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, sess.ID, sess.UserID, nilIfEmpty(sess.UserEmail), nilIfEmpty(sess.UserName),
		sess.AuthMethod, sess.CreatedAt, sess.ExpiresAt, sess.LastSeen)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession loads a session by ID. Returns ErrNotFound if the row does not
// exist or the session has already expired.
func (s *SQLiteStore) GetSession(ctx context.Context, id string) (*model.Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, COALESCE(user_email,''), COALESCE(user_name,''),
		       auth_method, created_at, expires_at, last_seen
		FROM sessions WHERE id = ?
	`, id)
	var sess model.Session
	if err := row.Scan(&sess.ID, &sess.UserID, &sess.UserEmail, &sess.UserName,
		&sess.AuthMethod, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	// Treat expired sessions as not-found so callers don't need to check
	// expiry themselves.
	if sess.ExpiresAt < time.Now().UnixMilli() {
		return nil, ErrNotFound
	}
	return &sess, nil
}

// TouchSession updates the last_seen timestamp for an active session.
// Returns ErrNotFound if the session does not exist (or is expired).
func (s *SQLiteStore) TouchSession(ctx context.Context, id string, lastSeen int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET last_seen = ? WHERE id = ? AND expires_at > ?
	`, lastSeen, id, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSession removes a session row. Returns ErrNotFound when absent.
func (s *SQLiteStore) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GarbageCollectSessions deletes all sessions whose expires_at is in the past.
// Returns the number of rows deleted.
func (s *SQLiteStore) GarbageCollectSessions(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("gc sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

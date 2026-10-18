package db

import (
	"context"
	"database/sql"
)

type Session struct {
	ID               string
	UserID           string
	RefreshTokenHash string
	CreatedAt        int64
	ExpiresAt        int64
	RevokedAt        *int64
}

func (d *DB) CreateSession(ctx context.Context, s *Session) error {
	s.ID, s.CreatedAt = newID(), now()
	_, err := d.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, refresh_token_hash, created_at, expires_at) VALUES (?,?,?,?,?)`,
		s.ID, s.UserID, s.RefreshTokenHash, s.CreatedAt, s.ExpiresAt)
	return err
}

func (d *DB) SessionByID(ctx context.Context, id string) (*Session, error) {
	return scanSession(d.QueryRowContext(ctx,
		`SELECT id, user_id, refresh_token_hash, created_at, expires_at, revoked_at FROM sessions WHERE id = ?`, id))
}

func (d *DB) SessionByRefreshHash(ctx context.Context, hash string) (*Session, error) {
	return scanSession(d.QueryRowContext(ctx,
		`SELECT id, user_id, refresh_token_hash, created_at, expires_at, revoked_at FROM sessions WHERE refresh_token_hash = ?`, hash))
}

func scanSession(row *sql.Row) (*Session, error) {
	var s Session
	var rev sql.NullInt64
	if err := row.Scan(&s.ID, &s.UserID, &s.RefreshTokenHash, &s.CreatedAt, &s.ExpiresAt, &rev); err != nil {
		return nil, notFound(err)
	}
	s.RevokedAt = int64Ptr(rev)
	return &s, nil
}

// RotateSession atomically replaces the refresh hash of an active session.
// It returns ErrNotFound if the session is revoked or the old hash no longer
// matches (i.e. the token was already rotated).
func (d *DB) RotateSession(ctx context.Context, id, oldHash, newHash string, expiresAt int64) error {
	return execOne(d.ExecContext(ctx,
		`UPDATE sessions SET refresh_token_hash = ?, expires_at = ?
		 WHERE id = ? AND refresh_token_hash = ? AND revoked_at IS NULL`,
		newHash, expiresAt, id, oldHash))
}

func (d *DB) RevokeSession(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now(), id)
	return err
}

func (d *DB) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := d.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now(), userID)
	return err
}

// PruneSessions deletes sessions that expired or were revoked before cutoff.
func (d *DB) PruneSessions(ctx context.Context, cutoff int64) error {
	_, err := d.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR revoked_at < ?`, cutoff, cutoff)
	return err
}

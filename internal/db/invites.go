package db

import (
	"context"
	"database/sql"
	"errors"
)

// ErrInviteInvalid is returned when an invite is unknown, used, revoked or expired.
var ErrInviteInvalid = errors.New("invite is invalid or has expired")

type Invite struct {
	ID                string  `json:"id"`
	TokenHash         string  `json:"-"`
	CreatedBy         *string `json:"created_by"`
	CreatedAt         int64   `json:"created_at"`
	ExpiresAt         int64   `json:"expires_at"`
	UsedAt            *int64  `json:"used_at"`
	UsedBy            *string `json:"used_by"`
	WhitelistServerID *string `json:"whitelist_server_id"`
}

const inviteCols = `id, token_hash, created_by, created_at, expires_at, used_at, used_by, whitelist_server_id`

func scanInvite(row interface{ Scan(...any) error }) (*Invite, error) {
	var i Invite
	var usedAt sql.NullInt64
	var by, usedBy, wl sql.NullString
	if err := row.Scan(&i.ID, &i.TokenHash, &by, &i.CreatedAt, &i.ExpiresAt, &usedAt, &usedBy, &wl); err != nil {
		return nil, notFound(err)
	}
	i.CreatedBy, i.UsedAt, i.UsedBy, i.WhitelistServerID = strPtr(by), int64Ptr(usedAt), strPtr(usedBy), strPtr(wl)
	return &i, nil
}

func (d *DB) CreateInvite(ctx context.Context, i *Invite) error {
	i.ID, i.CreatedAt = newID(), now()
	_, err := d.ExecContext(ctx,
		`INSERT INTO invites (id, token_hash, created_by, created_at, expires_at, whitelist_server_id) VALUES (?,?,?,?,?,?)`,
		i.ID, i.TokenHash, nullStr(i.CreatedBy), i.CreatedAt, i.ExpiresAt, nullStr(i.WhitelistServerID))
	return err
}

// PendingInvites lists unused, unexpired invites, newest first.
func (d *DB) PendingInvites(ctx context.Context) ([]*Invite, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+inviteCols+` FROM invites WHERE used_at IS NULL AND expires_at > ? ORDER BY created_at DESC`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Invite{}
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ValidInviteByHash returns a pending, unexpired invite by token hash.
func (d *DB) ValidInviteByHash(ctx context.Context, hash string) (*Invite, error) {
	i, err := scanInvite(d.QueryRowContext(ctx,
		`SELECT `+inviteCols+` FROM invites WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`, hash, now()))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrInviteInvalid
	}
	return i, err
}

// RevokeInvite deletes a pending invite.
func (d *DB) RevokeInvite(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM invites WHERE id = ? AND used_at IS NULL`, id))
}

// RedeemInvite atomically creates u and marks the invite used. If the
// invite was redeemed concurrently or has expired, nothing is written and
// ErrInviteInvalid is returned.
func (d *DB) RedeemInvite(ctx context.Context, inviteID string, u *User) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	u.ID, u.CreatedAt = newID(), now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, nullStr(u.MCUsername), u.PasswordHash, u.Role, u.CreatedAt, nullStr(u.CreatedBy), u.CreatedAt); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE invites SET used_at = ?, used_by = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		u.CreatedAt, u.ID, inviteID, u.CreatedAt)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInviteInvalid
	}
	return tx.Commit()
}

type WhitelistEntry struct {
	ID         string `json:"id"`
	ServerID   string `json:"server_id"`
	MCUsername string `json:"mc_username"`
	QueuedAt   int64  `json:"queued_at"`
	AppliedAt  *int64 `json:"applied_at"`
}

// QueueWhitelist records a pending whitelist addition.
func (d *DB) QueueWhitelist(ctx context.Context, serverID, mcUsername string, applied bool) (*WhitelistEntry, error) {
	e := &WhitelistEntry{ID: newID(), ServerID: serverID, MCUsername: mcUsername, QueuedAt: now()}
	var appliedAt any
	if applied {
		e.AppliedAt = &e.QueuedAt
		appliedAt = e.QueuedAt
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO whitelist_queue (id, server_id, mc_username, queued_at, applied_at) VALUES (?,?,?,?,?)`,
		e.ID, e.ServerID, e.MCUsername, e.QueuedAt, appliedAt)
	return e, err
}

// PendingWhitelist lists unapplied entries for a server, oldest first.
func (d *DB) PendingWhitelist(ctx context.Context, serverID string) ([]*WhitelistEntry, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, server_id, mc_username, queued_at FROM whitelist_queue
		 WHERE server_id = ? AND applied_at IS NULL ORDER BY queued_at`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*WhitelistEntry
	for rows.Next() {
		var e WhitelistEntry
		if err := rows.Scan(&e.ID, &e.ServerID, &e.MCUsername, &e.QueuedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (d *DB) MarkWhitelistApplied(ctx context.Context, id string) error {
	_, err := d.ExecContext(ctx, `UPDATE whitelist_queue SET applied_at = ? WHERE id = ?`, now(), id)
	return err
}

// CountAdmins returns the number of admin users.
func (d *DB) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&n)
	return n, err
}

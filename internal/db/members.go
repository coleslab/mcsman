package db

import (
	"context"
	"database/sql"
	"errors"
)

// Member is a user's membership of a server, joined with the user's names.
type Member struct {
	ServerID   string  `json:"server_id"`
	UserID     string  `json:"user_id"`
	Username   string  `json:"username"`
	MCUsername *string `json:"mc_username"`
	Role       string  `json:"role"`
	AddedBy    *string `json:"added_by"`
	CreatedAt  int64   `json:"created_at"`
}

// AddMember makes userID a member of serverID. It reports whether the
// membership is new (false if it already existed).
func (d *DB) AddMember(ctx context.Context, serverID, userID string, addedBy *string) (bool, error) {
	res, err := d.ExecContext(ctx,
		`INSERT OR IGNORE INTO server_members (server_id, user_id, added_by, created_at) VALUES (?,?,?,?)`,
		serverID, userID, nullStr(addedBy), now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (d *DB) RemoveMember(ctx context.Context, serverID, userID string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM server_members WHERE server_id = ? AND user_id = ?`, serverID, userID))
}

func (d *DB) IsMember(ctx context.Context, serverID, userID string) (bool, error) {
	var one int
	err := d.QueryRowContext(ctx, `SELECT 1 FROM server_members WHERE server_id = ? AND user_id = ?`, serverID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListMembers lists a server's members, oldest first.
func (d *DB) ListMembers(ctx context.Context, serverID string) ([]*Member, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT m.server_id, m.user_id, u.username, u.mc_username, u.role, m.added_by, m.created_at
		 FROM server_members m JOIN users u ON u.id = m.user_id
		 WHERE m.server_id = ? ORDER BY m.created_at, u.username`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Member{}
	for rows.Next() {
		var m Member
		var mc, by sql.NullString
		if err := rows.Scan(&m.ServerID, &m.UserID, &m.Username, &mc, &m.Role, &by, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.MCUsername, m.AddedBy = strPtr(mc), strPtr(by)
		out = append(out, &m)
	}
	return out, rows.Err()
}

// ServersForUser returns the IDs of the servers userID is a member of.
func (d *DB) ServersForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT server_id FROM server_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) SetMCUsername(ctx context.Context, id string, mc *string) error {
	return execOne(d.ExecContext(ctx, `UPDATE users SET mc_username = ? WHERE id = ?`, nullStr(mc), id))
}

// AcceptInvite marks an invite used by an existing user, adds the server
// membership and, if setMC is non-nil, saves the user's Minecraft name, in
// one transaction. ErrInviteInvalid means the invite was used or expired
// concurrently.
func (d *DB) AcceptInvite(ctx context.Context, inv *Invite, userID string, setMC *string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	if setMC != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET mc_username = ? WHERE id = ?`, *setMC, userID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE invites SET used_at = ?, used_by = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		t, userID, inv.ID, t)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInviteInvalid
	}
	if inv.WhitelistServerID != nil {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO server_members (server_id, user_id, added_by, created_at) VALUES (?,?,?,?)`,
			*inv.WhitelistServerID, userID, nullStr(inv.CreatedBy), t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

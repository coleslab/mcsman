package db

import (
	"context"
	"database/sql"
)

// MaxConfigRevisions is how many previous versions are kept per file.
const MaxConfigRevisions = 10

type ConfigRevision struct {
	ID        string  `json:"id"`
	ServerID  string  `json:"server_id"`
	Path      string  `json:"path"`
	SHA256    string  `json:"sha256"`
	Content   []byte  `json:"-"`
	UserID    *string `json:"user_id"`
	Username  *string `json:"username"`
	CreatedAt int64   `json:"created_at"`
}

// AddConfigRevision stores r and prunes the file's history to
// MaxConfigRevisions.
func (d *DB) AddConfigRevision(ctx context.Context, r *ConfigRevision) error {
	r.ID, r.CreatedAt = newID(), now()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO config_revisions (id, server_id, path, sha256, content, user_id, created_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.ServerID, r.Path, r.SHA256, r.Content, nullStr(r.UserID), r.CreatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM config_revisions WHERE server_id = ? AND path = ? AND id NOT IN (
		   SELECT id FROM config_revisions WHERE server_id = ? AND path = ?
		   ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		r.ServerID, r.Path, r.ServerID, r.Path, MaxConfigRevisions); err != nil {
		return err
	}
	return tx.Commit()
}

// ListConfigRevisions lists a file's revisions, newest first, without content.
func (d *DB) ListConfigRevisions(ctx context.Context, serverID, path string) ([]*ConfigRevision, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT r.id, r.server_id, r.path, r.sha256, r.user_id, u.username, r.created_at
		 FROM config_revisions r LEFT JOIN users u ON u.id = r.user_id
		 WHERE r.server_id = ? AND r.path = ? ORDER BY r.created_at DESC, r.rowid DESC`, serverID, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ConfigRevision{}
	for rows.Next() {
		var r ConfigRevision
		var uid, uname sql.NullString
		if err := rows.Scan(&r.ID, &r.ServerID, &r.Path, &r.SHA256, &uid, &uname, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.UserID, r.Username = strPtr(uid), strPtr(uname)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ConfigRevisionByID returns one revision of a server's file, with content.
func (d *DB) ConfigRevisionByID(ctx context.Context, serverID, id string) (*ConfigRevision, error) {
	var r ConfigRevision
	var uid sql.NullString
	err := d.QueryRowContext(ctx,
		`SELECT id, server_id, path, sha256, content, user_id, created_at FROM config_revisions WHERE server_id = ? AND id = ?`,
		serverID, id).Scan(&r.ID, &r.ServerID, &r.Path, &r.SHA256, &r.Content, &uid, &r.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	r.UserID = strPtr(uid)
	return &r, nil
}

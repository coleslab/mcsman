package db

import (
	"context"
	"database/sql"
)

type Backup struct {
	ID        string  `json:"id"`
	ServerID  string  `json:"server_id"`
	Filename  string  `json:"filename"`
	SizeBytes int64   `json:"size_bytes"`
	SHA256    string  `json:"sha256"`
	Tag       *string `json:"tag"`
	CreatedAt int64   `json:"created_at"`
	CreatedBy *string `json:"created_by"`
}

const backupCols = `id, server_id, filename, size_bytes, sha256, tag, created_at, created_by`

func scanBackup(row interface{ Scan(...any) error }) (*Backup, error) {
	var b Backup
	var tag, by sql.NullString
	if err := row.Scan(&b.ID, &b.ServerID, &b.Filename, &b.SizeBytes, &b.SHA256, &tag, &b.CreatedAt, &by); err != nil {
		return nil, notFound(err)
	}
	b.Tag, b.CreatedBy = strPtr(tag), strPtr(by)
	return &b, nil
}

func (d *DB) CreateBackup(ctx context.Context, b *Backup) error {
	b.ID = newID()
	if b.CreatedAt == 0 {
		b.CreatedAt = now()
	}
	_, err := d.ExecContext(ctx, `INSERT INTO backups (`+backupCols+`) VALUES (?,?,?,?,?,?,?,?)`,
		b.ID, b.ServerID, b.Filename, b.SizeBytes, b.SHA256, nullStr(b.Tag), b.CreatedAt, nullStr(b.CreatedBy))
	return err
}

func (d *DB) BackupByID(ctx context.Context, serverID, id string) (*Backup, error) {
	return scanBackup(d.QueryRowContext(ctx,
		`SELECT `+backupCols+` FROM backups WHERE id = ? AND server_id = ?`, id, serverID))
}

// ListBackups returns a server's backups, newest first.
func (d *DB) ListBackups(ctx context.Context, serverID string) ([]*Backup, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT `+backupCols+` FROM backups WHERE server_id = ? ORDER BY created_at DESC, rowid DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Backup{}
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *DB) DeleteBackup(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM backups WHERE id = ?`, id))
}

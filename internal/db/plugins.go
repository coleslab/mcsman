package db

import (
	"context"
	"database/sql"
)

const (
	SourceModrinth = "modrinth"
	SourceHangar   = "hangar"
	SourceManual   = "manual"
)

type Plugin struct {
	ID          string  `json:"id"`
	ServerID    string  `json:"server_id"`
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	Source      string  `json:"source"`
	SourceID    *string `json:"source_id"`
	Filename    string  `json:"filename"`
	SHA256      string  `json:"sha256"`
	InstalledAt int64   `json:"installed_at"`
	UpdatedAt   int64   `json:"updated_at"`
}

const pluginCols = `id, server_id, name, version, source, source_id, filename, sha256, installed_at, updated_at`

func scanPlugin(row interface{ Scan(...any) error }) (*Plugin, error) {
	var p Plugin
	var sid sql.NullString
	if err := row.Scan(&p.ID, &p.ServerID, &p.Name, &p.Version, &p.Source, &sid, &p.Filename, &p.SHA256, &p.InstalledAt, &p.UpdatedAt); err != nil {
		return nil, notFound(err)
	}
	p.SourceID = strPtr(sid)
	return &p, nil
}

func (d *DB) CreatePlugin(ctx context.Context, p *Plugin) error {
	p.ID = newID()
	p.InstalledAt = now()
	p.UpdatedAt = p.InstalledAt
	_, err := d.ExecContext(ctx, `INSERT INTO plugins (`+pluginCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.ServerID, p.Name, p.Version, p.Source, nullStr(p.SourceID), p.Filename, p.SHA256, p.InstalledAt, p.UpdatedAt)
	return err
}

// UpdatePlugin records a new version/file for an installed plugin.
func (d *DB) UpdatePlugin(ctx context.Context, p *Plugin) error {
	p.UpdatedAt = now()
	return execOne(d.ExecContext(ctx,
		`UPDATE plugins SET name = ?, version = ?, source = ?, source_id = ?, filename = ?, sha256 = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Version, p.Source, nullStr(p.SourceID), p.Filename, p.SHA256, p.UpdatedAt, p.ID))
}

func (d *DB) PluginByID(ctx context.Context, serverID, id string) (*Plugin, error) {
	return scanPlugin(d.QueryRowContext(ctx, `SELECT `+pluginCols+` FROM plugins WHERE id = ? AND server_id = ?`, id, serverID))
}

func (d *DB) ListPlugins(ctx context.Context, serverID string) ([]*Plugin, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+pluginCols+` FROM plugins WHERE server_id = ? ORDER BY name COLLATE NOCASE`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Plugin{}
	for rows.Next() {
		p, err := scanPlugin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) DeletePlugin(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM plugins WHERE id = ?`, id))
}

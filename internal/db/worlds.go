package db

import (
	"context"
	"database/sql"
)

type WorldImport struct {
	ID                string  `json:"id"`
	ServerID          string  `json:"server_id"`
	ImportedAt        int64   `json:"imported_at"`
	ImportedBy        *string `json:"imported_by"`
	SourceName        string  `json:"source_name"`
	SourceMCVersion   string  `json:"source_mc_version"`
	PreImportBackupID *string `json:"pre_import_backup_id"`
}

func (d *DB) CreateWorldImport(ctx context.Context, w *WorldImport) error {
	w.ID, w.ImportedAt = newID(), now()
	_, err := d.ExecContext(ctx,
		`INSERT INTO world_imports (id, server_id, imported_at, imported_by, source_name, source_mc_version, pre_import_backup_id)
		 VALUES (?,?,?,?,?,?,?)`,
		w.ID, w.ServerID, w.ImportedAt, nullStr(w.ImportedBy), w.SourceName, w.SourceMCVersion, nullStr(w.PreImportBackupID))
	return err
}

func (d *DB) ListWorldImports(ctx context.Context, serverID string) ([]*WorldImport, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, server_id, imported_at, imported_by, source_name, source_mc_version, pre_import_backup_id
		 FROM world_imports WHERE server_id = ? ORDER BY imported_at DESC`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WorldImport{}
	for rows.Next() {
		var w WorldImport
		var by, bk sql.NullString
		if err := rows.Scan(&w.ID, &w.ServerID, &w.ImportedAt, &by, &w.SourceName, &w.SourceMCVersion, &bk); err != nil {
			return nil, err
		}
		w.ImportedBy, w.PreImportBackupID = strPtr(by), strPtr(bk)
		out = append(out, &w)
	}
	return out, rows.Err()
}

package db

import (
	"context"
	"database/sql"
)

const (
	StatusRunning  = "running"
	StatusStopped  = "stopped"
	StatusStarting = "starting"
	StatusStopping = "stopping"
	StatusCrashed  = "crashed"
)

type Server struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Directory         string  `json:"directory"`
	MCVersion         string  `json:"mc_version"`
	JavaVersion       string  `json:"java_version"`
	Port              int     `json:"port"`
	RCONPort          int     `json:"rcon_port"`
	RCONPasswordEnc   string  `json:"-"`
	MaxMemoryMB       int     `json:"max_memory_mb"`
	AutoRestart       bool    `json:"auto_restart"`
	CreatedAt         int64   `json:"created_at"`
	Status            string  `json:"status"`
	ConnectionAddress *string `json:"connection_address"`
}

const serverCols = `id, name, directory, mc_version, java_version, port, rcon_port, rcon_password,
	max_memory_mb, auto_restart, created_at, status, connection_address`

func scanServer(row interface{ Scan(...any) error }) (*Server, error) {
	var s Server
	var addr sql.NullString
	if err := row.Scan(&s.ID, &s.Name, &s.Directory, &s.MCVersion, &s.JavaVersion, &s.Port, &s.RCONPort,
		&s.RCONPasswordEnc, &s.MaxMemoryMB, &s.AutoRestart, &s.CreatedAt, &s.Status, &addr); err != nil {
		return nil, notFound(err)
	}
	s.ConnectionAddress = strPtr(addr)
	return &s, nil
}

// CreateServer inserts s. ID must be set by the caller (the directory is
// derived from it); CreatedAt is assigned here.
func (d *DB) CreateServer(ctx context.Context, s *Server) error {
	if s.ID == "" {
		s.ID = newID()
	}
	s.CreatedAt = now()
	if s.Status == "" {
		s.Status = StatusStopped
	}
	_, err := d.ExecContext(ctx, `INSERT INTO servers (`+serverCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.Directory, s.MCVersion, s.JavaVersion, s.Port, s.RCONPort, s.RCONPasswordEnc,
		s.MaxMemoryMB, s.AutoRestart, s.CreatedAt, s.Status, nullStr(s.ConnectionAddress))
	return err
}

func (d *DB) ServerByID(ctx context.Context, id string) (*Server, error) {
	return scanServer(d.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = ?`, id))
}

func (d *DB) ServerByName(ctx context.Context, name string) (*Server, error) {
	return scanServer(d.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE name = ?`, name))
}

func (d *DB) ListServers(ctx context.Context) ([]*Server, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+serverCols+` FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateServer writes the mutable configuration fields of s.
func (d *DB) UpdateServer(ctx context.Context, s *Server) error {
	return execOne(d.ExecContext(ctx,
		`UPDATE servers SET name = ?, mc_version = ?, java_version = ?, port = ?, rcon_port = ?,
		 max_memory_mb = ?, auto_restart = ?, connection_address = ? WHERE id = ?`,
		s.Name, s.MCVersion, s.JavaVersion, s.Port, s.RCONPort, s.MaxMemoryMB, s.AutoRestart,
		nullStr(s.ConnectionAddress), s.ID))
}

func (d *DB) SetServerStatus(ctx context.Context, id, status string) error {
	_, err := d.ExecContext(ctx, `UPDATE servers SET status = ? WHERE id = ?`, status, id)
	return err
}

func (d *DB) DeleteServer(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id))
}

type ServerEvent struct {
	ID        string  `json:"id"`
	ServerID  string  `json:"server_id"`
	Type      string  `json:"type"`
	Detail    *string `json:"detail"`
	CreatedAt int64   `json:"created_at"`
}

func (d *DB) AddServerEvent(ctx context.Context, serverID, typ string, detail *string) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO server_events (id, server_id, type, detail, created_at) VALUES (?,?,?,?,?)`,
		newID(), serverID, typ, nullStr(detail), now())
	return err
}

func (d *DB) ListServerEvents(ctx context.Context, serverID string, limit int) ([]*ServerEvent, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, server_id, type, detail, created_at FROM server_events
		 WHERE server_id = ? ORDER BY created_at DESC LIMIT ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ServerEvent
	for rows.Next() {
		var e ServerEvent
		var det sql.NullString
		if err := rows.Scan(&e.ID, &e.ServerID, &e.Type, &det, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Detail = strPtr(det)
		out = append(out, &e)
	}
	return out, rows.Err()
}

package db

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Webhook is a Discord webhook. URLEnc is the AES-GCM sealed URL; the
// plaintext URL never touches the DB.
type Webhook struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	URLEnc     string          `json:"-"`
	Events     map[string]bool `json:"events"`
	AllServers bool            `json:"all_servers"`
	ServerIDs  []string        `json:"server_ids"` // used when AllServers is false
	CreatedAt  int64           `json:"created_at"`
}

// AppliesTo reports whether the webhook should receive events for serverID.
func (w *Webhook) AppliesTo(serverID string) bool {
	if w.AllServers {
		return true
	}
	for _, id := range w.ServerIDs {
		if id == serverID {
			return true
		}
	}
	return false
}

func scanWebhook(row interface{ Scan(...any) error }) (*Webhook, error) {
	var w Webhook
	var ev string
	if err := row.Scan(&w.ID, &w.Name, &w.URLEnc, &ev, &w.AllServers, &w.CreatedAt); err != nil {
		return nil, notFound(err)
	}
	w.Events = map[string]bool{}
	json.Unmarshal([]byte(ev), &w.Events)
	return &w, nil
}

// CreateWebhook inserts w and its server scope in one transaction.
func (d *DB) CreateWebhook(ctx context.Context, w *Webhook) error {
	w.ID, w.CreatedAt = newID(), now()
	ev, _ := json.Marshal(w.Events)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO webhooks (id, name, url_enc, events_json, all_servers, created_at) VALUES (?,?,?,?,?,?)`,
		w.ID, w.Name, w.URLEnc, string(ev), w.AllServers, w.CreatedAt); err != nil {
		return err
	}
	if err := setWebhookServers(ctx, tx, w.ID, w.scope()); err != nil {
		return err
	}
	return tx.Commit()
}

// scope is the server list to persist (none when AllServers).
func (w *Webhook) scope() []string {
	if w.AllServers {
		return nil
	}
	return w.ServerIDs
}

// UpdateWebhook writes w's fields and replaces its server scope.
func (d *DB) UpdateWebhook(ctx context.Context, w *Webhook) error {
	ev, _ := json.Marshal(w.Events)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := execOne(tx.ExecContext(ctx, `UPDATE webhooks SET name = ?, url_enc = ?, events_json = ?, all_servers = ? WHERE id = ?`,
		w.Name, w.URLEnc, string(ev), w.AllServers, w.ID)); err != nil {
		return err
	}
	if err := setWebhookServers(ctx, tx, w.ID, w.scope()); err != nil {
		return err
	}
	return tx.Commit()
}

func setWebhookServers(ctx context.Context, tx *sql.Tx, webhookID string, serverIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_servers WHERE webhook_id = ?`, webhookID); err != nil {
		return err
	}
	for _, sid := range serverIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO webhook_servers (webhook_id, server_id) VALUES (?,?)`, webhookID, sid); err != nil {
			return err
		}
	}
	return nil
}

// loadServerIDs fills ServerIDs for the given webhooks.
func (d *DB) loadServerIDs(ctx context.Context, hooks ...*Webhook) error {
	byID := map[string]*Webhook{}
	for _, h := range hooks {
		h.ServerIDs = []string{}
		byID[h.ID] = h
	}
	rows, err := d.QueryContext(ctx, `SELECT webhook_id, server_id FROM webhook_servers ORDER BY server_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var wid, sid string
		if err := rows.Scan(&wid, &sid); err != nil {
			return err
		}
		if h, ok := byID[wid]; ok {
			h.ServerIDs = append(h.ServerIDs, sid)
		}
	}
	return rows.Err()
}

func (d *DB) WebhookByID(ctx context.Context, id string) (*Webhook, error) {
	w, err := scanWebhook(d.QueryRowContext(ctx, `SELECT id, name, url_enc, events_json, all_servers, created_at FROM webhooks WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return w, d.loadServerIDs(ctx, w)
}

func (d *DB) ListWebhooks(ctx context.Context) ([]*Webhook, error) {
	rows, err := d.QueryContext(ctx, `SELECT id, name, url_enc, events_json, all_servers, created_at FROM webhooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, d.loadServerIDs(ctx, out...)
}

func (d *DB) DeleteWebhook(ctx context.Context, id string) error {
	return execOne(d.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id))
}

func (d *DB) CountWebhooks(ctx context.Context) (int, error) {
	var n int
	err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks`).Scan(&n)
	return n, err
}

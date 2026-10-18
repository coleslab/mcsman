// Package discord posts server events to Discord webhooks.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mcsman/internal/config"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

// Event keys used in webhook configuration (match the spec's config keys).
var EventKeys = []string{
	"server_online", "server_offline", "server_crash", "restart_warning",
	"backup_failed", "upgrade_complete", "player_join", "player_leave", "backup_complete",
}

// DefaultEvents is the subscription for a new webhook.
var DefaultEvents = map[string]bool{
	"server_online": true, "server_offline": true, "server_crash": true, "restart_warning": true,
	"backup_failed": true, "upgrade_complete": true,
}

// Embed colours.
const (
	colorGreen   = 0x57F287
	colorGray    = 0x99AAB5
	colorRed     = 0xED4245
	colorYellow  = 0xFEE75C
	colorBlurple = 0x5865F2
)

// AllowedHosts restricts webhook URLs to Discord, so the daemon cannot be
// used to POST to arbitrary internal hosts. Tests override it.
var AllowedHosts = []string{"discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com"}

var ErrInvalidURL = errors.New("webhook URL must be https://discord.com/api/webhooks/...")

// ValidateURL checks that raw is a Discord webhook URL.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return ErrInvalidURL
	}
	host := u.Hostname()
	ok := false
	for _, h := range AllowedHosts {
		if host == h {
			ok = true
		}
	}
	if !ok || (u.Scheme != "https" && !(u.Scheme == "http" && isTestHost(host))) {
		return ErrInvalidURL
	}
	if !strings.HasPrefix(u.Path, "/api/webhooks/") && !isTestHost(host) {
		return ErrInvalidURL
	}
	return nil
}

func isTestHost(h string) bool { return h == "127.0.0.1" || h == "localhost" }

// Mask returns a webhook URL safe to display: only the host and the last
// four characters of the token.
func Mask(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "…"
	}
	tail := raw
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	return u.Scheme + "://" + u.Host + "/api/webhooks/…" + tail
}

// Notifier subscribes to the event bus and posts embeds.
type Notifier struct {
	DB      *db.DB
	Sealer  *crypto.Sealer // LabelWebhook
	Servers *server.Manager
	Bus     *events.Bus
	HTTP    *http.Client
	Log     *slog.Logger
}

type embed struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color"`
	Timestamp   string `json:"timestamp"`
}

type payload struct {
	Username string  `json:"username"`
	Embeds   []embed `json:"embeds"`
}

// Seed copies webhooks from config into the DB the first time the daemon
// runs with an empty webhooks table.
func (n *Notifier) Seed(ctx context.Context, hooks []config.Webhook) error {
	count, err := n.DB.CountWebhooks(ctx)
	if err != nil || count > 0 {
		return err
	}
	for _, h := range hooks {
		if h.URL == "" {
			continue
		}
		if err := ValidateURL(h.URL); err != nil {
			n.Log.Warn("skipping invalid webhook from config", "name", h.Name)
			continue
		}
		enc, err := n.Sealer.Seal(h.URL)
		if err != nil {
			return err
		}
		ev := map[string]bool{}
		for _, k := range EventKeys {
			ev[k] = h.Events[k]
		}
		var ids []string
		for _, name := range h.Servers {
			inst, err := n.Servers.Lookup(name)
			if err != nil {
				n.Log.Warn("webhook from config references unknown server", "webhook", h.Name, "server", name)
				continue
			}
			ids = append(ids, inst.Server().ID)
		}
		if len(h.Servers) > 0 && len(ids) == 0 {
			// Scoped to servers that don't exist (yet): don't silently widen
			// it to all servers.
			n.Log.Warn("skipping webhook from config: none of its servers exist", "webhook", h.Name)
			continue
		}
		hook := &db.Webhook{Name: h.Name, URLEnc: enc, Events: ev, AllServers: len(h.Servers) == 0, ServerIDs: ids}
		if err := n.DB.CreateWebhook(ctx, hook); err != nil {
			return err
		}
		n.Log.Info("seeded webhook from config", "name", h.Name)
	}
	return nil
}

// Run delivers events until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	ch, cancel := n.Bus.Subscribe(256)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			key, emb, ok := n.render(e)
			if !ok {
				continue
			}
			go n.deliver(ctx, e.ServerID, key, emb)
		}
	}
}

// render maps a bus event to a webhook event key and embed.
func (n *Notifier) render(e events.Event) (string, embed, bool) {
	ts := time.Unix(e.TS, 0).UTC().Format(time.RFC3339)
	str := func(k string) string {
		if m, ok := e.Data.(map[string]string); ok {
			return m[k]
		}
		if m, ok := e.Data.(map[string]any); ok {
			if v, ok := m[k].(string); ok {
				return v
			}
		}
		return ""
	}
	switch e.Type {
	case events.ServerOnline:
		return "server_online", embed{Title: "Server online", Description: n.connectLine(e.ServerID), Color: colorGreen, Timestamp: ts}, true
	case events.ServerOffline:
		return "server_offline", embed{Title: "Server offline", Description: "Stopped cleanly.", Color: colorGray, Timestamp: ts}, true
	case events.ServerCrash:
		desc := "The server stopped unexpectedly."
		if m, ok := e.Data.(map[string]any); ok {
			if r, _ := m["will_restart"].(bool); r {
				desc += " Restarting automatically."
			} else {
				desc += " **Not restarting** — check the console."
			}
		}
		return "server_crash", embed{Title: "Server crashed", Description: desc, Color: colorRed, Timestamp: ts}, true
	case events.RestartWarning:
		desc := "The server will restart shortly."
		if m, ok := e.Data.(map[string]int); ok {
			desc = fmt.Sprintf("Restarting in %d seconds.", m["delay_seconds"])
		}
		return "restart_warning", embed{Title: "Restart warning", Description: desc, Color: colorYellow, Timestamp: ts}, true
	case events.BackupFailed:
		return "backup_failed", embed{Title: "Backup failed", Description: truncate(str("error"), 500), Color: colorRed, Timestamp: ts}, true
	case events.BackupComplete:
		desc := ""
		if m, ok := e.Data.(map[string]any); ok {
			if sz, ok := m["size_bytes"].(int64); ok {
				desc = fmt.Sprintf("%.1f MB", float64(sz)/(1<<20))
			}
		}
		return "backup_complete", embed{Title: "Backup complete", Description: desc, Color: colorGreen, Timestamp: ts}, true
	case events.UpgradeComplete:
		if j, ok := e.Data.(interface{ Summary() (string, string, bool) }); ok {
			title, desc, success := j.Summary()
			c := colorBlurple
			if !success {
				c = colorRed
			}
			return "upgrade_complete", embed{Title: title, Description: truncate(desc, 1000), Color: c, Timestamp: ts}, true
		}
		return "upgrade_complete", embed{Title: "Upgrade complete", Color: colorBlurple, Timestamp: ts}, true
	case events.PlayerJoin:
		return "player_join", embed{Title: str("mc_username") + " joined", Color: colorGreen, Timestamp: ts}, true
	case events.PlayerLeave:
		return "player_leave", embed{Title: str("mc_username") + " left", Color: colorGray, Timestamp: ts}, true
	}
	return "", embed{}, false
}

func (n *Notifier) connectLine(serverID string) string {
	inst, err := n.Servers.Get(serverID)
	if err != nil {
		return ""
	}
	if a := inst.Server().ConnectionAddress; a != nil {
		return "Connect: `" + *a + "`"
	}
	return ""
}

func (n *Notifier) serverName(serverID string) string {
	inst, err := n.Servers.Get(serverID)
	if err != nil {
		return "mcsman"
	}
	return inst.Server().Name
}

func (n *Notifier) deliver(ctx context.Context, serverID, key string, e embed) {
	hooks, err := n.DB.ListWebhooks(ctx)
	if err != nil {
		n.Log.Error("load webhooks", "err", err)
		return
	}
	p := payload{Username: n.serverName(serverID), Embeds: []embed{e}}
	for _, h := range hooks {
		if !h.Events[key] || !h.AppliesTo(serverID) {
			continue
		}
		if err := n.send(ctx, h, p); err != nil {
			// Never log the URL; the webhook name identifies it.
			n.Log.Warn("discord webhook delivery failed", "webhook", h.Name, "event", key, "err", err)
		}
	}
}

// Test sends a test message through a webhook.
func (n *Notifier) Test(ctx context.Context, id string) error {
	h, err := n.DB.WebhookByID(ctx, id)
	if err != nil {
		return err
	}
	return n.send(ctx, h, payload{Username: "mcsman", Embeds: []embed{{
		Title: "Test notification", Description: "Webhook **" + h.Name + "** is working.",
		Color: colorBlurple, Timestamp: time.Now().UTC().Format(time.RFC3339),
	}}})
}

// send posts p, retrying on rate limits (429) up to three times.
func (n *Notifier) send(ctx context.Context, h *db.Webhook, p payload) error {
	raw, err := n.Sealer.Open(h.URLEnc)
	if err != nil {
		return errors.New("cannot decrypt webhook URL (was jwt_secret changed?)")
	}
	body, _ := json.Marshal(p)
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
		if err != nil {
			return errors.New("invalid webhook URL")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := n.HTTP.Do(req)
		if err != nil {
			return redact(err)
		}
		resp.Body.Close()
		switch {
		case resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := time.Second
			if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s > 0 && s < 60 {
				wait = time.Duration(s * float64(time.Second))
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("discord returned %s", resp.Status)
		}
	}
	return errors.New("rate limited by discord")
}

// redact strips the URL (which embeds the webhook token) from HTTP errors.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

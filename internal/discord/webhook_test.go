package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mcsman/internal/config"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

type fakeDiscord struct {
	srv      *httptest.Server
	mu       sync.Mutex
	got      []payload
	limitOne bool
	hits     int
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if f.limitOne && f.hits == 1 {
			w.Header().Set("Retry-After", "0.05")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		var p payload
		json.NewDecoder(r.Body).Decode(&p)
		f.got = append(f.got, p)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDiscord) payloads() []payload {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]payload(nil), f.got...)
}

func setup(t *testing.T) (*Notifier, *fakeDiscord, *bytes.Buffer, string) {
	t.Helper()
	old := AllowedHosts
	AllowedHosts = []string{"127.0.0.1"}
	t.Cleanup(func() { AllowedHosts = old })

	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rcon, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelWebhook)
	bus := events.NewBus()
	m := server.NewManager(server.Options{DB: d, Bus: bus, Sealer: rcon, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return "", nil }})
	addr := "survival.playit.gg"
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "survival", MCVersion: "1.21.1", Port: 30000,
		AcceptEULA: true, ConnectionAddress: &addr})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeDiscord(t)
	n := &Notifier{DB: d, Sealer: sealer, Servers: m, Bus: bus, HTTP: f.srv.Client(), Log: log}
	return n, f, &logs, srv.ID
}

const secretPath = "/api/webhooks/123/SECRET-TOKEN"

func TestDeliveryFiltersAndFormats(t *testing.T) {
	n, f, logs, sid := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := n.Seed(ctx, []config.Webhook{{Name: "status", URL: f.srv.URL + secretPath,
		Events: map[string]bool{"server_online": true, "server_crash": true}}}); err != nil {
		t.Fatal(err)
	}
	go n.Run(ctx)
	time.Sleep(50 * time.Millisecond)

	n.Bus.Publish(events.New(events.ServerOnline, sid, nil))
	n.Bus.Publish(events.New(events.PlayerJoin, sid, map[string]string{"mc_username": "Steve"})) // not subscribed
	n.Bus.Publish(events.New(events.ServerCrash, sid, map[string]any{"will_restart": false}))

	deadline := time.Now().Add(3 * time.Second)
	for len(f.payloads()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	got := f.payloads()
	if len(got) != 2 {
		t.Fatalf("got %d payloads: %+v", len(got), got)
	}
	colors := map[string]int{}
	for _, p := range got {
		if p.Username != "survival" {
			t.Errorf("username %q, want server name", p.Username)
		}
		colors[p.Embeds[0].Title] = p.Embeds[0].Color
		if p.Embeds[0].Title == "Server online" && !strings.Contains(p.Embeds[0].Description, "survival.playit.gg") {
			t.Errorf("online embed missing address: %q", p.Embeds[0].Description)
		}
	}
	if colors["Server online"] != 0x57F287 || colors["Server crashed"] != 0xED4245 {
		t.Errorf("colors %v", colors)
	}
	if strings.Contains(logs.String(), "SECRET-TOKEN") {
		t.Error("webhook token appeared in logs")
	}
}

func TestRetriesOn429AndRedactsErrors(t *testing.T) {
	n, f, logs, _ := setup(t)
	ctx := context.Background()
	f.limitOne = true
	enc, _ := n.Sealer.Seal(f.srv.URL + secretPath)
	h := &db.Webhook{Name: "h", URLEnc: enc, Events: DefaultEvents, AllServers: true}
	n.DB.CreateWebhook(ctx, h)
	if err := n.Test(ctx, h.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.payloads()) != 1 || f.hits != 2 {
		t.Errorf("hits=%d payloads=%d", f.hits, len(f.payloads()))
	}

	// A connection error must not leak the token.
	dead, _ := n.Sealer.Seal("http://127.0.0.1:1" + secretPath)
	h2 := &db.Webhook{Name: "dead", URLEnc: dead, Events: DefaultEvents, AllServers: true}
	n.DB.CreateWebhook(ctx, h2)
	err := n.Test(ctx, h2.ID)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
	n.deliver(ctx, "", "server_online", embed{Title: "x"})
	if strings.Contains(logs.String(), "SECRET") {
		t.Error("token leaked into logs")
	}
	// Stored ciphertext must not contain the plaintext.
	stored, _ := n.DB.WebhookByID(ctx, h.ID)
	if strings.Contains(stored.URLEnc, "SECRET") {
		t.Error("URL stored in plaintext")
	}
}

func TestValidateURLAndMask(t *testing.T) {
	cases := map[string]bool{
		"https://discord.com/api/webhooks/1/abc":        true,
		"https://discordapp.com/api/webhooks/1/abc":     true,
		"http://discord.com/api/webhooks/1/abc":         false,
		"https://evil.com/api/webhooks/1/abc":           false,
		"https://discord.com.evil.com/api/webhooks/1/a": false,
		"https://discord.com/other":                     false,
		"https://user:pw@discord.com/api/webhooks/1/a":  false,
		"http://169.254.169.254/latest":                 false,
	}
	for u, ok := range cases {
		if got := ValidateURL(u) == nil; got != ok {
			t.Errorf("ValidateURL(%s) = %v, want %v", u, got, ok)
		}
	}
	m := Mask("https://discord.com/api/webhooks/123/SECRET-TOKEN-wxyz")
	if strings.Contains(m, "SECRET") || !strings.HasSuffix(m, "wxyz") {
		t.Errorf("mask %q", m)
	}
}

func TestSeedOnlyWhenEmpty(t *testing.T) {
	n, f, _, _ := setup(t)
	ctx := context.Background()
	hooks := []config.Webhook{{Name: "a", URL: f.srv.URL + secretPath, Events: map[string]bool{"server_online": true}}}
	n.Seed(ctx, hooks)
	n.Seed(ctx, hooks)
	list, _ := n.DB.ListWebhooks(ctx)
	if len(list) != 1 {
		t.Errorf("seeded %d", len(list))
	}
}

func TestPerServerScoping(t *testing.T) {
	n, f, _, survivalID := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creative, err := n.Servers.Create(ctx, server.CreateParams{Name: "creative", MCVersion: "1.21.1", Port: 30100, AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	// Seed: one scoped by name to creative, one for all servers, one
	// scoped to a server that doesn't exist (skipped, not widened).
	err = n.Seed(ctx, []config.Webhook{
		{Name: "creative-only", URL: f.srv.URL + secretPath + "/c", Events: map[string]bool{"server_online": true}, Servers: []string{"creative"}},
		{Name: "everything", URL: f.srv.URL + secretPath + "/all", Events: map[string]bool{"server_online": true}},
		{Name: "ghost", URL: f.srv.URL + secretPath + "/g", Events: map[string]bool{"server_online": true}, Servers: []string{"nope"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks, _ := n.DB.ListWebhooks(ctx)
	if len(hooks) != 2 {
		t.Fatalf("seeded %d webhooks, want 2 (ghost skipped)", len(hooks))
	}

	go n.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	n.Bus.Publish(events.New(events.ServerOnline, survivalID, nil))
	n.Bus.Publish(events.New(events.ServerOnline, creative.ID, nil))

	deadline := time.Now().Add(3 * time.Second)
	for len(f.payloads()) < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	counts := map[string]int{}
	for _, p := range f.payloads() {
		counts[p.Username]++
	}
	// survival → "everything" only; creative → both webhooks.
	if counts["survival"] != 1 || counts["creative"] != 2 {
		t.Errorf("deliveries per server: %v", counts)
	}
}

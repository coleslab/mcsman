package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mcsman/internal/auth"
	"mcsman/internal/config"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
	"mcsman/internal/testutil"
)

const secret = "0123456789abcdef0123456789abcdef"

func TestMain(m *testing.M) {
	auth.UseMinCostForTesting()
	os.Exit(m.Run())
}

type env struct {
	t       *testing.T
	tcp     *httptest.Server
	sock    http.Handler
	db      *db.DB
	servers *server.Manager
}

func newEnv(t *testing.T, trusted ...string) *env {
	t.Helper()
	fake := testutil.FakeMC(t)
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer(secret, crypto.LabelRCON)
	m := server.NewManager(server.Options{
		DB: d, Bus: events.NewBus(), Sealer: sealer, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return fake, nil },
	})
	cfg := &config.Config{}
	cfg.HTTP.TrustedProxies = trusted
	a := New(cfg, d, auth.NewService(d, secret, time.Minute, time.Hour), m, log)
	e := &env{t: t, tcp: httptest.NewServer(a.Handler()), sock: a.SocketHandler(), db: d, servers: m}
	t.Cleanup(func() {
		e.tcp.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.Shutdown(ctx)
		d.Close()
	})
	return e
}

// call makes a TCP request with an optional bearer token.
func (e *env) call(method, path, token string, body any, hdr ...string) (*http.Response, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.tcp.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	json.Unmarshal(raw, &out)
	return resp, out
}

// socketCall makes a request through the Unix-socket handler.
func (e *env) socketCall(method, path string, body any) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	e.sock.ServeHTTP(rec, req)
	return rec
}

func (e *env) initAdmin() {
	e.t.Helper()
	if rec := e.socketCall("POST", "/api/init", map[string]string{"username": "admin", "password": "password123"}); rec.Code != 201 {
		e.t.Fatalf("init: %d %s", rec.Code, rec.Body)
	}
}

func (e *env) login(user, pw string) (string, string) {
	e.t.Helper()
	resp, out := e.call("POST", "/api/auth/login", "", map[string]string{"username": user, "password": pw})
	if resp.StatusCode != 200 {
		e.t.Fatalf("login %s: %d %v", user, resp.StatusCode, out)
	}
	return out["access_token"].(string), out["refresh_token"].(string)
}

func TestInitOnlyViaSocketAndOnce(t *testing.T) {
	e := newEnv(t)
	if resp, _ := e.call("POST", "/api/init", "", map[string]string{"username": "x", "password": "password123"}); resp.StatusCode != 404 {
		t.Errorf("init over TCP: %d", resp.StatusCode)
	}
	e.initAdmin()
	if rec := e.socketCall("POST", "/api/init", map[string]string{"username": "y", "password": "password123"}); rec.Code != 409 {
		t.Errorf("second init: %d", rec.Code)
	}
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	access, refresh := e.login("admin", "password123")

	if resp, out := e.call("GET", "/api/auth/me", access, nil); resp.StatusCode != 200 || out["role"] != "admin" {
		t.Fatalf("me: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.call("GET", "/api/auth/me", "", nil); resp.StatusCode != 401 {
		t.Errorf("no token: %d", resp.StatusCode)
	}

	resp, out := e.call("POST", "/api/auth/refresh", "", map[string]string{"refresh_token": refresh})
	if resp.StatusCode != 200 {
		t.Fatalf("refresh: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/auth/refresh", "", map[string]string{"refresh_token": refresh}); resp.StatusCode != 401 {
		t.Errorf("reused refresh: %d", resp.StatusCode)
	}
	access2 := out["access_token"].(string)
	if resp, _ := e.call("POST", "/api/auth/logout", access2, nil); resp.StatusCode != 204 {
		t.Errorf("logout: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/auth/me", access2, nil); resp.StatusCode != 401 {
		t.Errorf("after logout: %d", resp.StatusCode)
	}
}

func TestMeDoesNotLeakHash(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	access, _ := e.login("admin", "password123")
	_, out := e.call("GET", "/api/auth/me", access, nil)
	if _, ok := out["password_hash"]; ok {
		t.Error("password hash exposed")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	var last int
	for i := 0; i < 11; i++ {
		resp, _ := e.call("POST", "/api/auth/login", "", map[string]string{"username": "x", "password": "wrongpass"})
		last = resp.StatusCode
	}
	if last != 429 {
		t.Errorf("11th login: %d, want 429", last)
	}
}

func TestTrustedProxyXFF(t *testing.T) {
	e := newEnv(t, "127.0.0.1")
	// Each distinct forwarded client gets its own bucket.
	for i := 0; i < 11; i++ {
		resp, _ := e.call("POST", "/api/auth/login", "", map[string]string{"username": "x", "password": "wrongpass"},
			"X-Forwarded-For", "203.0.113.1")
		if i == 10 && resp.StatusCode != 429 {
			t.Errorf("client A 11th: %d", resp.StatusCode)
		}
	}
	resp, _ := e.call("POST", "/api/auth/login", "", map[string]string{"username": "x", "password": "wrongpass"},
		"X-Forwarded-For", "203.0.113.2")
	if resp.StatusCode == 429 {
		t.Error("client B limited by client A's bucket")
	}
}

func TestUntrustedXFFIgnored(t *testing.T) {
	e := newEnv(t) // no trusted proxies
	for i := 0; i < 10; i++ {
		e.call("POST", "/api/auth/login", "", map[string]string{"username": "x", "password": "wrongpass"},
			"X-Forwarded-For", "203.0.113."+string(rune('1'+i)))
	}
	resp, _ := e.call("POST", "/api/auth/login", "", map[string]string{"username": "x", "password": "wrongpass"},
		"X-Forwarded-For", "198.51.100.9")
	if resp.StatusCode != 429 {
		t.Errorf("spoofed XFF bypassed rate limit: %d", resp.StatusCode)
	}
}

func TestPlayerRestrictions(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	h, _ := auth.HashPassword("password123")
	e.db.CreateUser(context.Background(), &db.User{Username: "steve", PasswordHash: h, Role: db.RolePlayer})
	admin, _ := e.login("admin", "password123")
	player, _ := e.login("steve", "password123")

	resp, out := e.call("POST", "/api/servers", admin, map[string]any{
		"name": "survival", "mc_version": "1.21.1", "port": testutil.FreePort(t), "rcon_port": testutil.FreePort(t), "accept_eula": true,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)
	if _, ok := out["rcon_password"]; ok {
		t.Error("rcon password exposed")
	}

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/servers"}, {"POST", "/api/servers/" + id + "/start"},
		{"POST", "/api/servers/" + id + "/command"}, {"DELETE", "/api/servers/" + id},
	} {
		if resp, _ := e.call(c.method, c.path, player, map[string]string{}); resp.StatusCode != 403 {
			t.Errorf("player %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	resp, out = e.call("GET", "/api/servers/"+id, player, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("player get: %d", resp.StatusCode)
	}
	if _, ok := out["directory"]; ok {
		t.Error("player view exposes directory")
	}
	if _, ok := out["connection_address"]; !ok {
		t.Error("player view missing connection_address")
	}
}

func TestServerLifecycleAndConsole(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	_, out := e.call("POST", "/api/servers", admin, map[string]any{
		"name": "survival", "mc_version": "1.21.1", "port": testutil.FreePort(t), "rcon_port": testutil.FreePort(t), "accept_eula": true,
	})
	id := out["id"].(string)
	os.WriteFile(filepath.Join(out["directory"].(string), "server.jar"), []byte("x"), 0o640)

	if resp, out := e.call("POST", "/api/servers/survival/start", admin, nil); resp.StatusCode != 200 {
		t.Fatalf("start by name: %d %v", resp.StatusCode, out)
	}
	inst, _ := e.servers.Get(id)
	deadline := time.Now().Add(5 * time.Second)
	for inst.Status() != db.StatusRunning && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if resp, out := e.call("POST", "/api/servers/"+id+"/command", admin, map[string]string{"command": "/list"}); resp.StatusCode != 200 ||
		!strings.Contains(out["output"].(string), "players online") {
		t.Fatalf("command: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.call("POST", "/api/servers/"+id+"/command", admin, map[string]string{"command": "say a\nop me"}); resp.StatusCode != 400 {
		t.Errorf("multi-line command: %d", resp.StatusCode)
	}

	// Console WebSocket: no ticket → 401; with ticket → replay + live input.
	wsURL := "ws" + strings.TrimPrefix(e.tcp.URL, "http") + "/api/servers/" + id + "/console"
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil || resp.StatusCode != 401 {
		t.Errorf("ws without ticket should be 401")
	}
	_, tout := e.call("POST", "/api/ws-ticket", admin, nil)
	ticket := tout["ticket"].(string)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL+"?ticket="+ticket, nil); err == nil || resp.StatusCode != 401 {
		t.Error("ticket reusable")
	}
	conn.WriteJSON(map[string]string{"type": "input", "data": "say from-ws\n"})
	sawReplay, sawEcho := false, false
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !(sawReplay && sawEcho) {
		var ev events.Event
		if err := conn.ReadJSON(&ev); err != nil {
			t.Fatalf("ws read: %v (replay=%v echo=%v)", err, sawReplay, sawEcho)
		}
		if ev.Type != events.ConsoleOutput {
			continue
		}
		line := ev.Data.(map[string]any)["line"].(string)
		sawReplay = sawReplay || strings.Contains(line, "Done (")
		sawEcho = sawEcho || strings.Contains(line, "[Server] from-ws")
	}

	if resp, _ := e.call("POST", "/api/servers/"+id+"/stop?wait=true", admin, map[string]int{"delay_seconds": 0}); resp.StatusCode != 200 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	if inst.Status() != db.StatusStopped {
		t.Errorf("status = %s", inst.Status())
	}

	if resp, _ := e.call("DELETE", "/api/servers/"+id, admin, map[string]string{"confirm": "wrong"}); resp.StatusCode != 400 {
		t.Errorf("delete without confirm: %d", resp.StatusCode)
	}
	if resp, _ := e.call("DELETE", "/api/servers/"+id, admin, map[string]string{"confirm": "survival"}); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
}

func TestSocketListenerAcceptsOwner(t *testing.T) {
	e := newEnv(t)
	dir, err := os.MkdirTemp("", "mcsman-sock-") // short path: sun_path is limited to 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	ln, err := ListenSocket(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.sock}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o660 {
		t.Errorf("socket perms %#o", fi.Mode().Perm())
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("unix", path)
	}}}
	resp, err := c.Get("http://x/api/servers")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("socket list servers: %d", resp.StatusCode)
	}
	if _, err := ListenSocket(path, slog.Default()); err == nil {
		t.Error("second listener on live socket should fail")
	}
}

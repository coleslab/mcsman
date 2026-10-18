package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcsman/internal/testutil"
)

func (e *env) createServer(t *testing.T, admin, name string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"name": name, "mc_version": "1.21.1", "port": testutil.FreePort(t),
		"rcon_port": testutil.FreePort(t), "accept_eula": true}
	for k, v := range extra {
		body[k] = v
	}
	resp, out := e.call("POST", "/api/servers", admin, body)
	if resp.StatusCode != 201 {
		t.Fatalf("create server: %d %v", resp.StatusCode, out)
	}
	os.WriteFile(filepath.Join(out["directory"].(string), "server.jar"), []byte("x"), 0o640)
	return out
}

func TestInviteFlowWithWhitelist(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", map[string]any{"connection_address": "survival.playit.gg"})

	// Admin creates a whitelist invite (by server name).
	resp, inv := e.call("POST", "/api/invites", admin, map[string]any{"expires_in": "2d", "whitelist_server_id": "survival"})
	if resp.StatusCode != 201 {
		t.Fatalf("create invite: %d %v", resp.StatusCode, inv)
	}
	token := inv["token"].(string)
	if !strings.HasPrefix(inv["url"].(string), "https://mc.example.com/join/") {
		t.Errorf("url %v", inv["url"])
	}
	if exp := int64(inv["expires_at"].(float64)); exp < time.Now().Add(47*time.Hour).Unix() {
		t.Errorf("expires_at too soon: %d", exp)
	}

	// Public lookup tells the join page a Minecraft name is required.
	resp, look := e.call("GET", "/api/invites/"+token, "", nil)
	if resp.StatusCode != 200 || look["requires_mc_username"] != true || look["whitelist_server_name"] != "survival" {
		t.Fatalf("lookup: %d %v", resp.StatusCode, look)
	}

	// Redeem while the server is stopped → queued.
	resp, red := e.call("POST", "/api/invites/"+token+"/redeem", "", map[string]string{
		"username": "steve", "password": "password123", "mc_username": "Steve",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("redeem: %d %v", resp.StatusCode, red)
	}
	ws := red["whitelisted_server"].(map[string]any)
	if ws["name"] != "survival" || ws["connection_address"] != "survival.playit.gg" || ws["queued"] != true {
		t.Errorf("whitelisted_server %v", ws)
	}
	player := red["access_token"].(string)
	if resp, me := e.call("GET", "/api/auth/me", player, nil); resp.StatusCode != 200 || me["role"] != "player" {
		t.Errorf("new user: %d %v", resp.StatusCode, me)
	}

	// Token is single-use.
	if resp, _ := e.call("POST", "/api/invites/"+token+"/redeem", "", map[string]string{
		"username": "alex", "password": "password123", "mc_username": "Alex",
	}); resp.StatusCode != 410 {
		t.Errorf("reuse: %d", resp.StatusCode)
	}
	pending, _ := e.db.PendingWhitelist(t.Context(), srv["id"].(string))
	if len(pending) != 1 || pending[0].MCUsername != "Steve" {
		t.Errorf("queue %v", pending)
	}
}

func TestSiteOnlyInviteAndRevoke(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	_, inv := e.call("POST", "/api/invites", admin, nil)
	resp, red := e.call("POST", "/api/invites/"+inv["token"].(string)+"/redeem", "", map[string]string{
		"username": "bob", "password": "password123",
	})
	if resp.StatusCode != 201 || red["whitelisted_server"] != nil {
		t.Fatalf("site-only redeem: %d %v", resp.StatusCode, red)
	}

	_, inv2 := e.call("POST", "/api/invites", admin, map[string]any{"expires_in": "1h"})
	req, _ := http.NewRequest("GET", e.tcp.URL+"/api/invites", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	lresp, _ := http.DefaultClient.Do(req)
	var list []map[string]any
	json.NewDecoder(lresp.Body).Decode(&list)
	lresp.Body.Close()
	if len(list) != 1 || list[0]["id"] != inv2["id"] {
		t.Fatalf("pending list: %v", list)
	}
	if _, ok := list[0]["token_hash"]; ok {
		t.Error("token hash exposed")
	}
	if resp, _ := e.call("DELETE", "/api/invites/"+inv2["id"].(string), admin, nil); resp.StatusCode != 204 {
		t.Errorf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/invites/"+inv2["token"].(string), "", nil); resp.StatusCode != 410 {
		t.Errorf("revoked lookup: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/invites", admin, map[string]any{"expires_in": "soon"}); resp.StatusCode != 400 {
		t.Errorf("bad expiry: %d", resp.StatusCode)
	}
}

func TestRedeemRateLimit(t *testing.T) {
	e := newEnv(t)
	var last int
	for i := 0; i < 6; i++ {
		resp, _ := e.call("POST", "/api/invites/bogus/redeem", "", map[string]string{"username": "x", "password": "password123"})
		last = resp.StatusCode
	}
	if last != 429 {
		t.Errorf("6th redeem: %d, want 429", last)
	}
}

func TestPublicStatus(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	e.createServer(t, admin, "creative", nil)
	e.createServer(t, admin, "anarchy", map[string]any{"connection_address": "a.example.net"})
	resp, out := e.call("GET", "/api/status", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	servers := out["servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("servers %v", servers)
	}
	first := servers[0].(map[string]any)
	if first["name"] != "anarchy" || first["connection_address"] != "a.example.net" || first["status"] != "stopped" {
		t.Errorf("first %v", first)
	}
	for _, k := range []string{"directory", "rcon_port", "id", "port"} {
		if _, ok := first[k]; ok {
			t.Errorf("public status exposes %s", k)
		}
	}
}

func TestUserManagement(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	_, inv := e.call("POST", "/api/invites", admin, nil)
	_, red := e.call("POST", "/api/invites/"+inv["token"].(string)+"/redeem", "", map[string]string{"username": "steve", "password": "password123"})
	player := red["access_token"].(string)

	if resp, _ := e.call("GET", "/api/users", player, nil); resp.StatusCode != 403 {
		t.Errorf("player list users: %d", resp.StatusCode)
	}
	_, me := e.call("GET", "/api/auth/me", admin, nil)
	adminID := me["id"].(string)

	// Last admin protections.
	if resp, _ := e.call("PATCH", "/api/users/"+adminID, admin, map[string]string{"role": "player"}); resp.StatusCode != 409 {
		t.Errorf("demote last admin: %d", resp.StatusCode)
	}
	if resp, _ := e.call("DELETE", "/api/users/"+adminID, admin, nil); resp.StatusCode != 409 {
		t.Errorf("delete self: %d", resp.StatusCode)
	}

	// Promote by username, then the promoted user can act as admin immediately.
	if resp, out := e.call("PATCH", "/api/users/steve", admin, map[string]string{"role": "admin"}); resp.StatusCode != 200 || out["role"] != "admin" {
		t.Fatalf("promote: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.call("GET", "/api/users", player, nil); resp.StatusCode != 200 {
		t.Errorf("promoted user (existing token) list users: %d", resp.StatusCode)
	}
	// Now the original admin can be demoted.
	if resp, _ := e.call("PATCH", "/api/users/"+adminID, player, map[string]string{"role": "player"}); resp.StatusCode != 200 {
		t.Errorf("demote with another admin: %d", resp.StatusCode)
	}
	if resp, _ := e.call("DELETE", "/api/users/admin", player, nil); resp.StatusCode != 204 {
		t.Errorf("delete user: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/auth/me", admin, nil); resp.StatusCode != 401 {
		t.Errorf("deleted user's token still valid: %d", resp.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	a1, _ := e.login("admin", "password123")
	a2, _ := e.login("admin", "password123")
	if resp, _ := e.call("POST", "/api/users/me/password", a1, map[string]string{"current": "wrong", "new": "newpassword1"}); resp.StatusCode != 403 {
		t.Errorf("wrong current: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/users/me/password", a1, map[string]string{"current": "password123", "new": "short"}); resp.StatusCode != 400 {
		t.Errorf("weak new: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/users/me/password", a1, map[string]string{"current": "password123", "new": "newpassword1"}); resp.StatusCode != 204 {
		t.Fatalf("change: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/auth/me", a1, nil); resp.StatusCode != 200 {
		t.Error("current session signed out")
	}
	if resp, _ := e.call("GET", "/api/auth/me", a2, nil); resp.StatusCode != 401 {
		t.Error("other session not signed out")
	}
	e.login("admin", "newpassword1")
}

func TestPluginEndpointsAccess(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	id := srv["id"].(string)
	_, inv := e.call("POST", "/api/invites", admin, nil)
	_, red := e.call("POST", "/api/invites/"+inv["token"].(string)+"/redeem", "", map[string]string{"username": "p", "password": "password123"})
	player := red["access_token"].(string)

	if resp, _ := e.call("GET", "/api/servers/"+id+"/plugins", player, nil); resp.StatusCode != 200 {
		t.Errorf("player list plugins: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/servers/"+id+"/plugins", player, map[string]string{"source": "modrinth", "source_id": "x"}); resp.StatusCode != 403 {
		t.Errorf("player install: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/servers/"+id+"/plugins", admin, map[string]string{"source": "nope", "source_id": "x"}); resp.StatusCode != 400 {
		t.Errorf("unknown source: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/servers/"+id+"/upgrade", player, map[string]string{"target_version": "1.21.4"}); resp.StatusCode != 403 {
		t.Errorf("player preflight: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/plugins/search", admin, nil); resp.StatusCode != 400 {
		t.Errorf("search without q: %d", resp.StatusCode)
	}
}

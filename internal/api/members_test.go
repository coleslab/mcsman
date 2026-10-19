package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"mcsman/internal/auth"
	"mcsman/internal/db"
	"mcsman/internal/events"
)

// makeMember creates player username (Minecraft name mc, if not taken
// already) and adds them to server id.
func (e *env) makeMember(t *testing.T, admin, id, username, mc string) {
	t.Helper()
	if _, err := e.db.UserByUsername(context.Background(), username); err != nil {
		h, _ := auth.HashPassword("password123")
		e.db.CreateUser(context.Background(), &db.User{Username: username, MCUsername: &mc, PasswordHash: h, Role: db.RolePlayer})
	} else {
		e.call("PATCH", "/api/users/"+username, admin, map[string]string{"mc_username": mc})
	}
	if resp, out := e.call("POST", "/api/servers/"+id+"/members", admin, map[string]string{"user_id": username}); resp.StatusCode != 201 {
		t.Fatalf("add member: %d %v", resp.StatusCode, out)
	}
}

func (e *env) player(t *testing.T, username string, mc *string) string {
	t.Helper()
	h, _ := auth.HashPassword("password123")
	if err := e.db.CreateUser(context.Background(), &db.User{Username: username, MCUsername: mc, PasswordHash: h, Role: db.RolePlayer}); err != nil {
		t.Fatal(err)
	}
	tok, _ := e.login(username, "password123")
	return tok
}

func (e *env) list(t *testing.T, path, token string) []map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", e.tcp.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	var out []map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func pending(t *testing.T, e *env, serverID string) []string {
	t.Helper()
	p, err := e.db.PendingWhitelist(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range p {
		out = append(out, x.Action+" "+x.MCUsername)
	}
	return out
}

func TestMembership(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	survival := e.createServer(t, admin, "survival", nil)["id"].(string)
	creative := e.createServer(t, admin, "creative", nil)["id"].(string)
	steveMC := "Steve"
	steve := e.player(t, "steve", &steveMC)
	e.player(t, "alex", nil)

	// A user without a Minecraft name can't be whitelisted.
	if resp, _ := e.call("POST", "/api/servers/survival/members", admin, map[string]string{"user_id": "alex"}); resp.StatusCode != 400 {
		t.Errorf("add member without mc name: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/servers/survival/members", admin, map[string]string{"user_id": "nobody"}); resp.StatusCode != 400 {
		t.Errorf("add unknown user: %d", resp.StatusCode)
	}
	resp, out := e.call("POST", "/api/servers/survival/members", admin, map[string]string{"user_id": "steve"})
	if resp.StatusCode != 201 || out["queued"] != true {
		t.Fatalf("add member: %d %v", resp.StatusCode, out)
	}
	if got := pending(t, e, survival); len(got) != 1 || got[0] != "add Steve" {
		t.Errorf("queue after add: %v", got)
	}
	members := e.list(t, "/api/servers/survival/members", admin)
	if len(members) != 1 || members[0]["username"] != "steve" || members[0]["mc_username"] != "Steve" {
		t.Errorf("members: %v", members)
	}
	if resp, _ := e.call("GET", "/api/servers/survival/members", steve, nil); resp.StatusCode != 403 {
		t.Errorf("player list members: %d", resp.StatusCode)
	}

	// Players see their own servers only; others are 404.
	mine := e.list(t, "/api/servers", steve)
	if len(mine) != 1 || mine[0]["id"] != survival {
		t.Errorf("player server list: %v", mine)
	}
	if _, ok := mine[0]["directory"]; ok {
		t.Error("player list exposes directory")
	}
	for _, p := range []string{"", "/players", "/plugins", "/events"} {
		if resp, _ := e.call("GET", "/api/servers/"+survival+p, steve, nil); resp.StatusCode != 200 {
			t.Errorf("member GET %s: %d", p, resp.StatusCode)
		}
		if resp, _ := e.call("GET", "/api/servers/"+creative+p, steve, nil); resp.StatusCode != 404 {
			t.Errorf("non-member GET %s: %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/api/servers/survival/backups", "/api/servers/survival/worlds/imports", "/api/versions/paper", "/api/plugins/search?q=x"} {
		if resp, _ := e.call("GET", p, steve, nil); resp.StatusCode != 403 {
			t.Errorf("player GET %s: %d", p, resp.StatusCode)
		}
	}

	// Members see event history without details.
	detail := `{"error":"/srv/secret/path"}`
	e.db.AddServerEvent(context.Background(), survival, "backup", &detail)
	evs := e.list(t, "/api/servers/survival/events", steve)
	if len(evs) != 1 || evs[0]["type"] != "backup" || evs[0]["detail"] != nil {
		t.Errorf("member events: %v", evs)
	}
	if evs := e.list(t, "/api/servers/survival/events", admin); evs[0]["detail"] == nil {
		t.Error("admin lost event detail")
	}

	// Removing supersedes the pending add with a pending remove.
	if resp, _ := e.call("DELETE", "/api/servers/survival/members/steve", admin, nil); resp.StatusCode != 204 {
		t.Fatalf("remove member: %d", resp.StatusCode)
	}
	if got := pending(t, e, survival); len(got) != 1 || got[0] != "remove Steve" {
		t.Errorf("queue after remove: %v", got)
	}
	if resp, _ := e.call("GET", "/api/servers/"+survival, steve, nil); resp.StatusCode != 404 {
		t.Errorf("removed member GET: %d", resp.StatusCode)
	}
	if resp, _ := e.call("DELETE", "/api/servers/survival/members/steve", admin, nil); resp.StatusCode != 404 {
		t.Errorf("remove non-member: %d", resp.StatusCode)
	}

	// Deleting a user un-whitelists them.
	e.makeMember(t, admin, creative, "steve", "Steve")
	if resp, _ := e.call("DELETE", "/api/users/steve", admin, nil); resp.StatusCode != 204 {
		t.Fatalf("delete user: %d", resp.StatusCode)
	}
	if got := pending(t, e, creative); len(got) != 1 || got[0] != "remove Steve" {
		t.Errorf("queue after user delete: %v", got)
	}
}

func TestAcceptInviteAsExistingUser(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	survival := e.createServer(t, admin, "survival", map[string]any{"connection_address": "s.example.net"})["id"].(string)
	steve := e.player(t, "steve", nil)

	_, inv := e.call("POST", "/api/invites", admin, map[string]any{"whitelist_server_id": "survival"})
	token := inv["token"].(string)

	if resp, _ := e.call("POST", "/api/invites/"+token+"/accept", "", nil); resp.StatusCode != 401 {
		t.Errorf("anonymous accept: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/invites/"+token+"/accept", steve, nil); resp.StatusCode != 400 {
		t.Errorf("accept without mc name: %d", resp.StatusCode)
	}
	resp, out := e.call("POST", "/api/invites/"+token+"/accept", steve, map[string]string{"mc_username": "Steve"})
	if resp.StatusCode != 200 {
		t.Fatalf("accept: %d %v", resp.StatusCode, out)
	}
	ws := out["whitelisted_server"].(map[string]any)
	if ws["name"] != "survival" || ws["connection_address"] != "s.example.net" || ws["queued"] != true {
		t.Errorf("whitelisted_server %v", ws)
	}
	if _, me := e.call("GET", "/api/auth/me", steve, nil); me["mc_username"] != "Steve" {
		t.Errorf("mc name not saved: %v", me)
	}
	if got := pending(t, e, survival); len(got) != 1 || got[0] != "add Steve" {
		t.Errorf("queue: %v", got)
	}
	if resp, _ := e.call("GET", "/api/servers/"+survival, steve, nil); resp.StatusCode != 200 {
		t.Errorf("member GET after accept: %d", resp.StatusCode)
	}
	if resp, _ := e.call("POST", "/api/invites/"+token+"/accept", steve, nil); resp.StatusCode != 410 {
		t.Errorf("reuse: %d", resp.StatusCode)
	}

	// Site-only invites have nothing to give an existing account.
	_, site := e.call("POST", "/api/invites", admin, nil)
	if resp, _ := e.call("POST", "/api/invites/"+site["token"].(string)+"/accept", steve, nil); resp.StatusCode != 409 {
		t.Errorf("accept site-only invite: %d", resp.StatusCode)
	}
	if resp, _ := e.call("GET", "/api/invites/"+site["token"].(string), "", nil); resp.StatusCode != 200 {
		t.Errorf("site-only invite consumed by failed accept: %d", resp.StatusCode)
	}
}

func TestRedeemMakesMember(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	survival := e.createServer(t, admin, "survival", nil)["id"].(string)
	_, inv := e.call("POST", "/api/invites", admin, map[string]any{"whitelist_server_id": "survival"})
	_, red := e.call("POST", "/api/invites/"+inv["token"].(string)+"/redeem", "", map[string]string{
		"username": "steve", "password": "password123", "mc_username": "Steve",
	})
	player := red["access_token"].(string)
	if resp, _ := e.call("GET", "/api/servers/"+survival, player, nil); resp.StatusCode != 200 {
		t.Errorf("redeemed user GET: %d", resp.StatusCode)
	}
}

func waitForCommand(t *testing.T, dir, cmd string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(filepath.Join(dir, "fakemc.commands"))
		if strings.Contains(string(raw), cmd+"\n") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never received %q; got:\n%s", cmd, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMembershipLiveWhitelistAndEvents(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	id, dir := srv["id"].(string), srv["directory"].(string)
	e.createServer(t, admin, "creative", nil)
	steveMC := "Steve"
	steve := e.player(t, "steve", &steveMC)
	outsider := e.player(t, "alex", nil)

	// alex watches the events socket but belongs to no server.
	_, tout := e.call("POST", "/api/ws-ticket", outsider, nil)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.tcp.URL, "http")+"/api/events?ticket="+tout["ticket"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	e.call("POST", "/api/servers/"+id+"/start", admin, nil)
	inst, _ := e.servers.Get(id)
	deadline := time.Now().Add(5 * time.Second)
	for inst.Status() != db.StatusRunning && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	// Running server: membership changes go straight out over RCON.
	if resp, out := e.call("POST", "/api/servers/"+id+"/members", admin, map[string]string{"user_id": "steve"}); resp.StatusCode != 201 || out["queued"] != false {
		t.Fatalf("add member: %d %v", resp.StatusCode, out)
	}
	waitForCommand(t, dir, "whitelist add Steve")
	if resp, _ := e.call("PATCH", "/api/users/me", steve, map[string]string{"mc_username": "Steve_2"}); resp.StatusCode != 200 {
		t.Fatalf("rename: %d", resp.StatusCode)
	}
	waitForCommand(t, dir, "whitelist remove Steve")
	waitForCommand(t, dir, "whitelist add Steve_2")
	if resp, _ := e.call("PATCH", "/api/users/me", steve, map[string]string{"mc_username": "no spaces"}); resp.StatusCode != 400 {
		t.Errorf("invalid mc name: %d", resp.StatusCode)
	}
	if got := pending(t, e, id); len(got) != 0 {
		t.Errorf("pending on a running server: %v", got)
	}

	// The non-member saw none of survival's events.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var ev events.Event
	if err := conn.ReadJSON(&ev); err == nil {
		t.Errorf("non-member received %s for %s", ev.Type, ev.ServerID)
	}
}

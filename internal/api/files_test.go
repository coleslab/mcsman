package api

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileEditor(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	id, dir := srv["id"].(string), srv["directory"].(string)
	cfg := filepath.Join(dir, "plugins", "Essentials", "config.yml")
	os.MkdirAll(filepath.Dir(cfg), 0o750)
	os.WriteFile(cfg, []byte("v: 0\n"), 0o640)
	steveMC := "Steve"
	player := e.player(t, "steve", &steveMC)
	e.makeMember(t, admin, id, "steve", "Steve")

	base := "/api/servers/" + id + "/files"
	q := "?path=" + url.QueryEscape("plugins/Essentials/config.yml")
	for _, p := range []string{base, base + "/content" + q, base + "/revisions" + q} {
		if resp, _ := e.call("GET", p, player, nil); resp.StatusCode != 403 {
			t.Errorf("player GET %s: %d", p, resp.StatusCode)
		}
	}

	files := e.list(t, base, admin)
	var paths []string
	for _, f := range files {
		paths = append(paths, f["path"].(string))
	}
	if !strings.Contains(strings.Join(paths, ","), "plugins/Essentials/config.yml") {
		t.Errorf("files: %v", paths)
	}

	resp, f := e.call("GET", base+"/content"+q, admin, nil)
	if resp.StatusCode != 200 || f["content"] != "v: 0\n" {
		t.Fatalf("read: %d %v", resp.StatusCode, f)
	}
	sha := f["sha256"].(string)

	resp, out := e.call("PUT", base+"/content"+q, admin, map[string]any{"content": "v: [\n", "base_sha256": sha})
	if resp.StatusCode != 422 || out["line"] == nil {
		t.Errorf("invalid yaml: %d %v", resp.StatusCode, out)
	}
	// Save twelve times; only the last ten previous versions are kept.
	for i := 1; i <= 12; i++ {
		resp, out := e.call("PUT", base+"/content"+q, admin, map[string]any{"content": fmt.Sprintf("v: %d\n", i), "base_sha256": sha})
		if resp.StatusCode != 200 || out["restart_required"] != false {
			t.Fatalf("save %d: %d %v", i, resp.StatusCode, out)
		}
		sha = out["sha256"].(string)
	}
	if raw, _ := os.ReadFile(cfg); string(raw) != "v: 12\n" {
		t.Errorf("on disk: %q", raw)
	}
	if resp, _ := e.call("PUT", base+"/content"+q, admin, map[string]any{"content": "v: x\n", "base_sha256": "stale"}); resp.StatusCode != 409 {
		t.Errorf("stale save: %d", resp.StatusCode)
	}
	revs := e.list(t, base+"/revisions"+q, admin)
	if len(revs) != 10 || revs[0]["username"] != "admin" {
		t.Fatalf("revisions: %d %v", len(revs), revs[0])
	}
	resp, rev := e.call("GET", base+"/revisions/"+revs[0]["id"].(string), admin, nil)
	if resp.StatusCode != 200 || rev["content"] != "v: 11\n" {
		t.Errorf("latest revision: %d %v", resp.StatusCode, rev)
	}
	if evs := e.list(t, "/api/servers/"+id+"/events", admin); len(evs) == 0 || evs[0]["type"] != "config_edit" {
		t.Errorf("events: %v", evs)
	}

	for _, p := range []string{"eula.txt", "server.jar", "../../etc/passwd", "plugins/Essentials/missing.yml"} {
		want := 400
		if strings.HasSuffix(p, "missing.yml") {
			want = 404
		}
		if resp, _ := e.call("GET", base+"/content?path="+url.QueryEscape(p), admin, nil); resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", p, resp.StatusCode, want)
		}
	}
}

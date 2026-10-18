package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebhookCRUDMasksURL(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	url := "https://discord.com/api/webhooks/123/SUPER-SECRET-abcd"

	if resp, _ := e.call("POST", "/api/webhooks", admin, map[string]any{"name": "x", "url": "https://evil.com/hook"}); resp.StatusCode != 400 {
		t.Errorf("non-discord url: %d", resp.StatusCode)
	}
	resp, out := e.call("POST", "/api/webhooks", admin, map[string]any{"name": "status", "url": url})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	if strings.Contains(out["url_masked"].(string), "SECRET") || !strings.HasSuffix(out["url_masked"].(string), "abcd") {
		t.Errorf("mask %v", out["url_masked"])
	}
	if out["events"].(map[string]any)["server_crash"] != true || out["events"].(map[string]any)["player_join"] != false {
		t.Errorf("default events %v", out["events"])
	}
	id := out["id"].(string)
	resp, out = e.call("PATCH", "/api/webhooks/"+id, admin, map[string]any{"events": map[string]bool{"player_join": true, "bogus": true}})
	if resp.StatusCode != 200 || out["events"].(map[string]any)["player_join"] != true {
		t.Errorf("patch: %d %v", resp.StatusCode, out)
	}
	if _, ok := out["events"].(map[string]any)["bogus"]; ok {
		t.Error("unknown event key stored")
	}

	req, _ := http.NewRequest("GET", e.tcp.URL+"/api/webhooks", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	lr, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(lr.Body)
	lr.Body.Close()
	if strings.Contains(string(body), "SECRET") {
		t.Error("list exposes URL")
	}
	if resp, _ := e.call("DELETE", "/api/webhooks/"+id, admin, nil); resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
}

func levelDat(dv int32) []byte {
	var w bytes.Buffer
	tag := func(t byte, name string) {
		w.WriteByte(t)
		binary.Write(&w, binary.BigEndian, uint16(len(name)))
		w.WriteString(name)
	}
	tag(10, "")
	tag(10, "Data")
	tag(3, "DataVersion")
	binary.Write(&w, binary.BigEndian, dv)
	w.WriteByte(0)
	w.WriteByte(0)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	gz.Write(w.Bytes())
	gz.Close()
	return out.Bytes()
}

func TestWorldImportExportAndTicketDownload(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	id := srv["id"].(string)

	// Build an upload.
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	f, _ := zw.Create("Island/level.dat")
	f.Write(levelDat(3955))
	f, _ = zw.Create("Island/region/r.0.0.mca")
	f.Write([]byte("region"))
	zw.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("source_name", "Island")
	fw, _ := mw.CreateFormFile("world", "island.zip")
	fw.Write(zbuf.Bytes())
	mw.Close()
	req, _ := http.NewRequest("POST", e.tcp.URL+"/api/servers/"+id+"/worlds/import", &body)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var wi map[string]any
	json.NewDecoder(resp.Body).Decode(&wi)
	resp.Body.Close()
	if resp.StatusCode != 201 || wi["source_name"] != "Island" {
		t.Fatalf("import: %d %v", resp.StatusCode, wi)
	}
	if b, _ := os.ReadFile(filepath.Join(srv["directory"].(string), "world", "region", "r.0.0.mca")); string(b) != "region" {
		t.Error("world not installed")
	}

	// Export: bearer works, ticket works once, no auth fails.
	_, tk := e.call("POST", "/api/ws-ticket", admin, nil)
	ticket := tk["ticket"].(string)
	r1, _ := http.Get(e.tcp.URL + "/api/servers/" + id + "/worlds/export?ticket=" + ticket)
	data, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	if r1.StatusCode != 200 || !strings.Contains(r1.Header.Get("Content-Disposition"), "survival-world-") {
		t.Fatalf("ticket export: %d", r1.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("export zip: %v", err)
	}
	r2, _ := http.Get(e.tcp.URL + "/api/servers/" + id + "/worlds/export?ticket=" + ticket)
	r2.Body.Close()
	if r2.StatusCode != 401 {
		t.Errorf("ticket reuse: %d", r2.StatusCode)
	}
	r3, _ := http.Get(e.tcp.URL + "/api/servers/" + id + "/worlds/export")
	r3.Body.Close()
	if r3.StatusCode != 401 {
		t.Errorf("no auth export: %d", r3.StatusCode)
	}

	resp2, list := e.call("GET", "/api/servers/"+id+"/worlds/imports", admin, nil)
	_ = list
	if resp2.StatusCode != 200 {
		t.Errorf("imports list: %d", resp2.StatusCode)
	}
}

func TestWebhookServerScopeAPI(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	url := "https://discord.com/api/webhooks/1/tok"

	if resp, _ := e.call("POST", "/api/webhooks", admin, map[string]any{"name": "x", "url": url, "server_ids": []string{"nope"}}); resp.StatusCode != 400 {
		t.Errorf("unknown server: %d", resp.StatusCode)
	}
	// Names are accepted and resolved to IDs.
	resp, out := e.call("POST", "/api/webhooks", admin, map[string]any{"name": "s", "url": url, "server_ids": []string{"survival"}})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	ids := out["server_ids"].([]any)
	if len(ids) != 1 || ids[0] != srv["id"] || out["server_names"].([]any)[0] != "survival" {
		t.Errorf("scope %v %v", out["server_ids"], out["server_names"])
	}
	// PATCH without server_ids keeps the scope; [] clears it.
	resp, out = e.call("PATCH", "/api/webhooks/"+out["id"].(string), admin, map[string]any{"name": "renamed"})
	if len(out["server_ids"].([]any)) != 1 {
		t.Errorf("scope lost on unrelated patch: %v", out["server_ids"])
	}
	resp, out = e.call("PATCH", "/api/webhooks/"+out["id"].(string), admin, map[string]any{"server_ids": []string{}})
	if resp.StatusCode != 200 || len(out["server_ids"].([]any)) != 0 || out["all_servers"] != true {
		t.Errorf("clear scope: %d %v", resp.StatusCode, out["server_ids"])
	}
}

func TestServerViewScheduleAndLastBackup(t *testing.T) {
	e := newEnv(t)
	e.initAdmin()
	admin, _ := e.login("admin", "password123")
	srv := e.createServer(t, admin, "survival", nil)
	id := srv["id"].(string)

	resp, out := e.call("PATCH", "/api/servers/"+id, admin, map[string]any{
		"backup_schedule": "6h", "backup_keep": 5, "restart_schedule": "0 5 * * *", "autostart": true,
		"connection_address": "survival.example.net",
	})
	if resp.StatusCode != 200 || out["backup_schedule"] != "6h" || out["backup_keep"].(float64) != 5 || out["autostart"] != true {
		t.Fatalf("patch: %d %v", resp.StatusCode, out)
	}
	if resp, _ := e.call("PATCH", "/api/servers/"+id, admin, map[string]any{"restart_schedule": "sometimes"}); resp.StatusCode != 400 {
		t.Errorf("bad schedule: %d", resp.StatusCode)
	}
	// "" resets to default, 0 resets keep.
	_, out = e.call("PATCH", "/api/servers/"+id, admin, map[string]any{"backup_schedule": "", "backup_keep": 0})
	if out["backup_schedule"] != nil || out["backup_keep"] != nil {
		t.Errorf("reset: %v %v", out["backup_schedule"], out["backup_keep"])
	}

	// A failed backup (no world yet) shows up as last_backup.ok=false.
	e.call("POST", "/api/servers/"+id+"/backups?wait=true", admin, nil)
	_, view := e.call("GET", "/api/servers/"+id, admin, nil)
	lb, ok := view["last_backup"].(map[string]any)
	if !ok || lb["ok"] != false || lb["error"] == "" {
		t.Errorf("last_backup %v", view["last_backup"])
	}
}

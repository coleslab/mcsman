package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

func jarWith(t *testing.T, yml string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("plugin.yml")
	io.WriteString(w, yml)
	zw.Close()
	return buf.Bytes()
}

// fakeRegistries serves a Modrinth project "lp" with versions for 1.21.1
// (v1, v2) and none for 26.3, and a Hangar project "Owner/Thing".
type fakeRegistries struct {
	srv      *httptest.Server
	files    map[string][]byte
	tamper   bool
	external bool
	modrinth *Modrinth
	hangar   *Hangar
	latestLP string
}

func newFakeRegistries(t *testing.T) *fakeRegistries {
	f := &fakeRegistries{files: map[string][]byte{
		"lp-1.jar":    jarWith(t, "name: LuckPerms\nversion: 1\n"),
		"lp-2.jar":    jarWith(t, "name: LuckPerms\nversion: 2\n"),
		"thing-3.jar": jarWith(t, "name: Thing\nversion: 3\n"),
	}, latestLP: "v1"}
	sha512hex := func(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }
	sha256hex := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

	mux := http.NewServeMux()
	mrVersion := func(id string) map[string]any {
		file := map[string]string{"v1": "lp-1.jar", "v2": "lp-2.jar"}[id]
		sum := sha512hex(f.files[file])
		if f.tamper {
			sum = strings.Repeat("0", 128)
		}
		return map[string]any{
			"id": id, "project_id": "LPID", "version_number": strings.TrimPrefix(id, "v") + ".0",
			"version_type": "release", "loaders": []string{"paper"},
			"files": []map[string]any{{"filename": file, "url": f.srv.URL + "/files/" + file, "primary": true,
				"hashes": map[string]string{"sha512": sum, "sha1": "x"}}},
		}
	}
	mux.HandleFunc("/v2/project/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "UA", 400)
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "LPID", "title": "LuckPerms"})
	})
	mux.HandleFunc("/v2/project/{id}/version", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("game_versions"), "26.3") {
			w.Write([]byte("[]"))
			return
		}
		json.NewEncoder(w).Encode([]any{mrVersion(f.latestLP)})
	})
	mux.HandleFunc("/v2/version/{id}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mrVersion(r.PathValue("id")))
	})
	mux.HandleFunc("/v2/search", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"hits":[{"project_id":"LPID","slug":"luckperms","title":"LuckPerms","author":"lucko","downloads":5}]}`)
	})
	mux.HandleFunc("/api/v1/projects/{owner}/{slug}/versions", func(w http.ResponseWriter, r *http.Request) {
		dl := map[string]any{"fileInfo": map[string]any{"name": "thing-3.jar", "sha256Hash": sha256hex(f.files["thing-3.jar"])},
			"downloadUrl": f.srv.URL + "/files/thing-3.jar"}
		if f.external {
			dl = map[string]any{"externalUrl": "https://example.com/thing.jar"}
		}
		json.NewEncoder(w).Encode(map[string]any{"result": []any{
			map[string]any{"name": "3.0-SNAPSHOT", "channel": map[string]string{"name": "Snapshot"}, "downloads": map[string]any{"PAPER": dl}},
			map[string]any{"name": "3.0", "channel": map[string]string{"name": "Release"}, "downloads": map[string]any{"PAPER": dl}},
		}})
	})
	mux.HandleFunc("/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Write(f.files[r.PathValue("name")])
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.modrinth = NewModrinth(f.srv.URL)
	f.hangar = NewHangar(f.srv.URL)
	return f
}

func setup(t *testing.T) (*Installer, *fakeRegistries, string, string) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	m := server.NewManager(server.Options{DB: d, Bus: events.NewBus(), Sealer: sealer, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return "", errors.New("unused") }})
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "s", MCVersion: "1.21.1", Port: 30000, AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeRegistries(t)
	return NewInstaller(d, m, log, f.modrinth, f.hangar), f, srv.ID, filepath.Join(srv.Directory, "plugins")
}

func TestInstallUpdateRemove(t *testing.T) {
	in, f, sid, pdir := setup(t)
	ctx := context.Background()

	p, err := in.Install(ctx, sid, "modrinth", "luckperms", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "LuckPerms" || p.Version != "1.0" || p.Filename != "lp-1.jar" || *p.SourceID != "LPID" {
		t.Errorf("installed %+v", p)
	}
	sum := sha256.Sum256(f.files["lp-1.jar"])
	if p.SHA256 != hex.EncodeToString(sum[:]) {
		t.Error("stored sha256 is not the file's sha256")
	}

	if _, updated, err := in.Update(ctx, sid, p.ID); err != nil || updated {
		t.Errorf("no-op update: updated=%v err=%v", updated, err)
	}
	f.latestLP = "v2"
	np, updated, err := in.Update(ctx, sid, p.ID)
	if err != nil || !updated || np.Version != "2.0" || np.ID != p.ID {
		t.Fatalf("update: %+v %v %v", np, updated, err)
	}
	if _, err := os.Stat(filepath.Join(pdir, "lp-1.jar")); !os.IsNotExist(err) {
		t.Error("old jar left behind")
	}

	// Installing the same project again updates rather than duplicating.
	if _, err := in.Install(ctx, sid, "modrinth", "LPID", "v1"); err != nil {
		t.Fatal(err)
	}
	list, _ := in.List(ctx, sid)
	if len(list) != 1 || list[0].Version != "1.0" {
		t.Fatalf("list after reinstall: %+v", list)
	}

	if err := in.Remove(ctx, sid, p.ID); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(pdir)
	if len(entries) != 0 {
		t.Errorf("plugins dir not empty: %v", entries)
	}
}

func TestHangarPrefersReleaseChannel(t *testing.T) {
	in, _, sid, _ := setup(t)
	p, err := in.Install(context.Background(), sid, "hangar", "Owner/Thing", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "3.0" {
		t.Errorf("picked %s, want release channel 3.0", p.Version)
	}
	if _, err := in.Install(context.Background(), sid, "hangar", "noslash", ""); err == nil {
		t.Error("invalid hangar id accepted")
	}
}

func TestRefusesUnverifiedDownloads(t *testing.T) {
	in, f, sid, pdir := setup(t)
	f.tamper = true
	if _, err := in.Install(context.Background(), sid, "modrinth", "LPID", ""); err == nil {
		t.Fatal("tampered checksum accepted")
	}
	f.external = true
	if _, err := in.Install(context.Background(), sid, "hangar", "Owner/Thing", ""); !errors.Is(err, ErrUnverifiable) {
		t.Errorf("external hangar download: %v", err)
	}
	entries, _ := os.ReadDir(pdir)
	if len(entries) != 0 {
		t.Errorf("unverified files written: %v", entries)
	}
}

func TestManualDiscoveryAndConflict(t *testing.T) {
	in, f, sid, pdir := setup(t)
	ctx := context.Background()
	os.MkdirAll(pdir, 0o750)
	os.WriteFile(filepath.Join(pdir, "Essentials.jar"), jarWith(t, "name: Essentials\nversion: 2.20.1\n"), 0o640)
	os.WriteFile(filepath.Join(pdir, "lp-1.jar"), f.files["lp-1.jar"], 0o640) // same name as a registry file
	os.WriteFile(filepath.Join(pdir, "notes.txt"), []byte("x"), 0o640)

	list, err := in.List(ctx, sid)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	var ess *db.Plugin
	for _, p := range list {
		if p.Filename == "Essentials.jar" {
			ess = p
		}
	}
	if ess == nil || ess.Name != "Essentials" || ess.Version != "2.20.1" || ess.Source != "manual" {
		t.Errorf("manual plugin %+v", ess)
	}
	if _, err := in.Install(ctx, sid, "modrinth", "LPID", "v1"); !errors.Is(err, server.ErrConflict) {
		t.Errorf("filename clash with manual jar: %v", err)
	}
	if _, _, err := in.Update(ctx, sid, ess.ID); !errors.Is(err, server.ErrInvalid) {
		t.Errorf("update manual: %v", err)
	}

	// Deleting a file out-of-band drops its record on the next list.
	os.Remove(filepath.Join(pdir, "Essentials.jar"))
	list, _ = in.List(ctx, sid)
	if len(list) != 1 {
		t.Errorf("stale record kept: %+v", list)
	}
}

func TestCompatCheck(t *testing.T) {
	in, _, sid, pdir := setup(t)
	ctx := context.Background()
	in.Install(ctx, sid, "modrinth", "LPID", "")
	in.Install(ctx, sid, "hangar", "Owner/Thing", "")
	os.WriteFile(filepath.Join(pdir, "Custom.jar"), jarWith(t, "name: Custom\nversion: 1\n"), 0o640)
	list, _ := in.List(ctx, sid)

	got := map[string]string{}
	for _, c := range in.Check(ctx, list, "26.3") {
		got[c.Plugin.Name] = c.Status
	}
	// Fake Modrinth has nothing for 26.3; fake Hangar ignores the version.
	want := map[string]string{"LuckPerms": Amber, "Thing": Green, "Custom": Gray}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
}

func TestSearch(t *testing.T) {
	_, f, _, _ := setup(t)
	res, err := f.modrinth.Search(context.Background(), "luck", "1.21.1", 5)
	if err != nil || len(res) != 1 || res[0].PageURL != "https://modrinth.com/plugin/luckperms" {
		t.Errorf("search %+v %v", res, err)
	}
}

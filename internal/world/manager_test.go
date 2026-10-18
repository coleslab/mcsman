package world

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcsman/internal/backup"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

type entry struct {
	name string
	body []byte
	mode os.FileMode
}

func makeZip(t *testing.T, entries []entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "w.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.body)
	}
	zw.Close()
	f.Close()
	return p
}

func serverJar(worldVersion string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("version.json")
	io.WriteString(w, `{"id":"1.21.1","world_version":`+worldVersion+`}`)
	zw.Close()
	return buf.Bytes()
}

func setup(t *testing.T) (*Manager, db.Server) {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	bus := events.NewBus()
	m := server.NewManager(server.Options{DB: d, Bus: bus, Sealer: sealer, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return "", nil }})
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "s", MCVersion: "1.21.1", Port: 30000, AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.Directory, "server.jar"), serverJar("3955"), 0o640)
	// An existing world with a separate nether folder.
	os.MkdirAll(filepath.Join(srv.Directory, "world"), 0o750)
	os.WriteFile(filepath.Join(srv.Directory, "world", "level.dat"), LevelDat(3955, "1.21.1"), 0o640)
	os.WriteFile(filepath.Join(srv.Directory, "world", "old.txt"), []byte("old"), 0o640)
	os.MkdirAll(filepath.Join(srv.Directory, "world_nether"), 0o750)
	os.WriteFile(filepath.Join(srv.Directory, "world_nether", "old.txt"), []byte("old"), 0o640)
	return &Manager{DB: d, Servers: m, Log: log,
		Backups: &backup.Manager{DB: d, Servers: m, Bus: bus, Log: log, DataDir: dir, Keep: 10}}, *srv
}

func TestInspect(t *testing.T) {
	m, srv := setup(t)
	root := makeZip(t, []entry{{name: "level.dat", body: LevelDat(3700, "1.20.4")}})
	in, err := m.Inspect(root, srv.ID)
	if err != nil || in.DataVersion != 3700 || in.ServerDataVersion != 3955 || in.Newer || !in.Older {
		t.Fatalf("root: %+v %v", in, err)
	}
	nested := makeZip(t, []entry{{name: "My World/level.dat", body: LevelDat(4000, "1.21.4")}})
	in, err = m.Inspect(nested, srv.ID)
	if err != nil || in.root != "My World" || !in.Newer {
		t.Fatalf("nested: %+v %v", in, err)
	}
	deep := makeZip(t, []entry{{name: "a/b/level.dat", body: LevelDat(1, "x")}})
	if _, err := m.Inspect(deep, srv.ID); !errors.Is(err, server.ErrInvalid) {
		t.Errorf("too deep: %v", err)
	}
	if _, err := m.Inspect(makeZip(t, []entry{{name: "readme.txt"}}), srv.ID); !errors.Is(err, server.ErrInvalid) {
		t.Errorf("no level.dat: %v", err)
	}
}

func TestImportReplacesWorld(t *testing.T) {
	m, srv := setup(t)
	ctx := context.Background()
	z := makeZip(t, []entry{
		{name: "Survival/level.dat", body: LevelDat(3955, "1.21.1")},
		{name: "Survival/region/r.0.0.mca", body: []byte("region")},
		{name: "Survival/session.lock", body: []byte("lock")},
		{name: "Survival_nether/DIM-1/region/r.0.0.mca", body: []byte("nether")},
		{name: "__MACOSX/._junk", body: []byte("x")},
	})
	wi, err := m.Import(ctx, srv.ID, z, ImportOptions{SourceName: "Bob's world"})
	if err != nil {
		t.Fatal(err)
	}
	if wi.SourceMCVersion != "1.21.1" || wi.PreImportBackupID == nil {
		t.Errorf("record %+v", wi)
	}
	dir := srv.Directory
	if b, _ := os.ReadFile(filepath.Join(dir, "world", "region", "r.0.0.mca")); string(b) != "region" {
		t.Error("world not extracted")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "world_nether", "DIM-1", "region", "r.0.0.mca")); string(b) != "nether" {
		t.Error("nether sibling not mapped to world_nether")
	}
	for _, p := range []string{"world/old.txt", "world_nether/old.txt", "world/session.lock"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s should not exist", p)
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".import-") || strings.HasPrefix(e.Name(), ".old-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
	list, _ := m.DB.ListWorldImports(ctx, srv.ID)
	if len(list) != 1 {
		t.Errorf("imports = %d", len(list))
	}
}

func TestImportRefusesNewerUnlessForced(t *testing.T) {
	m, srv := setup(t)
	z := makeZip(t, []entry{{name: "level.dat", body: LevelDat(9999, "99.1")}})
	if _, err := m.Import(context.Background(), srv.ID, z, ImportOptions{}); !errors.Is(err, ErrNewerWorld) {
		t.Fatalf("newer world: %v", err)
	}
	if _, err := m.Import(context.Background(), srv.ID, z, ImportOptions{Force: true}); err != nil {
		t.Fatalf("forced: %v", err)
	}
}

func TestImportRejectsHostileArchives(t *testing.T) {
	cases := map[string][]entry{
		"traversal": {{name: "level.dat", body: LevelDat(3955, "1.21.1")}, {name: "../../evil.txt", body: []byte("x")}},
		"symlink":   {{name: "level.dat", body: LevelDat(3955, "1.21.1")}, {name: "link", body: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777}},
	}
	for name, ents := range cases {
		t.Run(name, func(t *testing.T) {
			m, srv := setup(t)
			_, err := m.Import(context.Background(), srv.ID, makeZip(t, ents), ImportOptions{})
			if name == "traversal" {
				// path.Clean("/"+name) neutralises ../ to a path inside the
				// world; the file must land inside, never outside.
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(filepath.Dir(srv.Directory), "evil.txt")); !os.IsNotExist(err) {
					t.Fatal("file escaped server directory")
				}
				return
			}
			if !errors.Is(err, server.ErrInvalid) {
				t.Errorf("got %v", err)
			}
			if b, _ := os.ReadFile(filepath.Join(srv.Directory, "world", "old.txt")); string(b) != "old" {
				t.Error("existing world damaged by rejected import")
			}
		})
	}
}

func TestImportSizeLimit(t *testing.T) {
	old := MaxUncompressed
	MaxUncompressed = 1024
	t.Cleanup(func() { MaxUncompressed = old })
	m, srv := setup(t)
	z := makeZip(t, []entry{{name: "level.dat", body: LevelDat(3955, "1.21.1")}, {name: "big", body: make([]byte, 4096)}})
	if _, err := m.Import(context.Background(), srv.ID, z, ImportOptions{}); !errors.Is(err, server.ErrInvalid) {
		t.Errorf("zip bomb: %v", err)
	}
}

func TestExportRoundTrip(t *testing.T) {
	m, srv := setup(t)
	var buf bytes.Buffer
	if err := m.Export(context.Background(), srv.ID, &buf); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "export.zip")
	os.WriteFile(p, buf.Bytes(), 0o600)
	in, err := m.Inspect(p, srv.ID)
	if err != nil || in.root != "world" {
		t.Fatalf("export not importable: %+v %v", in, err)
	}
	zr, _ := zip.OpenReader(p)
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["world/old.txt"] || !names["world_nether/old.txt"] {
		t.Errorf("export contents %v", names)
	}
	// Round trip keeps the nether.
	if _, err := m.Import(context.Background(), srv.ID, p, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(srv.Directory, "world_nether", "old.txt")); err != nil {
		t.Error("nether lost in export/import round trip")
	}
}

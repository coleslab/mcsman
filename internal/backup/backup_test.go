package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
	"mcsman/internal/testutil"
)

type harness struct {
	m    *Manager
	srvs *server.Manager
	db   *db.DB
	bus  *events.Bus
}

func newHarness(t *testing.T, keep int) *harness {
	t.Helper()
	fake := testutil.FakeMC(t)
	dataDir := t.TempDir()
	d, err := db.Open(filepath.Join(dataDir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	bus := events.NewBus()
	srvs := server.NewManager(server.Options{
		DB: d, Bus: bus, Sealer: sealer, DataDir: dataDir, Logger: log,
		Java: func(string) (string, error) { return fake, nil },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		srvs.Shutdown(ctx)
		d.Close()
	})
	return &harness{
		m:    &Manager{DB: d, Servers: srvs, Bus: bus, Log: log, DataDir: dataDir, Keep: keep},
		srvs: srvs, db: d, bus: bus,
	}
}

func (h *harness) server(t *testing.T, start bool) *server.Instance {
	t.Helper()
	s, err := h.srvs.Create(context.Background(), server.CreateParams{
		Name: "survival", MCVersion: "1.21.1", Port: testutil.FreePort(t), RCONPort: testutil.FreePort(t), AcceptEULA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.Directory, "server.jar"), []byte("x"), 0o640)
	inst, _ := h.srvs.Get(s.ID)
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, inst, db.StatusRunning)
	if !start {
		if err := inst.Stop(context.Background(), server.StopOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return inst
}

func waitStatus(t *testing.T, inst *server.Instance, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for inst.Status() != want {
		if time.Now().After(deadline) {
			t.Fatalf("status %s, want %s", inst.Status(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackupRunningServerFlushSequence(t *testing.T) {
	h := newHarness(t, 24)
	inst := h.server(t, true)
	srv := inst.Server()
	ch, cancel := h.bus.Subscribe(64)
	defer cancel()

	b, err := h.m.Create(context.Background(), srv.ID, Options{Tag: "Manual Test!"})
	if err != nil {
		t.Fatal(err)
	}
	if *b.Tag != "manual-test" {
		t.Errorf("tag = %q", *b.Tag)
	}

	raw, _ := os.ReadFile(filepath.Join(srv.Directory, "fakemc.commands"))
	cmds := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var seq []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "save-") {
			seq = append(seq, c)
		}
	}
	if strings.Join(seq, ",") != "save-all flush,save-off,save-on" {
		t.Errorf("flush sequence = %v", seq)
	}

	_, path, err := h.m.File(context.Background(), srv.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o640 || fi.Size() != b.SizeBytes {
		t.Fatalf("archive: %v %v", fi, err)
	}
	if side, _ := os.ReadFile(path + ".sha256"); !strings.HasPrefix(string(side), b.SHA256) {
		t.Error("sidecar checksum mismatch")
	}
	tops, err := verifyArchive(path, b.SHA256)
	if err != nil || len(tops) != 1 || tops[0] != "world" {
		t.Errorf("archive contents %v %v", tops, err)
	}

	phases := map[string]bool{}
	timeout := time.After(time.Second)
	for !phases["done"] {
		select {
		case e := <-ch:
			if e.Type == events.BackupProgress {
				phases[e.Data.(map[string]any)["phase"].(string)] = true
			}
		case <-timeout:
			t.Fatalf("phases seen: %v", phases)
		}
	}
	for _, p := range []string{"flush", "archive", "verify", "done"} {
		if !phases[p] {
			t.Errorf("missing phase %s", p)
		}
	}
}

func TestRetention(t *testing.T) {
	h := newHarness(t, 2)
	inst := h.server(t, false)
	id := inst.Server().ID
	var first *db.Backup
	for i := 0; i < 3; i++ {
		b, err := h.m.Create(context.Background(), id, Options{Tag: "auto" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		}
	}
	list, _ := h.m.List(context.Background(), id)
	if len(list) != 2 {
		t.Fatalf("kept %d backups, want 2", len(list))
	}
	dir, _ := h.m.Dir(inst.Server())
	if _, err := os.Stat(filepath.Join(dir, first.Filename)); !os.IsNotExist(err) {
		t.Error("pruned archive still on disk")
	}
}

func TestRestore(t *testing.T) {
	h := newHarness(t, 24)
	inst := h.server(t, false)
	srv := inst.Server()
	ctx := context.Background()
	level := filepath.Join(srv.Directory, "world", "level.dat")

	b, err := h.m.Create(ctx, srv.ID, Options{Tag: "good"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(level, []byte("griefed"), 0o640)
	os.WriteFile(filepath.Join(srv.Directory, "world", "extra.txt"), []byte("x"), 0o640)

	// Restore refuses while running.
	inst.Start()
	waitStatus(t, inst, db.StatusRunning)
	if err := h.m.Restore(ctx, srv.ID, b.ID, nil); !errors.Is(err, server.ErrConflict) {
		t.Errorf("restore while running: %v", err)
	}
	inst.Stop(ctx, server.StopOptions{})

	if err := h.m.Restore(ctx, srv.ID, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(level)
	if string(got) != "level" {
		t.Errorf("level.dat = %q after restore", got)
	}
	if _, err := os.Stat(filepath.Join(srv.Directory, "world", "extra.txt")); !os.IsNotExist(err) {
		t.Error("files from after the backup survived restore")
	}
	list, _ := h.m.List(ctx, srv.ID)
	if len(list) != 2 || *list[0].Tag != "pre-restore" {
		t.Errorf("expected pre-restore backup, got %d backups", len(list))
	}
	entries, _ := os.ReadDir(srv.Directory)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".restore-") || strings.HasPrefix(e.Name(), ".old-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestRestoreRejectsTamperedArchive(t *testing.T) {
	h := newHarness(t, 24)
	inst := h.server(t, false)
	srv := inst.Server()
	b, err := h.m.Create(context.Background(), srv.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, path, _ := h.m.File(context.Background(), srv.ID, b.ID)
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.Write([]byte("junk"))
	f.Close()
	if err := h.m.Restore(context.Background(), srv.ID, b.ID, nil); err == nil {
		t.Fatal("tampered archive restored")
	}
}

func TestConcurrentBackupRejected(t *testing.T) {
	h := newHarness(t, 24)
	inst := h.server(t, false)
	unlock, ok := h.m.lock(inst.Server().ID)
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	if _, err := h.m.Create(context.Background(), inst.Server().ID, Options{}); !errors.Is(err, ErrInProgress) {
		t.Errorf("got %v", err)
	}
}

func TestSanitizeTag(t *testing.T) {
	cases := map[string]string{"": "manual", "pre-upgrade-1.21.1": "pre-upgrade-1.21.1", "../../etc": "etc", "A B": "a-b"}
	for in, want := range cases {
		if got := sanitizeTag(in); got != want {
			t.Errorf("sanitizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

package version

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcsman/internal/backup"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/plugin"
	"mcsman/internal/server"
	"mcsman/internal/testutil"
)

type fakeJava struct{ installed []int }

func (f *fakeJava) Ensure(_ context.Context, major int) (string, error) {
	f.installed = append(f.installed, major)
	return "", nil
}

type upgradeEnv struct {
	u    *Upgrader
	inst *server.Instance
	db   *db.DB
	fill *fakeFill
}

func newUpgradeEnv(t *testing.T, mc string) *upgradeEnv {
	t.Helper()
	fake := testutil.FakeMC(t)
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	bus := events.NewBus()
	m := server.NewManager(server.Options{DB: d, Bus: bus, Sealer: sealer, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return fake, nil }})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.Shutdown(ctx)
		d.Close()
	})
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "s", MCVersion: mc, JavaVersion: "17",
		Port: testutil.FreePort(t), RCONPort: testutil.FreePort(t), AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.Directory, "server.jar"), []byte("old jar"), 0o640)
	inst, _ := m.Get(srv.ID)

	f := newFakeFill(t)
	c := f.client()
	u := &Upgrader{
		Paper: c, Jars: &JarStore{Dir: filepath.Join(dir, "jars", "paper"), Client: c}, Java: &fakeJava{},
		Plugins: plugin.NewInstaller(d, m, log),
		Backups: &backup.Manager{DB: d, Servers: m, Bus: bus, Log: log, DataDir: dir, Keep: 10},
		Servers: m, DB: d, Bus: bus, Log: log, TailDuration: 1500 * time.Millisecond,
	}
	return &upgradeEnv{u: u, inst: inst, db: d, fill: f}
}

func waitJob(t *testing.T, u *Upgrader, id string) *Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if j := u.Job(id); j != nil && j.State != JobRunning {
			return j
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("upgrade did not finish")
	return nil
}

func TestPreflight(t *testing.T) {
	e := newUpgradeEnv(t, "1.21.1")
	ctx := context.Background()
	id := e.inst.Server().ID

	p, err := e.u.Preflight(ctx, id, "1.21.4")
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanUpgrade || p.TargetBuild != 232 || !p.JavaChange || p.RequiredJava != 21 {
		t.Errorf("preflight %+v", p)
	}
	if _, err := e.u.Preflight(ctx, id, "9.9"); err == nil {
		t.Error("unknown version accepted")
	}
	e2 := newUpgradeEnv(t, "26.1.1")
	p, err = e2.u.Preflight(ctx, e2.inst.Server().ID, "1.21.4")
	if err != nil || p.CanUpgrade || len(p.Blockers) == 0 {
		t.Errorf("downgrade allowed: %+v %v", p, err)
	}
	if _, err := e2.u.Confirm(ctx, e2.inst.Server().ID, "1.21.4", ConfirmOptions{}); err == nil {
		t.Error("confirm allowed a downgrade")
	}
}

func TestUpgradeRunningServer(t *testing.T) {
	e := newUpgradeEnv(t, "1.21.1")
	ctx := context.Background()
	srv := e.inst.Server()
	if err := e.inst.Start(); err != nil {
		t.Fatal(err)
	}
	for e.inst.Status() != db.StatusRunning {
		time.Sleep(20 * time.Millisecond)
	}
	os.WriteFile(filepath.Join(srv.Directory, "fakemc.plugin-error"), nil, 0o640)

	if _, err := e.u.Confirm(ctx, srv.ID, "1.21.4", ConfirmOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.u.Confirm(ctx, srv.ID, "1.21.4", ConfirmOptions{}); err == nil {
		t.Error("second concurrent upgrade accepted")
	}
	j := waitJob(t, e.u, srv.ID)
	if j.State != JobSucceeded {
		t.Fatalf("job failed: %s (steps %+v)", j.Error, j.Steps)
	}
	jar, _ := os.ReadFile(filepath.Join(srv.Directory, "server.jar"))
	if string(jar) != string(e.fill.jar) {
		t.Error("server.jar not replaced")
	}
	got := e.inst.Server()
	if got.MCVersion != "1.21.4" || got.JavaVersion != "21" || e.inst.Status() != db.StatusRunning {
		t.Errorf("after upgrade: %s java %s status %s", got.MCVersion, got.JavaVersion, e.inst.Status())
	}
	b, err := e.db.BackupByID(ctx, srv.ID, j.BackupID)
	if err != nil || *b.Tag != "pre-upgrade-1.21.1" {
		t.Errorf("pre-upgrade backup: %+v %v", b, err)
	}
	if len(j.PluginErrors) != 1 || !strings.Contains(j.PluginErrors[0], "BrokenPlugin") {
		t.Errorf("plugin errors %v", j.PluginErrors)
	}
	var phases []string
	for _, s := range j.Steps {
		phases = append(phases, s.Phase)
	}
	if strings.Join(phases, ",") != "stop,backup,download,java,apply,start,verify,done" {
		t.Errorf("phases %v", phases)
	}
}

func TestUpgradeFailureLeavesServerUntouched(t *testing.T) {
	e := newUpgradeEnv(t, "1.21.1")
	srv := e.inst.Server()
	os.MkdirAll(filepath.Join(srv.Directory, "world"), 0o750) // so the backup has something
	e.fill.badSum = true
	if _, err := e.u.Confirm(context.Background(), srv.ID, "1.21.4", ConfirmOptions{}); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, e.u, srv.ID)
	if j.State != JobFailed || !strings.Contains(j.Error, "checksum") {
		t.Fatalf("job %+v", j)
	}
	jar, _ := os.ReadFile(filepath.Join(srv.Directory, "server.jar"))
	if string(jar) != "old jar" || e.inst.Server().MCVersion != "1.21.1" {
		t.Error("failed upgrade modified the server")
	}
	if e.inst.Busy() != "" {
		t.Error("exclusive lock not released")
	}
}

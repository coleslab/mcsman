package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mcsman/internal/backup"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
	"mcsman/internal/testutil"
)

func setup(t *testing.T) (*Scheduler, *server.Instance) {
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
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "s", MCVersion: "1.21.1",
		Port: testutil.FreePort(t), RCONPort: testutil.FreePort(t), AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.Directory, "server.jar"), []byte("x"), 0o640)
	inst, _ := m.Get(srv.ID)
	s := &Scheduler{Servers: m, DB: d, Log: log, DefaultBackup: "1h", RestartDelay: time.Second,
		Backups: &backup.Manager{DB: d, Servers: m, Bus: bus, Log: log, DataDir: dir, Keep: 10}}
	s.Start()
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s, inst
}

func waitRunning(t *testing.T, inst *server.Instance) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for inst.Status() != db.StatusRunning {
		if time.Now().After(deadline) {
			t.Fatalf("status %s", inst.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNextAndReload(t *testing.T) {
	s, inst := setup(t)
	id := inst.Server().ID
	b, r := s.Next(id)
	if b == nil || r != nil || time.Until(*b) < 59*time.Minute || time.Until(*b) > 61*time.Minute {
		t.Fatalf("next backup %v restart %v", b, r)
	}
	off, restart := "off", "0 4 * * *"
	if _, err := s.Servers.Update(context.Background(), id, server.UpdateParams{BackupSchedule: &off, RestartSchedule: &restart}); err != nil {
		t.Fatal(err)
	}
	s.Reload(id)
	b, r = s.Next(id)
	if b != nil || r == nil || r.Hour() != 4 || r.Minute() != 0 {
		t.Errorf("after reload: backup %v restart %v", b, r)
	}
	bad := "every tuesday"
	if _, err := s.Servers.Update(context.Background(), id, server.UpdateParams{BackupSchedule: &bad}); err == nil {
		t.Error("invalid schedule accepted")
	}
	s.Remove(id)
	if b, r := s.Next(id); b != nil || r != nil {
		t.Error("removed server still scheduled")
	}
}

func TestBackupJobOnlyWhenChanged(t *testing.T) {
	s, inst := setup(t)
	id := inst.Server().ID
	count := func() int {
		l, _ := s.DB.ListBackups(context.Background(), id)
		return len(l)
	}

	s.runBackup(id) // never started: no world
	if count() != 0 {
		t.Fatal("backed up a server that never ran")
	}
	inst.Start()
	waitRunning(t, inst)
	s.runBackup(id) // running: always
	if count() != 1 {
		t.Fatalf("running backup: %d", count())
	}
	time.Sleep(1100 * time.Millisecond) // event timestamps are in seconds
	inst.Stop(context.Background(), server.StopOptions{})
	s.runBackup(id) // stopped after last backup: once more
	if count() != 2 {
		t.Fatalf("post-stop backup: %d", count())
	}
	time.Sleep(1100 * time.Millisecond)
	s.runBackup(id) // unchanged since: skip
	if count() != 2 {
		t.Errorf("unchanged stopped server backed up again: %d", count())
	}
	l, _ := s.DB.ListBackups(context.Background(), id)
	if *l[0].Tag != "auto" || l[0].CreatedBy != nil {
		t.Errorf("tag %v by %v", *l[0].Tag, l[0].CreatedBy)
	}
}

func TestRestartJob(t *testing.T) {
	s, inst := setup(t)
	id := inst.Server().ID
	s.runRestart(id) // stopped: no-op
	if inst.Status() != db.StatusStopped {
		t.Fatal("restart job started a stopped server")
	}
	inst.Start()
	waitRunning(t, inst)
	pid := inst.PID()
	s.runRestart(id)
	waitRunning(t, inst)
	if inst.PID() == pid {
		t.Error("server was not restarted")
	}
}

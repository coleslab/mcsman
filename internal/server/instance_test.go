package server

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
	"mcsman/internal/testutil"
)

type harness struct {
	m   *Manager
	db  *db.DB
	bus *events.Bus
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	fake := testutil.FakeMC(t)
	dataDir := t.TempDir()
	d, err := db.Open(filepath.Join(dataDir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	bus := events.NewBus()
	m := NewManager(Options{
		DB: d, Bus: bus, Sealer: sealer, DataDir: dataDir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Java:    func(string) (string, error) { return fake, nil },
		Backoff: []time.Duration{50 * time.Millisecond, 50 * time.Millisecond},
	})
	h := &harness{m: m, db: d, bus: bus}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.Shutdown(ctx)
		d.Close()
	})
	return h
}

func (h *harness) create(t *testing.T, name string) *Instance {
	t.Helper()
	s, err := h.m.Create(context.Background(), CreateParams{
		Name: name, MCVersion: "1.21.1", Port: testutil.FreePort(t), RCONPort: testutil.FreePort(t), AcceptEULA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Directory, "server.jar"), []byte("fake"), 0o640); err != nil {
		t.Fatal(err)
	}
	inst, _ := h.m.Get(s.ID)
	return inst
}

func waitStatus(t *testing.T, inst *Instance, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if inst.Status() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status = %s, want %s", inst.Status(), want)
}

func TestStartStopLifecycle(t *testing.T) {
	h := newHarness(t)
	inst := h.create(t, "survival")
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); !errors.Is(err, ErrConflict) {
		t.Errorf("double start: %v", err)
	}
	waitStatus(t, inst, db.StatusRunning, 5*time.Second)

	props, err := readProperties(inst.Server().Directory)
	if err != nil || props["enable-rcon"] != "true" || props["rcon.password"] == "" {
		t.Fatalf("properties: %v %v", props, err)
	}
	if out, err := inst.Exec("list"); err != nil || !strings.Contains(out, "players online") {
		t.Fatalf("rcon list: %q %v", out, err)
	}

	lines, ch, cancel := inst.ConsoleSnapshot(64)
	defer cancel()
	if !containsLine(lines, "Done (") {
		t.Errorf("ring buffer missing startup lines: %v", lines)
	}
	if err := inst.WriteConsole([]byte("say hello\n")); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, ch, func(e events.Event) bool {
		d, ok := e.Data.(map[string]string)
		return e.Type == events.ConsoleOutput && ok && strings.Contains(d["line"], "[Server] hello")
	})

	start := time.Now()
	if err := inst.Stop(context.Background(), StopOptions{Delay: 1 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Second {
		t.Error("countdown was skipped")
	}
	if inst.Status() != db.StatusStopped {
		t.Errorf("status after stop = %s", inst.Status())
	}
	s, _ := h.db.ServerByID(context.Background(), inst.Server().ID)
	if s.Status != db.StatusStopped {
		t.Errorf("persisted status = %s", s.Status)
	}
}

func TestCrashAutoRestartThenGiveUp(t *testing.T) {
	h := newHarness(t)
	inst := h.create(t, "crashy")
	ch, cancel := h.bus.Subscribe(256)
	defer cancel()

	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	// Backoff has 2 entries: two restarts, then the third crash is final.
	for attempt := 0; attempt < 3; attempt++ {
		waitStatus(t, inst, db.StatusRunning, 5*time.Second)
		inst.WriteConsole([]byte("crash\n"))
		waitForEvent(t, ch, func(e events.Event) bool { return e.Type == events.ServerCrash })
	}
	waitStatus(t, inst, db.StatusCrashed, 2*time.Second)
	time.Sleep(200 * time.Millisecond)
	if inst.Status() != db.StatusCrashed {
		t.Fatalf("restarted after giving up: %s", inst.Status())
	}
	evs, _ := h.db.ListServerEvents(context.Background(), inst.Server().ID, 50)
	crashes := 0
	for _, e := range evs {
		if e.Type == "crash" {
			crashes++
		}
	}
	if crashes != 3 {
		t.Errorf("crash events = %d, want 3", crashes)
	}
	// Stop settles a crashed server.
	inst.Stop(context.Background(), StopOptions{})
	if inst.Status() != db.StatusStopped {
		t.Errorf("status = %s", inst.Status())
	}
}

func TestExternalKillRestarts(t *testing.T) {
	h := newHarness(t)
	inst := h.create(t, "killed")
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, inst, db.StatusRunning, 5*time.Second)
	pid := inst.PID()
	p, _ := os.FindProcess(pid)
	p.Kill()
	waitStatus(t, inst, db.StatusCrashed, 5*time.Second)
	waitStatus(t, inst, db.StatusRunning, 5*time.Second)
	if inst.PID() == pid {
		t.Error("expected a new process after kill")
	}
}

func TestStopEscalates(t *testing.T) {
	oldStop, oldTerm := stopTimeout, termTimeout
	stopTimeout, termTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { stopTimeout, termTimeout = oldStop, oldTerm })

	for _, ignoreTerm := range []bool{false, true} {
		h := newHarness(t)
		inst := h.create(t, "stubborn")
		dir := inst.Server().Directory
		os.WriteFile(filepath.Join(dir, "fakemc.ignore-stop"), nil, 0o640)
		if ignoreTerm {
			os.WriteFile(filepath.Join(dir, "fakemc.ignore-term"), nil, 0o640)
		}
		if err := inst.Start(); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, inst, db.StatusRunning, 5*time.Second)
		done := make(chan struct{})
		go func() { inst.Stop(context.Background(), StopOptions{}); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("ignoreTerm=%v: stop did not escalate", ignoreTerm)
		}
		if inst.Status() != db.StatusStopped {
			t.Errorf("ignoreTerm=%v: status = %s", ignoreTerm, inst.Status())
		}
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	base := CreateParams{Name: "ok", MCVersion: "1.21.1", Port: 30000, AcceptEULA: true}

	bad := map[string]func(p *CreateParams){
		"no eula":   func(p *CreateParams) { p.AcceptEULA = false },
		"traversal": func(p *CreateParams) { p.Name = "../etc" },
		"uppercase": func(p *CreateParams) { p.Name = "Survival" },
		"version":   func(p *CreateParams) { p.MCVersion = "latest" },
		"low port":  func(p *CreateParams) { p.Port = 80 },
		"same port": func(p *CreateParams) { p.RCONPort = 30000 },
	}
	for name, mut := range bad {
		p := base
		mut(&p)
		if _, err := h.m.Create(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	s, err := h.m.Create(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if s.JavaVersion != "21" || s.RCONPort != 30010 {
		t.Errorf("defaults: java=%s rcon=%d", s.JavaVersion, s.RCONPort)
	}
	dup := base
	dup.Name = "other"
	if _, err := h.m.Create(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Errorf("port reuse: %v", err)
	}
	if err := h.m.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Directory); !os.IsNotExist(err) {
		t.Error("directory not removed")
	}
}

func TestEnvDoesNotLeakSecrets(t *testing.T) {
	t.Setenv("MCSMAN_JWT_SECRET", "super-secret")
	for _, kv := range childEnv() {
		if strings.Contains(kv, "super-secret") {
			t.Fatal("secret leaked into child env")
		}
	}
}

func TestRing(t *testing.T) {
	var r ring
	for i := 0; i < ringSize+5; i++ {
		r.add(strings.Repeat("x", i%3))
	}
	if len(r.lines()) != ringSize {
		t.Errorf("len = %d", len(r.lines()))
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func waitForEvent(t *testing.T, ch <-chan events.Event, match func(events.Event) bool) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e := <-ch:
			if match(e) {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}

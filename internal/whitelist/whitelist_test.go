package whitelist

import (
	"context"
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

func TestQueueThenApplyOnStart(t *testing.T) {
	fake := testutil.FakeMC(t)
	dir := t.TempDir()
	d, _ := db.Open(filepath.Join(dir, "t.db"))
	defer d.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sealer, _ := crypto.NewSealer("0123456789abcdef0123456789abcdef", crypto.LabelRCON)
	bus := events.NewBus()
	m := server.NewManager(server.Options{DB: d, Bus: bus, Sealer: sealer, DataDir: dir, Logger: log,
		Java: func(string) (string, error) { return fake, nil }})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	}()
	srv, err := m.Create(context.Background(), server.CreateParams{Name: "s", MCVersion: "1.21.1",
		Port: testutil.FreePort(t), RCONPort: testutil.FreePort(t), AcceptEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srv.Directory, "server.jar"), []byte("x"), 0o640)

	wl := &Service{DB: d, Servers: m, Bus: bus, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wl.Run(ctx)
	time.Sleep(50 * time.Millisecond) // let Run subscribe

	applied, err := wl.Add(context.Background(), srv.ID, "Steve")
	if err != nil || applied {
		t.Fatalf("stopped server: applied=%v err=%v", applied, err)
	}

	inst, _ := m.Get(srv.ID)
	inst.Start()
	cmds := filepath.Join(srv.Directory, "fakemc.commands")
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, _ := os.ReadFile(cmds)
		if strings.Contains(string(raw), "whitelist add Steve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued whitelist entry not applied on start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if p, _ := d.PendingWhitelist(context.Background(), srv.ID); len(p) != 0 {
		t.Errorf("still pending: %v", p)
	}

	applied, err = wl.Add(context.Background(), srv.ID, "Alex")
	if err != nil || !applied {
		t.Fatalf("running server: applied=%v err=%v", applied, err)
	}
}

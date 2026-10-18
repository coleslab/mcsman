package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPragmasAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	var fk int
	d.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Errorf("journal_mode=%s foreign_keys=%d", mode, fk)
	}
	d.Close()
	// Re-opening must be a no-op migration.
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
}

func TestUserSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	u := &User{Username: "alice", PasswordHash: "h", Role: RoleAdmin}
	if err := d.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateUser(ctx, &User{Username: "alice", PasswordHash: "h", Role: RolePlayer}); err == nil {
		t.Error("duplicate username accepted")
	}
	s := &Session{UserID: u.ID, RefreshTokenHash: "old", ExpiresAt: now() + 60}
	if err := d.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := d.RotateSession(ctx, s.ID, "old", "new", now()+60); err != nil {
		t.Fatal(err)
	}
	if err := d.RotateSession(ctx, s.ID, "old", "newer", now()+60); !errors.Is(err, ErrNotFound) {
		t.Errorf("replayed rotation: %v", err)
	}
	if err := d.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SessionByID(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Error("session not cascaded on user delete")
	}
}

func TestServerCRUD(t *testing.T) {
	ctx := context.Background()
	d := openTest(t)
	s := &Server{Name: "survival", Directory: "/x/survival", MCVersion: "1.21.1", JavaVersion: "21",
		Port: 25565, RCONPort: 25575, RCONPasswordEnc: "enc", MaxMemoryMB: 2048, AutoRestart: true}
	if err := d.CreateServer(ctx, s); err != nil {
		t.Fatal(err)
	}
	dup := *s
	dup.ID, dup.Name, dup.Directory = "", "other", "/x/other"
	if err := d.CreateServer(ctx, &dup); err == nil {
		t.Error("duplicate port accepted")
	}
	addr := "survival.playit.gg"
	s.ConnectionAddress = &addr
	if err := d.UpdateServer(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := d.ServerByName(ctx, "survival")
	if err != nil || got.ConnectionAddress == nil || *got.ConnectionAddress != addr || !got.AutoRestart {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := d.AddServerEvent(ctx, s.ID, "start", nil); err != nil {
		t.Fatal(err)
	}
	evs, _ := d.ListServerEvents(ctx, s.ID, 10)
	if len(evs) != 1 {
		t.Errorf("events = %d", len(evs))
	}
}

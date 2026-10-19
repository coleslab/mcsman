package db

import (
	"context"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestMembersMigrationBackfillsAndReverts(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	src, _ := iofs.New(migrationsFS, "migrations")
	drv, err := sqlite.WithInstance(d.DB, &sqlite.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", drv)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(4); err != nil {
		t.Fatalf("down to 4: %v", err)
	}

	// Pre-membership data: one server invite and one site invite, redeemed.
	admin := &User{Username: "admin", PasswordHash: "x", Role: RoleAdmin}
	steve := &User{Username: "steve", PasswordHash: "x", Role: RolePlayer}
	bob := &User{Username: "bob", PasswordHash: "x", Role: RolePlayer}
	for _, u := range []*User{admin, steve, bob} {
		if err := d.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{Name: "survival", Directory: "/x", MCVersion: "1.21.1", Port: 25565, RCONPort: 25575, RCONPasswordEnc: "x", MaxMemoryMB: 1024, JavaVersion: "21"}
	if err := d.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		id, user string
		server   any
	}{{"i1", steve.ID, srv.ID}, {"i2", bob.ID, nil}} {
		if _, err := d.ExecContext(ctx, `INSERT INTO invites (id, token_hash, created_by, created_at, expires_at, used_at, used_by, whitelist_server_id)
			VALUES (?, ?, ?, 1, 2, 1, ?, ?)`, x.id, x.id, admin.ID, x.user, x.server); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	ms, err := d.ListMembers(ctx, srv.ID)
	if err != nil || len(ms) != 1 || ms[0].UserID != steve.ID || ms[0].AddedBy == nil || *ms[0].AddedBy != admin.ID {
		t.Fatalf("backfilled members: %v %v", ms, err)
	}
	if ids, _ := d.ServersForUser(ctx, bob.ID); len(ids) != 0 {
		t.Errorf("site-only invite became a membership: %v", ids)
	}

	// The down migration drops the table and the queue's action column.
	if _, err := d.QueueWhitelist(ctx, srv.ID, "Steve", WhitelistRemove, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(4); err != nil {
		t.Fatalf("down again: %v", err)
	}
	var n int
	d.QueryRowContext(ctx, `SELECT COUNT(*) FROM whitelist_queue`).Scan(&n)
	if n != 0 {
		t.Errorf("queued removals survived the down migration: %d", n)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestQueueWhitelistSupersedes(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	srv := &Server{Name: "s", Directory: "/x", MCVersion: "1.21.1", Port: 25565, RCONPort: 25575, RCONPasswordEnc: "x", MaxMemoryMB: 1024, JavaVersion: "21"}
	if err := d.CreateServer(ctx, srv); err != nil {
		t.Fatal(err)
	}
	d.QueueWhitelist(ctx, srv.ID, "Steve", WhitelistAdd, false)
	d.QueueWhitelist(ctx, srv.ID, "Alex", WhitelistAdd, false)
	d.QueueWhitelist(ctx, srv.ID, "steve", WhitelistRemove, false)
	p, _ := d.PendingWhitelist(ctx, srv.ID)
	if len(p) != 2 || p[0].MCUsername != "Alex" || p[1].Action != WhitelistRemove || p[1].MCUsername != "steve" {
		t.Errorf("pending: %+v %+v", p[0], p[len(p)-1])
	}
}

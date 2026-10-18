package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mcsman/internal/db"
)

func newInvites(t *testing.T) (*Invites, *db.DB) {
	s, d := setup(t)
	return &Invites{DB: d, Auth: s, BaseURL: "https://mc.example.com/", DefaultTTL: time.Hour}, d
}

func TestInviteLifecycle(t *testing.T) {
	bcryptCost = 4 // speed; cost-12 is asserted in TestBcryptCost12
	t.Cleanup(func() { bcryptCost = 12 })
	ctx := context.Background()
	iv, d := newInvites(t)

	c, err := iv.Create(ctx, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.URL, "https://mc.example.com/join/") || len(c.Token) < 40 {
		t.Errorf("url=%s token=%s", c.URL, c.Token)
	}
	if c.Invite.TokenHash == c.Token || strings.Contains(c.Invite.TokenHash, c.Token) {
		t.Fatal("raw token stored")
	}
	pending, _ := d.PendingInvites(ctx)
	if len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}

	u, _, tok, err := iv.Redeem(ctx, c.Token, RedeemRequest{Username: "steve", Password: "password123", MCUsername: "Steve_1"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != db.RolePlayer || tok.AccessToken == "" || *u.MCUsername != "Steve_1" {
		t.Errorf("user %+v", u)
	}
	if _, _, _, err := iv.Redeem(ctx, c.Token, RedeemRequest{Username: "alex", Password: "password123"}); !errors.Is(err, db.ErrInviteInvalid) {
		t.Errorf("reuse: %v", err)
	}
	pending, _ = d.PendingInvites(ctx)
	if len(pending) != 0 {
		t.Error("used invite still pending")
	}
}

func TestInviteValidation(t *testing.T) {
	bcryptCost = 4
	t.Cleanup(func() { bcryptCost = 12 })
	ctx := context.Background()
	iv, d := newInvites(t)

	if _, err := iv.Create(ctx, nil, 31*24*time.Hour, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("long ttl: %v", err)
	}
	c, _ := iv.Create(ctx, nil, time.Hour, nil)
	cases := map[string]RedeemRequest{
		"taken":      {Username: "alice", Password: "password123"},
		"short pw":   {Username: "bob", Password: "short"},
		"bad mc":     {Username: "bob", Password: "password123", MCUsername: "no spaces!"},
		"blank name": {Username: "  ", Password: "password123"},
	}
	for name, req := range cases {
		if _, _, _, err := iv.Redeem(ctx, c.Token, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Failed attempts must not consume the invite.
	if _, err := iv.Lookup(ctx, c.Token); err != nil {
		t.Errorf("invite consumed by failed redemption: %v", err)
	}

	// Whitelist invites require a Minecraft username.
	srv := &db.Server{Name: "s", Directory: "/x", MCVersion: "1.21.1", JavaVersion: "21", Port: 30000, RCONPort: 30010, RCONPasswordEnc: "x", MaxMemoryMB: 1024}
	d.CreateServer(ctx, srv)
	wc, _ := iv.Create(ctx, nil, time.Hour, &srv.ID)
	if _, _, _, err := iv.Redeem(ctx, wc.Token, RedeemRequest{Username: "bob", Password: "password123"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("whitelist invite without mc name: %v", err)
	}
}

func TestInviteExpiryAndRevoke(t *testing.T) {
	ctx := context.Background()
	iv, d := newInvites(t)
	c, _ := iv.Create(ctx, nil, time.Hour, nil)
	d.Exec(`UPDATE invites SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), c.Invite.ID)
	if _, err := iv.Lookup(ctx, c.Token); !errors.Is(err, db.ErrInviteInvalid) {
		t.Errorf("expired: %v", err)
	}
	c2, _ := iv.Create(ctx, nil, time.Hour, nil)
	if err := d.RevokeInvite(ctx, c2.Invite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := iv.Lookup(ctx, c2.Token); !errors.Is(err, db.ErrInviteInvalid) {
		t.Errorf("revoked: %v", err)
	}
}

func TestConcurrentRedeemOnlyOnce(t *testing.T) {
	bcryptCost = 4
	t.Cleanup(func() { bcryptCost = 12 })
	ctx := context.Background()
	iv, d := newInvites(t)
	c, _ := iv.Create(ctx, nil, time.Hour, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := iv.Redeem(ctx, c.Token, RedeemRequest{Username: "user" + string(rune('a'+i)), Password: "password123"})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("%d redemptions succeeded", ok)
	}
	n, _ := d.CountUsers(ctx)
	if n != 2 { // alice + one redeemer
		t.Errorf("users = %d", n)
	}
}

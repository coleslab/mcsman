package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"mcsman/internal/db"
)

const secret = "0123456789abcdef0123456789abcdef"

func setup(t *testing.T) (*Service, *db.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateUser(context.Background(), &db.User{Username: "alice", PasswordHash: h, Role: db.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	return NewService(d, secret, time.Minute, time.Hour), d
}

func TestBcryptCost12(t *testing.T) {
	h, _ := HashPassword("correct horse")
	if c, _ := bcrypt.Cost([]byte(h)); c != 12 {
		t.Errorf("bcrypt cost = %d, want 12", c)
	}
}

func TestPasswordLength(t *testing.T) {
	if _, err := HashPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Error("short password accepted")
	}
}

func TestLoginRefreshLogout(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)

	if _, err := s.Login(ctx, "alice", "wrong pass"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.Login(ctx, "nobody", "whatever1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	tok, err := s.Login(ctx, "alice", "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, tok.AccessToken)
	if err != nil || !p.IsAdmin() {
		t.Fatalf("authenticate: %v", err)
	}

	tok2, err := s.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, tok.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Error("old refresh token reusable after rotation")
	}

	if err := s.Logout(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, tok2.AccessToken); !errors.Is(err, ErrUnauthorized) {
		t.Error("access token valid after logout")
	}
	if _, err := s.Refresh(ctx, tok2.RefreshToken); !errors.Is(err, ErrUnauthorized) {
		t.Error("refresh valid after logout")
	}
}

func TestRejectsForeignTokens(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "x", "sid": "y", "iss": "mcsman", "exp": time.Now().Add(time.Hour).Unix()})
	raw, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := s.Authenticate(ctx, raw); err == nil {
		t.Error("alg=none accepted")
	}
	other := tokenIssuer{secret: []byte("another-secret-another-secret-xx"), ttl: time.Minute}
	raw, _ = other.issue("x", "admin", "y")
	if _, err := s.Authenticate(ctx, raw); err == nil {
		t.Error("token signed with other secret accepted")
	}
}

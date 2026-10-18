// Package auth implements password hashing, JWT access tokens and rotating
// refresh-token sessions.
package auth

import (
	"context"
	"errors"
	"time"

	"mcsman/internal/crypto"
	"mcsman/internal/db"
)

var (
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInvalidCredentials = errors.New("invalid username or password")
)

// Tokens is returned on login and refresh.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Service ties together users, sessions and tokens.
type Service struct {
	db         *db.DB
	tokens     tokenIssuer
	refreshTTL time.Duration
}

func NewService(d *db.DB, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		db:         d,
		tokens:     tokenIssuer{secret: []byte(secret), ttl: accessTTL},
		refreshTTL: refreshTTL,
	}
}

// Login verifies credentials and opens a new session.
func (s *Service) Login(ctx context.Context, username, password string) (*Tokens, error) {
	u, err := s.db.UserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			CheckPassword(dummyHash(), password)
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if !CheckPassword(u.PasswordHash, password) {
		return nil, ErrInvalidCredentials
	}
	if err := s.db.TouchLogin(ctx, u.ID); err != nil {
		return nil, err
	}
	return s.StartSession(ctx, u)
}

// StartSession opens a session for an already-authenticated user (used by
// login and invite redemption).
func (s *Service) StartSession(ctx context.Context, u *db.User) (*Tokens, error) {
	refresh, err := crypto.RandomToken(32)
	if err != nil {
		return nil, err
	}
	sess := &db.Session{
		UserID:           u.ID,
		RefreshTokenHash: crypto.SHA256Hex(refresh),
		ExpiresAt:        time.Now().Add(s.refreshTTL).Unix(),
	}
	if err := s.db.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	access, err := s.tokens.issue(u.ID, u.Role, sess.ID)
	if err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, RefreshToken: refresh}, nil
}

// Refresh validates a refresh token, rotates it and issues a new access token.
func (s *Service) Refresh(ctx context.Context, refresh string) (*Tokens, error) {
	oldHash := crypto.SHA256Hex(refresh)
	sess, err := s.db.SessionByRefreshHash(ctx, oldHash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if sess.RevokedAt != nil || time.Now().Unix() >= sess.ExpiresAt {
		return nil, ErrUnauthorized
	}
	u, err := s.db.UserByID(ctx, sess.UserID)
	if err != nil {
		return nil, ErrUnauthorized
	}
	next, err := crypto.RandomToken(32)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(s.refreshTTL).Unix()
	if err := s.db.RotateSession(ctx, sess.ID, oldHash, crypto.SHA256Hex(next), expires); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrUnauthorized // lost a race with a concurrent refresh
		}
		return nil, err
	}
	access, err := s.tokens.issue(u.ID, u.Role, sess.ID)
	if err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, RefreshToken: next}, nil
}

// Logout revokes the session.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.db.RevokeSession(ctx, sessionID)
}

// Principal is the authenticated caller of a request.
type Principal struct {
	User      *db.User
	SessionID string // empty for Unix-socket callers
}

func (p *Principal) IsAdmin() bool { return p.User.Role == db.RoleAdmin }

// Authenticate validates an access token, checks the session is still live and
// loads the current user (so role changes and deletions apply immediately).
func (s *Service) Authenticate(ctx context.Context, accessToken string) (*Principal, error) {
	c, err := s.tokens.parse(accessToken)
	if err != nil {
		return nil, err
	}
	sess, err := s.db.SessionByID(ctx, c.SessionID)
	if err != nil || sess.RevokedAt != nil || sess.UserID != c.Subject {
		return nil, ErrUnauthorized
	}
	u, err := s.db.UserByID(ctx, c.Subject)
	if err != nil {
		return nil, ErrUnauthorized
	}
	return &Principal{User: u, SessionID: sess.ID}, nil
}

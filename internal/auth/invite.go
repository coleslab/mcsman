package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"mcsman/internal/crypto"
	"mcsman/internal/db"
)

const MaxInviteTTL = 30 * 24 * time.Hour

var (
	ErrUsernameTaken = errors.New("that username is taken")
	ErrInvalidInput  = errors.New("invalid input")

	mcUsernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)
)

// ValidMCUsername reports whether s is a valid Minecraft Java username.
func ValidMCUsername(s string) bool { return mcUsernameRe.MatchString(s) }

// ValidUsername reports whether s is an acceptable site username.
func ValidUsername(s string) bool {
	return len(s) >= 1 && len(s) <= 32 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n\t/")
}

// Invites issues and redeems single-use invite tokens. Only the SHA-256 of
// a token is stored; the raw token is returned once at creation.
type Invites struct {
	DB         *db.DB
	Auth       *Service
	BaseURL    string
	DefaultTTL time.Duration
}

// Created is returned once when an invite is made.
type Created struct {
	Invite *db.Invite `json:"invite"`
	Token  string     `json:"token"`
	URL    string     `json:"url"`
}

// Create issues an invite valid for ttl (0 = default), optionally
// whitelisting the redeemer on a server.
func (iv *Invites) Create(ctx context.Context, createdBy *string, ttl time.Duration, whitelistServerID *string) (*Created, error) {
	if ttl == 0 {
		ttl = iv.DefaultTTL
	}
	if ttl < time.Minute || ttl > MaxInviteTTL {
		return nil, fmt.Errorf("%w: expiry must be between 1 minute and 30 days", ErrInvalidInput)
	}
	// 32 random bytes, base64url: 256 bits of entropy.
	token, err := crypto.RandomToken(32)
	if err != nil {
		return nil, err
	}
	inv := &db.Invite{
		TokenHash:         crypto.SHA256Hex(token),
		CreatedBy:         createdBy,
		ExpiresAt:         time.Now().Add(ttl).Unix(),
		WhitelistServerID: whitelistServerID,
	}
	if err := iv.DB.CreateInvite(ctx, inv); err != nil {
		return nil, err
	}
	return &Created{Invite: inv, Token: token, URL: strings.TrimRight(iv.BaseURL, "/") + "/join/" + token}, nil
}

// Lookup returns the pending invite for a raw token. Lookup is by SHA-256
// of the token: the stored hash of a 256-bit secret leaks nothing useful
// through comparison timing, so an indexed equality match is safe.
func (iv *Invites) Lookup(ctx context.Context, token string) (*db.Invite, error) {
	if token == "" || len(token) > 128 {
		return nil, db.ErrInviteInvalid
	}
	return iv.DB.ValidInviteByHash(ctx, crypto.SHA256Hex(token))
}

// RedeemRequest is the public redemption form.
type RedeemRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	MCUsername string `json:"mc_username"`
}

// Redeem creates a player account from an invite and logs it in.
func (iv *Invites) Redeem(ctx context.Context, token string, req RedeemRequest) (*db.User, *db.Invite, *Tokens, error) {
	inv, err := iv.Lookup(ctx, token)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Username = strings.TrimSpace(req.Username)
	req.MCUsername = strings.TrimSpace(req.MCUsername)
	if !ValidUsername(req.Username) {
		return nil, nil, nil, fmt.Errorf("%w: username must be 1-32 characters", ErrInvalidInput)
	}
	var mc *string
	if req.MCUsername != "" {
		if !ValidMCUsername(req.MCUsername) {
			return nil, nil, nil, fmt.Errorf("%w: Minecraft username must be 3-16 letters, numbers or _", ErrInvalidInput)
		}
		mc = &req.MCUsername
	} else if inv.WhitelistServerID != nil {
		return nil, nil, nil, fmt.Errorf("%w: a Minecraft username is required for this invite", ErrInvalidInput)
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		return nil, nil, nil, err
	}
	u := &db.User{
		Username:     req.Username,
		MCUsername:   mc,
		PasswordHash: hash,
		Role:         db.RolePlayer,
		CreatedBy:    inv.CreatedBy,
	}
	if err := iv.DB.RedeemInvite(ctx, inv.ID, u); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.username") {
			return nil, nil, nil, ErrUsernameTaken
		}
		return nil, nil, nil, err
	}
	tok, err := iv.Auth.StartSession(ctx, u)
	if err != nil {
		return nil, nil, nil, err
	}
	return u, inv, tok, nil
}

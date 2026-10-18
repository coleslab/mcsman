package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims carried in an access token.
type Claims struct {
	Role      string `json:"role"`
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

type tokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

func (t *tokenIssuer) issue(userID, role, sessionID string) (string, error) {
	n := time.Now()
	c := Claims{
		Role:      role,
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(n),
			ExpiresAt: jwt.NewNumericDate(n.Add(t.ttl)),
			Issuer:    "mcsman",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
}

func (t *tokenIssuer) parse(raw string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c, func(tok *jwt.Token) (any, error) {
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer("mcsman"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if c.Subject == "" || c.SessionID == "" {
		return nil, errors.Join(ErrUnauthorized, errors.New("missing claims"))
	}
	return &c, nil
}

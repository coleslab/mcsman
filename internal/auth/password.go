package auth

import (
	"errors"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

var bcryptCost = 12

// UseMinCostForTesting lowers the bcrypt cost so tests in other packages run
// quickly. Never call outside tests.
func UseMinCostForTesting() { bcryptCost = bcrypt.MinCost }

const (
	MinPasswordLength = 8
	// bcrypt ignores input past 72 bytes; reject rather than silently truncate.
	maxPasswordLength = 72
)

var ErrWeakPassword = errors.New("password must be 8-72 characters")

// HashPassword validates length and returns a bcrypt hash at cost 12.
func HashPassword(pw string) (string, error) {
	if len(pw) < MinPasswordLength || len(pw) > maxPasswordLength {
		return "", ErrWeakPassword
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(h), err
}

// CheckPassword reports whether pw matches hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash is compared against when a username does not exist so login
// timing does not reveal which usernames are registered.
var dummyHash = sync.OnceValue(func() string {
	h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcryptCost)
	return string(h)
})

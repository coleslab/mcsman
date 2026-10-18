package api

import (
	"sync"
	"time"

	"mcsman/internal/auth"
	"mcsman/internal/crypto"
)

const ticketTTL = 30 * time.Second

// ticketStore issues short-lived, single-use tickets that authenticate a
// WebSocket upgrade without putting the JWT in a URL.
type ticketStore struct {
	mu      sync.Mutex
	tickets map[string]ticket
}

type ticket struct {
	p       *auth.Principal
	expires time.Time
}

func newTicketStore() *ticketStore { return &ticketStore{tickets: map[string]ticket{}} }

func (ts *ticketStore) issue(p *auth.Principal) (string, error) {
	raw, err := crypto.RandomToken(32)
	if err != nil {
		return "", err
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.tickets[crypto.SHA256Hex(raw)] = ticket{p: p, expires: time.Now().Add(ticketTTL)}
	return raw, nil
}

// redeem consumes a ticket, returning its principal if valid.
func (ts *ticketStore) redeem(raw string) *auth.Principal {
	key := crypto.SHA256Hex(raw)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.tickets[key]
	delete(ts.tickets, key)
	if !ok || time.Now().After(t.expires) {
		return nil
	}
	return t.p
}

func (ts *ticketStore) sweep() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for k, t := range ts.tickets {
		if time.Now().After(t.expires) {
			delete(ts.tickets, k)
		}
	}
}

// Package console wraps the two channels into a running server: the PTY
// attached to the Java process and the RCON protocol.
package console

import (
	"errors"
	"sync"
	"time"

	"github.com/gorcon/rcon"
)

// RCON is a lazily-connecting, reconnecting RCON client. It is safe for
// concurrent use; commands are serialised over one connection.
type RCON struct {
	addr     string
	password string

	mu   sync.Mutex
	conn *rcon.Conn
}

func NewRCON(addr, password string) *RCON {
	return &RCON{addr: addr, password: password}
}

// Exec runs a command, connecting (or reconnecting once) as needed.
func (r *RCON) Exec(cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if r.conn == nil {
			c, err := rcon.Dial(r.addr, r.password,
				rcon.SetDialTimeout(3*time.Second), rcon.SetDeadline(10*time.Second))
			if err != nil {
				return "", err
			}
			r.conn = c
		}
		out, err := r.conn.Execute(cmd)
		if err == nil {
			return out, nil
		}
		r.conn.Close()
		r.conn = nil
		if errors.Is(err, rcon.ErrAuthFailed) {
			return "", err
		}
	}
	return "", errors.New("rcon: command failed after reconnect")
}

// Close drops the connection; the next Exec reconnects.
func (r *RCON) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
}

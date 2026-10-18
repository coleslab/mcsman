package api

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"mcsman/internal/events"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 50 * time.Second
	wsMaxMessage = 4096
)

// Event types forwarded to console clients.
var consoleEventTypes = map[string]bool{
	events.ConsoleOutput: true, events.Metrics: true, events.ServerStatus: true,
	events.PlayerJoin: true, events.PlayerLeave: true,
	events.BackupProgress: true, events.UpgradeProgress: true,
}

// default CheckOrigin rejects cross-origin browser requests.
var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

// consoleInput is a client → server message.
type consoleInput struct {
	Type string `json:"type"` // "input"
	Data string `json:"data"`
}

// handleConsole streams console output and server events, and forwards
// input to the server's PTY. Auth: Unix socket, or ?ticket= from /api/ws-ticket.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		p = s.tickets.redeem(r.URL.Query().Get("ticket"))
	}
	if p == nil {
		writeError(w, http.StatusUnauthorized, "invalid or missing ticket")
		return
	}
	if !p.IsAdmin() {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the error response
	}
	defer conn.Close()

	srv := inst.Server()
	lines, ch, cancel := inst.ConsoleSnapshot(512)
	defer cancel()

	send := func(e events.Event) error {
		conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		return conn.WriteJSON(e)
	}
	for _, l := range lines {
		if send(events.New(events.ConsoleOutput, srv.ID, map[string]string{"line": l})) != nil {
			return
		}
	}
	if send(events.New(events.ServerStatus, srv.ID, map[string]string{"status": srv.Status})) != nil {
		return
	}

	// Reader: input messages and pongs.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		conn.SetReadLimit(wsMaxMessage)
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(wsPongWait))
		})
		for {
			var msg consoleInput
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Type == "input" && msg.Data != "" {
				if err := inst.WriteConsole([]byte(msg.Data)); err != nil {
					send(events.New(events.ConsoleOutput, srv.ID,
						map[string]string{"line": "[mcsman] server is not running"}))
				}
			}
		}
	}()

	ping := time.NewTicker(wsPingPeriod)
	defer ping.Stop()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			if e.ServerID != srv.ID || !consoleEventTypes[e.Type] {
				continue
			}
			if send(e) != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if conn.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		case <-readDone:
			return
		}
	}
}

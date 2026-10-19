package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"mcsman/internal/events"
)

// Events every authenticated user may receive, and the extra ones only
// admins see. Console output is never sent here (use the console socket).
var (
	publicEventTypes = map[string]bool{
		events.ServerStatus: true, events.Metrics: true,
		events.PlayerJoin: true, events.PlayerLeave: true,
	}
	adminEventTypes = map[string]bool{
		events.BackupProgress: true, events.UpgradeProgress: true,
		events.BackupComplete: true, events.BackupFailed: true, events.ServerCrash: true,
		events.UpgradeComplete: true,
	}
)

// handleEvents streams live events: every server's for admins, member
// servers' for players. Auth: ?ticket= from /api/ws-ticket (or the Unix
// socket).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		p = s.tickets.redeem(r.URL.Query().Get("ticket"))
	}
	if p == nil {
		writeError(w, http.StatusUnauthorized, "invalid or missing ticket")
		return
	}
	admin := p.IsAdmin()
	var member map[string]bool
	loadMembers := func() {
		ids, err := s.db.ServersForUser(context.WithoutCancel(r.Context()), p.User.ID)
		if err != nil {
			s.log.Error("load memberships", "err", err)
		}
		member = make(map[string]bool, len(ids))
		for _, id := range ids {
			member[id] = true
		}
	}
	if !admin {
		loadMembers()
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	ch, cancel := s.bus.Subscribe(256)
	defer cancel()

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		conn.SetReadLimit(512)
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(wsPongWait)) })
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
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
			if e.Type == events.MembersChanged {
				if !admin {
					loadMembers()
				}
				continue
			}
			if !publicEventTypes[e.Type] && !(admin && adminEventTypes[e.Type]) {
				continue
			}
			if !admin && !member[e.ServerID] {
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if conn.WriteJSON(e) != nil {
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

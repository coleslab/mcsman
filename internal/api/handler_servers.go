package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/db"
	"mcsman/internal/server"
)

// serverView is the admin representation of a server.
type serverView struct {
	db.Server
	PID         int      `json:"pid,omitempty"`
	PlayerCount int      `json:"player_count"`
	Players     []string `json:"players"`
}

// playerServerView is what players may see.
type playerServerView struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	MCVersion         string  `json:"mc_version"`
	ConnectionAddress *string `json:"connection_address"`
	PlayerCount       int     `json:"player_count"`
}

func viewOf(inst *server.Instance) serverView {
	players := inst.Players()
	return serverView{Server: inst.Server(), PID: inst.PID(), PlayerCount: len(players), Players: players}
}

// instance resolves the {id} URL param (ID or name).
func (s *Server) instance(w http.ResponseWriter, r *http.Request) (*server.Instance, bool) {
	inst, err := s.servers.Lookup(chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return nil, false
	}
	return inst, true
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	list := s.servers.List()
	out := make([]serverView, 0, len(list))
	for _, srv := range list {
		inst, err := s.servers.Get(srv.ID)
		if err != nil {
			continue
		}
		out = append(out, viewOf(inst))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	v := viewOf(inst)
	if principalFrom(r.Context()).IsAdmin() {
		writeJSON(w, http.StatusOK, v)
		return
	}
	writeJSON(w, http.StatusOK, playerServerView{
		ID: v.ID, Name: v.Name, Status: v.Status, MCVersion: v.MCVersion,
		ConnectionAddress: v.ConnectionAddress, PlayerCount: v.PlayerCount,
	})
}

func (s *Server) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var p server.CreateParams
	if !decode(w, r, &p) {
		return
	}
	srv, err := s.servers.Create(r.Context(), p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, srv)
}

func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var p server.UpdateParams
	if !decode(w, r, &p) {
		return
	}
	srv, err := s.servers.Update(r.Context(), inst.Server().ID, p)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

// handleDeleteServer requires {"confirm": "<server name>"} in the body.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	srv := inst.Server()
	if req.Confirm != srv.Name {
		writeError(w, http.StatusBadRequest, `set "confirm" to the server name to delete it`)
		return
	}
	if err := s.servers.Delete(r.Context(), srv.ID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("server deleted", "server", srv.Name, "by", principalFrom(r.Context()).User.Username)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	if err := inst.Start(); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(inst))
}

type stopRequest struct {
	Graceful     *bool `json:"graceful"`
	DelaySeconds int   `json:"delay_seconds"`
}

// runLifecycle runs a stop/restart either synchronously (?wait=true, used by
// mcsctl) or in the background, returning 202.
func (s *Server) runLifecycle(w http.ResponseWriter, r *http.Request, inst *server.Instance, fn func(context.Context) error) {
	if r.URL.Query().Get("wait") == "true" {
		if err := fn(context.WithoutCancel(r.Context())); err != nil {
			s.writeErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(inst))
		return
	}
	go func() {
		if err := fn(context.Background()); err != nil {
			s.log.Error("lifecycle action failed", "server", inst.Server().Name, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, viewOf(inst))
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req stopRequest
	if !decodeOptional(w, r, &req) {
		return
	}
	delay, ok := parseDelay(w, req.DelaySeconds)
	if !ok {
		return
	}
	if req.Graceful != nil && !*req.Graceful {
		delay = 0
	}
	s.runLifecycle(w, r, inst, func(ctx context.Context) error {
		return inst.Stop(ctx, server.StopOptions{Delay: delay})
	})
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req stopRequest
	if !decodeOptional(w, r, &req) {
		return
	}
	delay, ok := parseDelay(w, req.DelaySeconds)
	if !ok {
		return
	}
	s.runLifecycle(w, r, inst, func(ctx context.Context) error {
		return inst.Restart(ctx, delay)
	})
}

func parseDelay(w http.ResponseWriter, secs int) (time.Duration, bool) {
	if secs < 0 || secs > 3600 {
		writeError(w, http.StatusBadRequest, "delay_seconds must be 0-3600")
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		Command string `json:"command"`
	}
	if !decode(w, r, &req) {
		return
	}
	cmd := strings.TrimPrefix(strings.TrimSpace(req.Command), "/")
	if cmd == "" || strings.ContainsAny(cmd, "\r\n") {
		writeError(w, http.StatusBadRequest, "command must be a single non-empty line")
		return
	}
	out, err := inst.Exec(cmd)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func (s *Server) handlePlayers(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": inst.Players()})
}

func (s *Server) handleServerEvents(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	evs, err := s.db.ListServerEvents(r.Context(), inst.Server().ID, 100)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if evs == nil {
		evs = []*db.ServerEvent{}
	}
	writeJSON(w, http.StatusOK, evs)
}

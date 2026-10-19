package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/auth"
	"mcsman/internal/db"
	"mcsman/internal/events"
)

// lookupUser resolves a user ID or username (mcsctl passes names).
func (s *Server) lookupUser(ctx context.Context, key string) (*db.User, error) {
	if u, err := s.db.UserByID(ctx, key); err == nil {
		return u, nil
	}
	return s.db.UserByUsername(ctx, key)
}

func (s *Server) membersChanged(serverID string) {
	s.bus.Publish(events.New(events.MembersChanged, serverID, nil))
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	list, err := s.db.ListMembers(r.Context(), inst.Server().ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleAddMember makes an existing user a member of the server and
// whitelists their Minecraft name.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"` // ID or username
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.lookupUser(r.Context(), req.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown user")
		return
	}
	srv := inst.Server()
	applied, err := s.whitelist.AddMember(r.Context(), srv.ID, u, userID(r))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.membersChanged(srv.ID)
	s.log.Info("member added", "server", srv.Name, "user", u.Username, "by", principalFrom(r.Context()).User.Username)
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": u.ID, "username": u.Username, "queued": !applied})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	u, err := s.lookupUser(r.Context(), chi.URLParam(r, "user"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	srv := inst.Server()
	if err := s.whitelist.RemoveMember(r.Context(), srv.ID, u); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.membersChanged(srv.ID)
	s.log.Info("member removed", "server", srv.Name, "user", u.Username, "by", principalFrom(r.Context()).User.Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleAcceptInvite applies a server invite to the caller's existing
// account. [auth, rate-limited]
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.User.ID == "" {
		writeError(w, http.StatusBadRequest, "not available over the CLI socket")
		return
	}
	var req struct {
		MCUsername string `json:"mc_username"`
	}
	if !decodeOptional(w, r, &req) {
		return
	}
	u := *p.User
	inv, err := s.invites.Accept(r.Context(), chi.URLParam(r, "token"), &u, req.MCUsername)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("invite accepted", "username", u.Username, "ip", clientIP(r))
	serverID := *inv.WhitelistServerID
	s.membersChanged(serverID)
	var wl *whitelistedServer
	if inst, err := s.servers.Get(serverID); err == nil {
		// The membership exists now; a whitelist failure is logged and left
		// queued rather than failing the request.
		applied, err := s.whitelist.Add(context.WithoutCancel(r.Context()), serverID, *u.MCUsername)
		if err != nil {
			s.log.Error("whitelist on accept", "err", err)
		}
		srv := inst.Server()
		wl = &whitelistedServer{Name: srv.Name, ConnectionAddress: srv.ConnectionAddress, Queued: !applied}
	}
	writeJSON(w, http.StatusOK, map[string]any{"whitelisted_server": wl})
}

// handleUpdateMe lets a user set their own Minecraft username.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.User.ID == "" {
		writeError(w, http.StatusBadRequest, "not available over the CLI socket")
		return
	}
	var req struct {
		MCUsername *string `json:"mc_username"` // "" clears it
	}
	if !decode(w, r, &req) {
		return
	}
	u := *p.User
	if req.MCUsername != nil {
		if err := s.setMCUsername(r.Context(), &u, *req.MCUsername); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, &u)
}

// setMCUsername saves u's Minecraft name ("" clears it) and moves their
// whitelist entries on the servers they belong to.
func (s *Server) setMCUsername(ctx context.Context, u *db.User, mc string) error {
	mc = strings.TrimSpace(mc)
	var next *string
	if mc != "" {
		if !auth.ValidMCUsername(mc) {
			return fmt.Errorf("%w: Minecraft username must be 3-16 letters, numbers or _", auth.ErrInvalidInput)
		}
		next = &mc
	}
	prev := u.MCUsername
	if (prev == nil && next == nil) || (prev != nil && next != nil && *prev == *next) {
		return nil
	}
	if err := s.db.SetMCUsername(ctx, u.ID, next); err != nil {
		return err
	}
	u.MCUsername = next
	if err := s.whitelist.Rename(context.WithoutCancel(ctx), u.ID, prev, next); err != nil {
		s.log.Error("whitelist rename", "user", u.Username, "err", err)
	}
	return nil
}

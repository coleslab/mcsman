package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/auth"
	"mcsman/internal/db"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if users == nil {
		users = []*db.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

// userParam resolves {id} as a user ID or username (mcsctl passes names).
func (s *Server) userParam(r *http.Request) (*db.User, error) {
	key := chi.URLParam(r, "id")
	if u, err := s.db.UserByID(r.Context(), key); err == nil {
		return u, nil
	}
	return s.db.UserByUsername(r.Context(), key)
}

// handleUpdateUser changes a user's role and/or Minecraft username. The
// last admin cannot be demoted.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role       string  `json:"role"`
		MCUsername *string `json:"mc_username"` // "" clears it
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Role != "" && req.Role != db.RoleAdmin && req.Role != db.RolePlayer {
		writeError(w, http.StatusBadRequest, `role must be "admin" or "player"`)
		return
	}
	u, err := s.userParam(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if req.MCUsername != nil {
		if err := s.setMCUsername(r.Context(), u, *req.MCUsername); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if req.Role == "" || req.Role == u.Role {
		writeJSON(w, http.StatusOK, u)
		return
	}
	if u.Role == db.RoleAdmin && req.Role == db.RolePlayer {
		if n, err := s.db.CountAdmins(r.Context()); err != nil || n <= 1 {
			writeError(w, http.StatusConflict, "cannot demote the last admin")
			return
		}
	}
	if err := s.db.SetUserRole(r.Context(), u.ID, req.Role); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("user role changed", "user", u.Username, "role", req.Role, "by", principalFrom(r.Context()).User.Username)
	u.Role = req.Role
	writeJSON(w, http.StatusOK, u)
}

// handleDeleteUser removes a user (sessions cascade). Admins cannot delete
// themselves or the last admin.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.userParam(r)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if p := principalFrom(r.Context()); p.User.ID == u.ID {
		writeError(w, http.StatusConflict, "you cannot delete your own account")
		return
	}
	if u.Role == db.RoleAdmin {
		if n, err := s.db.CountAdmins(r.Context()); err != nil || n <= 1 {
			writeError(w, http.StatusConflict, "cannot delete the last admin")
			return
		}
	}
	if err := s.whitelist.RemoveUser(r.Context(), u); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.db.DeleteUser(r.Context(), u.ID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("user deleted", "user", u.Username, "by", principalFrom(r.Context()).User.Username)
	w.WriteHeader(http.StatusNoContent)
}

// handleChangePassword changes the caller's password and signs out their
// other sessions.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.User.ID == "" {
		writeError(w, http.StatusBadRequest, "not available over the CLI socket")
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !auth.CheckPassword(p.User.PasswordHash, req.Current) {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.db.SetPasswordHash(r.Context(), p.User.ID, hash); err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.db.RevokeOtherSessions(r.Context(), p.User.ID, p.SessionID); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

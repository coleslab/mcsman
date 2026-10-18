package api

import (
	"errors"
	"net/http"
	"strings"

	"mcsman/internal/auth"
	"mcsman/internal/db"
)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleInit creates the first admin. Only allowed over the Unix socket and
// only while no users exist.
func (s *Server) handleInit(w http.ResponseWriter, r *http.Request) {
	if !viaSocket(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req credentials
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !auth.ValidUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 1-32 characters")
		return
	}
	n, err := s.db.CountUsers(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, "already initialised: users exist")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	u := &db.User{Username: req.Username, PasswordHash: hash, Role: db.RoleAdmin}
	if err := s.db.CreateUser(r.Context(), u); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("first admin created", "username", u.Username)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credentials
	if !decode(w, r, &req) {
		return
	}
	tok, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.log.Info("login failed", "ip", clientIP(r))
		}
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	tok, err := s.auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	// The spec returns only access_token; the rotated refresh token is
	// included too since the old one is now invalid.
	writeJSON(w, http.StatusOK, tok)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.SessionID != "" {
		if err := s.auth.Logout(r.Context(), p.SessionID); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, principalFrom(r.Context()).User)
}

func (s *Server) handleWSTicket(w http.ResponseWriter, r *http.Request) {
	t, err := s.tickets.issue(principalFrom(r.Context()))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": t})
}

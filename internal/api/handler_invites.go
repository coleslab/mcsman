package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/auth"
	"mcsman/internal/config"
	"mcsman/internal/db"
)

type inviteView struct {
	*db.Invite
	ServerName *string `json:"whitelist_server_name"`
}

func (s *Server) serverName(id *string) *string {
	if id == nil {
		return nil
	}
	inst, err := s.servers.Get(*id)
	if err != nil {
		return nil
	}
	n := inst.Server().Name
	return &n
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExpiresIn         string  `json:"expires_in"` // e.g. "48h", "7d"; empty = default
		WhitelistServerID *string `json:"whitelist_server_id"`
	}
	if !decodeOptional(w, r, &req) {
		return
	}
	var ttl time.Duration
	if req.ExpiresIn != "" {
		d, err := config.ParseDuration(req.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, `expires_in must be a duration like "48h" or "7d"`)
			return
		}
		ttl = d
	}
	if req.WhitelistServerID != nil {
		if *req.WhitelistServerID == "" {
			req.WhitelistServerID = nil
		} else {
			// Accept a server name too (mcsctl passes names).
			inst, err := s.servers.Lookup(*req.WhitelistServerID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "unknown whitelist server")
				return
			}
			id := inst.Server().ID
			req.WhitelistServerID = &id
		}
	}
	c, err := s.invites.Create(r.Context(), userID(r), ttl, req.WhitelistServerID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("invite created", "by", principalFrom(r.Context()).User.Username, "expires_at", c.Invite.ExpiresAt)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": c.Invite.ID, "token": c.Token, "url": c.URL, "expires_at": c.Invite.ExpiresAt,
		"whitelist_server_name": s.serverName(c.Invite.WhitelistServerID),
	})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.PendingInvites(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]inviteView, 0, len(list))
	for _, i := range list {
		out = append(out, inviteView{Invite: i, ServerName: s.serverName(i.WhitelistServerID)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.db.RevokeInvite(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInviteLookup lets the join page show what an invite is for before
// the user fills in the form. [public, rate-limited]
func (s *Server) handleInviteLookup(w http.ResponseWriter, r *http.Request) {
	inv, err := s.invites.Lookup(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"expires_at":            inv.ExpiresAt,
		"whitelist_server_name": s.serverName(inv.WhitelistServerID),
		"requires_mc_username":  inv.WhitelistServerID != nil,
	})
}

type whitelistedServer struct {
	Name              string  `json:"name"`
	ConnectionAddress *string `json:"connection_address"`
	Queued            bool    `json:"queued"` // applied on the server's next start
}

// handleRedeem creates a player account from an invite. [public, rate-limited]
func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var req auth.RedeemRequest
	if !decode(w, r, &req) {
		return
	}
	u, inv, tok, err := s.invites.Redeem(r.Context(), chi.URLParam(r, "token"), req)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("invite redeemed", "username", u.Username, "ip", clientIP(r))

	var wl *whitelistedServer
	if inv.WhitelistServerID != nil && u.MCUsername != nil {
		if inst, err := s.servers.Get(*inv.WhitelistServerID); err == nil {
			// The account exists now; a whitelist failure must not fail the
			// redemption, so it is logged and left queued.
			applied, err := s.whitelist.Add(context.WithoutCancel(r.Context()), *inv.WhitelistServerID, *u.MCUsername)
			if err != nil {
				s.log.Error("whitelist on redeem", "err", err)
			}
			srv := inst.Server()
			wl = &whitelistedServer{Name: srv.Name, ConnectionAddress: srv.ConnectionAddress, Queued: !applied}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"access_token":       tok.AccessToken,
		"refresh_token":      tok.RefreshToken,
		"whitelisted_server": wl,
	})
}

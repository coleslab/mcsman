package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/db"
	"mcsman/internal/discord"
	"mcsman/internal/server"
)

type webhookView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	URL         string          `json:"url_masked"`
	Events      map[string]bool `json:"events"`
	AllServers  bool            `json:"all_servers"`
	ServerIDs   []string        `json:"server_ids"`
	ServerNames []string        `json:"server_names"`
	CreatedAt   int64           `json:"created_at"`
}

func (s *Server) webhookView(h *db.Webhook) webhookView {
	masked := "(unreadable — re-enter the URL)"
	if raw, err := s.discord.Sealer.Open(h.URLEnc); err == nil {
		masked = discord.Mask(raw)
	}
	names := []string{}
	for _, id := range h.ServerIDs {
		if inst, err := s.servers.Get(id); err == nil {
			names = append(names, inst.Server().Name)
		}
	}
	return webhookView{ID: h.ID, Name: h.Name, URL: masked, Events: h.Events,
		AllServers: h.AllServers, ServerIDs: h.ServerIDs, ServerNames: names, CreatedAt: h.CreatedAt}
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListWebhooks(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := make([]webhookView, 0, len(list))
	for _, h := range list {
		out = append(out, s.webhookView(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out, "event_keys": discord.EventKeys})
}

type webhookRequest struct {
	Name      *string         `json:"name"`
	URL       *string         `json:"url"`
	Events    map[string]bool `json:"events"`
	ServerIDs *[]string       `json:"server_ids"` // IDs or names; [] = all servers
}

// applyScope sets a webhook's scope from a request's server_ids.
func (s *Server) applyScope(h *db.Webhook, ids []string) error {
	resolved, err := s.resolveServers(ids)
	if err != nil {
		return err
	}
	h.AllServers, h.ServerIDs = len(resolved) == 0, resolved
	return nil
}

// resolveServers maps server IDs or names to IDs, rejecting unknown ones.
func (s *Server) resolveServers(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, key := range in {
		inst, err := s.servers.Lookup(key)
		if err != nil {
			return nil, fmt.Errorf("%w: unknown server %q", server.ErrInvalid, key)
		}
		if id := inst.Server().ID; !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// normalizeEvents keeps only known keys.
func normalizeEvents(in map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, k := range discord.EventKeys {
		out[k] = in[k]
	}
	return out
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req webhookRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil || strings.TrimSpace(*req.Name) == "" || req.URL == nil {
		writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	if err := discord.ValidateURL(strings.TrimSpace(*req.URL)); err != nil {
		s.writeErr(w, r, err)
		return
	}
	enc, err := s.discord.Sealer.Seal(strings.TrimSpace(*req.URL))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	events := discord.DefaultEvents
	if req.Events != nil {
		events = req.Events
	}
	h := &db.Webhook{Name: strings.TrimSpace(*req.Name), URLEnc: enc, Events: normalizeEvents(events), AllServers: true}
	if req.ServerIDs != nil {
		if err := s.applyScope(h, *req.ServerIDs); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.db.CreateWebhook(r.Context(), h); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "a webhook with that name exists")
			return
		}
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.webhookView(h))
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	h, err := s.db.WebhookByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var req webhookRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		h.Name = strings.TrimSpace(*req.Name)
	}
	if req.URL != nil && strings.TrimSpace(*req.URL) != "" {
		if err := discord.ValidateURL(strings.TrimSpace(*req.URL)); err != nil {
			s.writeErr(w, r, err)
			return
		}
		if h.URLEnc, err = s.discord.Sealer.Seal(strings.TrimSpace(*req.URL)); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if req.Events != nil {
		h.Events = normalizeEvents(req.Events)
	}
	if req.ServerIDs != nil {
		if err := s.applyScope(h, *req.ServerIDs); err != nil {
			s.writeErr(w, r, err)
			return
		}
	}
	if err := s.db.UpdateWebhook(r.Context(), h); err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.webhookView(h))
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if err := s.db.DeleteWebhook(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	if err := s.discord.Test(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			s.writeErr(w, r, err)
			return
		}
		writeError(w, http.StatusBadGateway, "test failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

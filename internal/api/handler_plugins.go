package api

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/db"
	"mcsman/internal/plugin"
	"mcsman/internal/version"
)

// playerPlugin is what members see of an installed plugin.
type playerPlugin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.memberInstance(w, r)
	if !ok {
		return
	}
	list, err := s.plugins.List(r.Context(), inst.Server().ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if !principalFrom(r.Context()).IsAdmin() {
		out := make([]playerPlugin, 0, len(list))
		for _, p := range list {
			out = append(out, playerPlugin{Name: p.Name, Version: p.Version})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleSearchPlugins searches Modrinth and Hangar in parallel and merges
// the results. ?source= limits to one registry.
func (s *Server) handleSearchPlugins(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mc := r.URL.Query().Get("mc_version")
	only := r.URL.Query().Get("source")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	type res struct {
		source  string
		results []plugin.SearchResult
		err     error
	}
	var wg sync.WaitGroup
	out := make(chan res, len(s.plugins.Registries))
	for name, reg := range s.plugins.Registries {
		if only != "" && only != name {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs, err := reg.Search(r.Context(), q, mc, 10)
			out <- res{name, rs, err}
		}()
	}
	wg.Wait()
	close(out)
	merged := []plugin.SearchResult{}
	errs := map[string]string{}
	for x := range out {
		if x.err != nil {
			s.log.Warn("plugin search failed", "source", x.source, "err", x.err)
			errs[x.source] = "unavailable"
			continue
		}
		merged = append(merged, x.results...)
	}
	// Interleave by popularity across sources.
	slices.SortStableFunc(merged, func(a, b plugin.SearchResult) int { return cmp.Compare(b.Downloads, a.Downloads) })
	writeJSON(w, http.StatusOK, map[string]any{"results": merged, "errors": errs})
}

func (s *Server) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		Source   string `json:"source"`
		SourceID string `json:"source_id"`
		Version  string `json:"version"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.SourceID == "" {
		writeError(w, http.StatusBadRequest, "source_id is required")
		return
	}
	p, err := s.plugins.Install(r.Context(), inst.Server().ID, req.Source, req.SourceID, req.Version)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePlugin(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	p, updated, err := s.plugins.Update(r.Context(), inst.Server().ID, chi.URLParam(r, "plugin_id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugin": p, "updated": updated})
}

func (s *Server) handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	if err := s.plugins.Remove(r.Context(), inst.Server().ID, chi.URLParam(r, "plugin_id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePluginUpdates reports, per registry plugin, the newest release for
// the server's current Minecraft version.
func (s *Server) handlePluginUpdates(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	srv := inst.Server()
	list, err := s.plugins.List(r.Context(), srv.ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	type update struct {
		PluginID string `json:"plugin_id"`
		Current  string `json:"current"`
		Latest   string `json:"latest"`
	}
	out := []update{}
	for _, c := range s.plugins.Check(r.Context(), list, srv.MCVersion) {
		if c.Status == plugin.Green && c.Release.Version != c.Plugin.Version {
			out = append(out, update{c.Plugin.ID, c.Plugin.Version, c.Release.Version})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type upgradeRequest struct {
	TargetVersion string `json:"target_version"`
	DelaySeconds  *int   `json:"delay_seconds"`
}

func (s *Server) handleUpgradePreflight(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req upgradeRequest
	if !decode(w, r, &req) {
		return
	}
	p, err := s.upgrader.Preflight(r.Context(), inst.Server().ID, req.TargetVersion)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpgradeConfirm(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req upgradeRequest
	if !decode(w, r, &req) {
		return
	}
	// Default countdown: 60s if anyone is online, otherwise none.
	delay := 0
	if req.DelaySeconds != nil {
		delay = *req.DelaySeconds
	} else if inst.Status() == db.StatusRunning && len(inst.Players()) > 0 {
		delay = 60
	}
	d, ok := parseDelay(w, delay)
	if !ok {
		return
	}
	j, err := s.upgrader.Confirm(r.Context(), inst.Server().ID, req.TargetVersion,
		version.ConfirmOptions{Delay: d, CreatedBy: userID(r)})
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("upgrade started", "server", inst.Server().Name, "to", req.TargetVersion, "by", principalFrom(r.Context()).User.Username)
	writeJSON(w, http.StatusAccepted, j)
}

func (s *Server) handleUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	j := s.upgrader.Job(inst.Server().ID)
	if j == nil {
		writeJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j})
}

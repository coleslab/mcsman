package api

import (
	"net/http"
	"slices"
	"strings"
)

type publicServer struct {
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	PlayerCount       int     `json:"player_count"`
	Version           string  `json:"version"`
	ConnectionAddress *string `json:"connection_address"`
}

// handleStatus is the unauthenticated status feed for the public page. It
// exposes only what players need to connect. [public]
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	all := s.servers.All()
	out := make([]publicServer, 0, len(all))
	for _, inst := range all {
		srv := inst.Server()
		count := len(inst.Players())
		if m := inst.Metrics(); m != nil {
			count = m.PlayerCount
		}
		out = append(out, publicServer{
			Name: srv.Name, Status: srv.Status, PlayerCount: count,
			Version: srv.MCVersion, ConnectionAddress: srv.ConnectionAddress,
		})
	}
	slices.SortFunc(out, func(a, b publicServer) int { return strings.Compare(a.Name, b.Name) })
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

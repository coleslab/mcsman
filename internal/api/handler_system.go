package api

import (
	"net/http"
	"syscall"

	"mcsman/internal/buildinfo"
)

// handleSystem reports the daemon's version and effective settings for
// mcsctl version/doctor. Socket only: it reveals configuration. [socket]
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if !viaSocket(r) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	users, err := s.db.CountUsers(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	out := map[string]any{
		"version":         buildinfo.Version,
		"users":           users,
		"listen":          s.cfg.HTTP.Listen,
		"base_url":        s.cfg.HTTP.BaseURL,
		"trusted_proxies": s.cfg.HTTP.TrustedProxies,
		"data_dir":        s.cfg.Daemon.DataDir,
	}
	var st syscall.Statfs_t
	if s.cfg.Daemon.DataDir != "" && syscall.Statfs(s.cfg.Daemon.DataDir, &st) == nil {
		out["data_free_bytes"] = st.Bavail * uint64(st.Bsize)
	}
	writeJSON(w, http.StatusOK, out)
}

package api

import (
	"context"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/backup"
)

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	list, err := s.backups.List(r.Context(), inst.Server().ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// userID returns the caller's user ID for created_by columns, or nil for
// the Unix-socket principal (which has no user row).
func userID(r *http.Request) *string {
	p := principalFrom(r.Context())
	if p == nil || p.User.ID == "" {
		return nil
	}
	id := p.User.ID
	return &id
}

// handleCreateBackup runs the backup in the background (progress arrives as
// backup_progress events) unless ?wait=true.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		Tag string `json:"tag"`
	}
	if !decodeOptional(w, r, &req) {
		return
	}
	if req.Tag == "" {
		req.Tag = "manual"
	}
	opts := backup.Options{Tag: req.Tag, CreatedBy: userID(r)}
	id := inst.Server().ID
	if r.URL.Query().Get("wait") == "true" {
		b, err := s.backups.Create(context.WithoutCancel(r.Context()), id, opts)
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, b)
		return
	}
	go func() {
		// Errors are logged and published as backup_failed by the manager.
		s.backups.Create(context.Background(), id, opts)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	err := s.backups.Restore(context.WithoutCancel(r.Context()), inst.Server().ID, chi.URLParam(r, "backup_id"), userID(r))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("backup restored", "server", inst.Server().Name, "by", principalFrom(r.Context()).User.Username)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	if err := s.backups.Delete(r.Context(), inst.Server().ID, chi.URLParam(r, "backup_id")); err != nil {
		s.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	b, path, err := s.backups.File(r.Context(), inst.Server().ID, chi.URLParam(r, "backup_id"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	name := inst.Server().Name + "_" + b.Filename
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Checksum-SHA256", b.SHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) handlePaperVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := s.versions.Versions(r.Context(), r.URL.Query().Get("refresh") == "true")
	if err != nil {
		s.log.Warn("fetch paper versions", "err", err)
		writeError(w, http.StatusBadGateway, "could not reach PaperMC")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

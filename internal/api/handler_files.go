package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mcsman/internal/db"
	"mcsman/internal/serverfiles"
)

// maxFileBody fits a MaxSize file even with every byte JSON-escaped.
const maxFileBody = 7 * serverfiles.MaxSize

func (s *Server) writeFileErr(w http.ResponseWriter, r *http.Request, err error) {
	var se *serverfiles.SyntaxError
	switch {
	case errors.As(err, &se):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": se.Error(), "line": se.Line})
	case errors.Is(err, serverfiles.ErrNotEditable), errors.Is(err, serverfiles.ErrNotText):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, serverfiles.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, serverfiles.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "file not found")
	default:
		s.writeErr(w, r, err)
	}
}

func (s *Server) handleListFiles(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	files, err := serverfiles.List(inst.Server().Directory)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (s *Server) handleReadFile(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	content, sha, err := serverfiles.Read(inst.Server().Directory, rel)
	if err != nil {
		s.writeFileErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": rel, "content": string(content), "sha256": sha})
}

// handleWriteFile saves a config file, keeping the previous content as a
// revision. base_sha256 (from the read) guards against overwriting a change
// made on disk meanwhile, e.g. by a plugin saving its config.
func (s *Server) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	var req struct {
		Content    string `json:"content"`
		BaseSHA256 string `json:"base_sha256"`
		Force      bool   `json:"force"` // save despite a syntax error
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFileBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	srv := inst.Server()
	rel := r.URL.Query().Get("path")

	s.filesMu.Lock()
	prev, sha, err := serverfiles.Write(srv.Directory, rel, []byte(req.Content), req.BaseSHA256, req.Force)
	s.filesMu.Unlock()
	if err != nil {
		s.writeFileErr(w, r, err)
		return
	}
	if err := s.db.AddConfigRevision(r.Context(), &db.ConfigRevision{
		ServerID: srv.ID, Path: rel, SHA256: req.BaseSHA256, Content: prev, UserID: userID(r),
	}); err != nil {
		s.log.Error("save config revision", "server", srv.Name, "path", rel, "err", err)
	}
	by := principalFrom(r.Context()).User.Username
	detail, _ := json.Marshal(map[string]string{"path": rel, "by": by})
	ds := string(detail)
	if err := s.db.AddServerEvent(r.Context(), srv.ID, "config_edit", &ds); err != nil {
		s.log.Error("record config edit", "err", err)
	}
	s.log.Info("config file edited", "server", srv.Name, "path", rel, "by", by)
	writeJSON(w, http.StatusOK, map[string]any{
		"sha256":           sha,
		"restart_required": inst.Status() != db.StatusStopped && inst.Status() != db.StatusCrashed,
	})
}

func (s *Server) handleFileRevisions(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	revs, err := s.db.ListConfigRevisions(r.Context(), inst.Server().ID, r.URL.Query().Get("path"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, revs)
}

// handleFileRevision returns a revision's content. Restoring is done by
// saving it through handleWriteFile, with the usual checks.
func (s *Server) handleFileRevision(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	rev, err := s.db.ConfigRevisionByID(r.Context(), inst.Server().ID, chi.URLParam(r, "rev"))
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": rev.ID, "path": rev.Path, "content": string(rev.Content), "created_at": rev.CreatedAt,
	})
}

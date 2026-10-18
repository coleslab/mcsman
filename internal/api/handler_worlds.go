package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcsman/internal/backup"
	"mcsman/internal/world"
)

const maxWorldUpload = 8 << 30 // 8 GiB

// handleImportWorld accepts multipart form data: a "world" zip file plus
// optional "source_name" and "force" fields. The file is streamed to disk.
func (s *Server) handleImportWorld(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorldUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	tmpDir := filepath.Join(s.servers.DataDir(), "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		s.writeErr(w, r, err)
		return
	}
	var zipPath, sourceName string
	force := false
	defer func() {
		if zipPath != "" {
			os.Remove(zipPath)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "upload failed: "+err.Error())
			return
		}
		switch part.FormName() {
		case "world":
			f, err := os.CreateTemp(tmpDir, "upload-*.zip")
			if err != nil {
				s.writeErr(w, r, err)
				return
			}
			zipPath = f.Name()
			_, err = io.Copy(f, part)
			f.Close()
			if err != nil {
				writeError(w, http.StatusBadRequest, "upload failed: "+err.Error())
				return
			}
		case "source_name":
			b, _ := io.ReadAll(io.LimitReader(part, 200))
			sourceName = strings.TrimSpace(string(b))
		case "force":
			b, _ := io.ReadAll(io.LimitReader(part, 10))
			force = strings.TrimSpace(string(b)) == "true"
		}
		part.Close()
	}
	if zipPath == "" {
		writeError(w, http.StatusBadRequest, `missing "world" file`)
		return
	}
	srv := inst.Server()
	wi, err := s.worlds.Import(context.WithoutCancel(r.Context()), srv.ID, zipPath,
		world.ImportOptions{SourceName: sourceName, ImportedBy: userID(r), Force: force})
	if err != nil {
		if errors.Is(err, world.ErrNewerWorld) {
			in, _ := s.worlds.Inspect(zipPath, srv.ID)
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "inspection": in})
			return
		}
		s.writeErr(w, r, err)
		return
	}
	s.log.Info("world imported", "server", srv.Name, "by", principalFrom(r.Context()).User.Username)
	writeJSON(w, http.StatusCreated, wi)
}

func (s *Server) handleExportWorld(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	srv := inst.Server()
	if _, err := backup.WorldDirs(srv.Directory); err != nil {
		s.writeErr(w, r, err)
		return
	}
	name := srv.Name + "-world-" + time.Now().Format("2006-01-02") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := s.worlds.Export(r.Context(), srv.ID, w); err != nil {
		// Headers may be sent already; log and abort the stream.
		s.log.Error("world export failed", "server", srv.Name, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) handleWorldImports(w http.ResponseWriter, r *http.Request) {
	inst, ok := s.instance(w, r)
	if !ok {
		return
	}
	list, err := s.db.ListWorldImports(r.Context(), inst.Server().ID)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

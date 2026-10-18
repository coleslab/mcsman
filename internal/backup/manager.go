// Package backup creates, prunes and restores world backups.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/fsutil"
	"mcsman/internal/server"
)

var (
	ErrInProgress = fmt.Errorf("%w: a backup or restore is already running for this server", server.ErrConflict)
	tagRe         = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// Manager orchestrates the backup lifecycle.
type Manager struct {
	DB      *db.DB
	Servers *server.Manager
	Bus     *events.Bus
	Log     *slog.Logger
	DataDir string
	Keep    int

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (m *Manager) lock(serverID string) (func(), bool) {
	m.mu.Lock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	l, ok := m.locks[serverID]
	if !ok {
		l = &sync.Mutex{}
		m.locks[serverID] = l
	}
	m.mu.Unlock()
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

// Dir is the backup directory for a server.
func (m *Manager) Dir(srv db.Server) (string, error) {
	return fsutil.SafePath(filepath.Join(m.DataDir, "backups"), filepath.Base(srv.Directory))
}

// Options for Create.
type Options struct {
	Tag       string  // "auto", "manual", "pre-upgrade-1.21.1", ...
	CreatedBy *string // user ID; nil for scheduled/system backups
}

// Create takes a backup of a server's world directories. For a running
// server it follows the flush sequence: save-all flush, save-off, archive,
// checksum, save-on (always, even on failure).
func (m *Manager) Create(ctx context.Context, serverID string, opts Options) (*db.Backup, error) {
	inst, err := m.Servers.Get(serverID)
	if err != nil {
		return nil, err
	}
	unlock, ok := m.lock(serverID)
	if !ok {
		return nil, ErrInProgress
	}
	defer unlock()
	return m.create(ctx, inst, opts)
}

func (m *Manager) create(ctx context.Context, inst *server.Instance, opts Options) (b *db.Backup, err error) {
	srv := inst.Server()
	defer func() {
		if err != nil {
			m.Log.Error("backup failed", "server", srv.Name, "err", err)
			m.Bus.Publish(events.New(events.BackupFailed, srv.ID, map[string]string{"error": err.Error()}))
			m.logEvent(srv.ID, map[string]any{"ok": false, "error": err.Error(), "tag": opts.Tag})
		}
	}()

	status := inst.Status()
	running := status == db.StatusRunning
	switch status {
	case db.StatusRunning, db.StatusStopped, db.StatusCrashed:
	default:
		return nil, fmt.Errorf("%w: cannot back up while server is %s", server.ErrConflict, status)
	}

	worlds, err := WorldDirs(srv.Directory)
	if err != nil {
		return nil, err
	}
	dir, err := m.Dir(srv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}

	progress := func(phase string, n int64) {
		m.Bus.Publish(events.New(events.BackupProgress, srv.ID, map[string]any{"phase": phase, "bytes_written": n}))
	}

	// 1-2: flush and disable auto-save. 5 (save-on) is deferred so it runs
	// on every exit path once save-off has been sent.
	if running {
		progress("flush", 0)
		if _, err := inst.Exec("save-all flush"); err != nil {
			return nil, fmt.Errorf("save-all: %w", err)
		}
		if _, err := inst.Exec("save-off"); err != nil {
			return nil, fmt.Errorf("save-off: %w", err)
		}
		var saveOn sync.Once
		reenable := func() {
			saveOn.Do(func() {
				if _, err := inst.Exec("save-on"); err != nil {
					m.Log.Error("save-on failed; auto-save may be disabled", "server", srv.Name, "err", err)
				}
			})
		}
		defer reenable()
		// archive also calls reenable as soon as the tarball is written,
		// before the slower verify step.
		return m.archive(ctx, srv, dir, worlds, opts, progress, reenable)
	}
	return m.archive(ctx, srv, dir, worlds, opts, progress, func() {})
}

// archive performs steps 3-7: tar.gz, checksum, (save-on), verify, record,
// retention.
func (m *Manager) archive(ctx context.Context, srv db.Server, dir string, worlds []string, opts Options,
	progress func(string, int64), afterWrite func()) (*db.Backup, error) {

	tag := sanitizeTag(opts.Tag)
	ts := time.Now().UTC()
	name := ts.Format("2006-01-02T15-04-05") + "_" + tag + ".tar.gz"
	final, err := fsutil.SafePath(dir, name)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(final); err == nil {
		return nil, fmt.Errorf("%w: a backup named %s already exists", server.ErrConflict, name)
	}

	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())

	progress("archive", 0)
	sum, size, err := writeArchive(tmp, srv.Directory, worlds, func(n int64) { progress("archive", n) })
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	afterWrite() // save-on
	if err != nil {
		return nil, fmt.Errorf("archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	progress("verify", size)
	if _, err := verifyArchive(tmp.Name(), sum); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp.Name(), 0o640); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(final+".sha256", []byte(sum+"  "+name+"\n"), 0o640); err != nil {
		return nil, err
	}

	b := &db.Backup{
		ServerID: srv.ID, Filename: name, SizeBytes: size, SHA256: sum,
		Tag: &tag, CreatedAt: ts.Unix(), CreatedBy: opts.CreatedBy,
	}
	if err := m.DB.CreateBackup(ctx, b); err != nil {
		os.Remove(final)
		os.Remove(final + ".sha256")
		return nil, err
	}
	progress("done", size)
	m.Bus.Publish(events.New(events.BackupComplete, srv.ID, map[string]any{"backup_id": b.ID, "size_bytes": size, "tag": tag}))
	m.logEvent(srv.ID, map[string]any{"ok": true, "backup_id": b.ID, "tag": tag, "size_bytes": size})
	m.Log.Info("backup created", "server", srv.Name, "file", name, "size", size)

	if err := m.prune(ctx, srv, dir); err != nil {
		m.Log.Warn("backup retention failed", "server", srv.Name, "err", err)
	}
	return b, nil
}

// prune deletes the oldest backups beyond the keep limit.
func (m *Manager) prune(ctx context.Context, srv db.Server, dir string) error {
	keep := m.Keep
	if keep < 1 {
		keep = 24
	}
	list, err := m.DB.ListBackups(ctx, srv.ID)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range list[min(keep, len(list)):] {
		errs = append(errs, m.deleteFiles(dir, b), m.DB.DeleteBackup(ctx, b.ID))
	}
	return errors.Join(errs...)
}

func (m *Manager) deleteFiles(dir string, b *db.Backup) error {
	p, err := fsutil.SafePath(dir, b.Filename)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(p + ".sha256"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns a server's backups, newest first.
func (m *Manager) List(ctx context.Context, serverID string) ([]*db.Backup, error) {
	if _, err := m.Servers.Get(serverID); err != nil {
		return nil, err
	}
	return m.DB.ListBackups(ctx, serverID)
}

// File returns the backup record and its absolute path.
func (m *Manager) File(ctx context.Context, serverID, backupID string) (*db.Backup, string, error) {
	inst, err := m.Servers.Get(serverID)
	if err != nil {
		return nil, "", err
	}
	b, err := m.DB.BackupByID(ctx, serverID, backupID)
	if err != nil {
		return nil, "", err
	}
	dir, err := m.Dir(inst.Server())
	if err != nil {
		return nil, "", err
	}
	p, err := fsutil.SafePath(dir, b.Filename)
	if err != nil {
		return nil, "", err
	}
	return b, p, nil
}

// Delete removes a backup's files and record.
func (m *Manager) Delete(ctx context.Context, serverID, backupID string) error {
	unlock, ok := m.lock(serverID)
	if !ok {
		return ErrInProgress
	}
	defer unlock()
	b, p, err := m.File(ctx, serverID, backupID)
	if err != nil {
		return err
	}
	if err := m.deleteFiles(filepath.Dir(p), b); err != nil {
		return err
	}
	return m.DB.DeleteBackup(ctx, b.ID)
}

// Restore replaces a stopped server's world directories with those in a
// backup. The current worlds are first backed up with tag "pre-restore".
func (m *Manager) Restore(ctx context.Context, serverID, backupID string, by *string) error {
	inst, err := m.Servers.Get(serverID)
	if err != nil {
		return err
	}
	release, err := inst.BeginExclusive("restore")
	if err != nil {
		return err
	}
	defer release()
	unlock, ok := m.lock(serverID)
	if !ok {
		return ErrInProgress
	}
	defer unlock()

	b, archivePath, err := m.File(ctx, serverID, backupID)
	if err != nil {
		return err
	}
	srv := inst.Server()
	tops, err := verifyArchive(archivePath, b.SHA256)
	if err != nil {
		return fmt.Errorf("backup %s failed verification: %w", b.Filename, err)
	}
	for _, t := range tops {
		if !isWorldName(t) {
			return fmt.Errorf("backup contains unexpected top-level entry %q", t)
		}
	}

	if _, err := WorldDirs(srv.Directory); err == nil {
		if _, err := m.create(ctx, inst, Options{Tag: "pre-restore", CreatedBy: by}); err != nil {
			return fmt.Errorf("pre-restore backup: %w", err)
		}
	}

	tmp, err := os.MkdirTemp(srv.Directory, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := fsutil.ExtractTarGz(archivePath, tmp, fsutil.ExtractOptions{}); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Swap each world directory into place, keeping the old one until all
	// swaps succeed.
	stamp := time.Now().Format("20060102150405")
	var olds []string
	for _, t := range tops {
		live, err := fsutil.SafePath(srv.Directory, t)
		if err != nil {
			return err
		}
		if _, err := os.Stat(live); err == nil {
			old := filepath.Join(srv.Directory, ".old-"+t+"-"+stamp)
			if err := os.Rename(live, old); err != nil {
				return err
			}
			olds = append(olds, old)
		}
		if err := os.Rename(filepath.Join(tmp, t), live); err != nil {
			return err
		}
	}
	for _, o := range olds {
		os.RemoveAll(o)
	}
	m.logEvent(srv.ID, map[string]any{"restored": b.ID, "filename": b.Filename})
	m.Log.Info("backup restored", "server", srv.Name, "file", b.Filename)
	return nil
}

func (m *Manager) logEvent(serverID string, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	s := string(raw)
	if err := m.DB.AddServerEvent(context.Background(), serverID, "backup", &s); err != nil {
		m.Log.Error("record backup event", "err", err)
	}
}

// WorldDirs lists the server's world directories: level-name plus the
// _nether/_the_end variants used by versions before 26.1.
func WorldDirs(serverDir string) ([]string, error) {
	level := LevelName(serverDir)
	var out []string
	for _, d := range []string{level, level + "_nether", level + "_the_end"} {
		p, err := fsutil.SafePath(serverDir, d)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no world directory found (has the server been started?)", server.ErrInvalid)
	}
	return out, nil
}

// LevelName returns the server's level-name from server.properties
// (default "world").
func LevelName(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		return "world"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "level-name="); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return "world"
}

func isWorldName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`) && !strings.HasPrefix(s, ".")
}

func sanitizeTag(tag string) string {
	t := tagRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(tag)), "-")
	t = strings.Trim(t, "-.")
	if t == "" {
		return "manual"
	}
	if len(t) > 48 {
		t = t[:48]
	}
	return t
}

package plugin

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"mcsman/internal/db"
	"mcsman/internal/fsutil"
	"mcsman/internal/server"
)

// Installer installs, updates and removes plugins in a server's plugins/
// directory. Changes take effect on the next server start; on Linux it is
// safe to replace a loaded plugin's JAR because the running JVM keeps the
// old file's inode open.
type Installer struct {
	DB         *db.DB
	Servers    *server.Manager
	Registries map[string]Registry
	HTTP       *http.Client
	Log        *slog.Logger

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewInstaller(d *db.DB, servers *server.Manager, log *slog.Logger, regs ...Registry) *Installer {
	m := map[string]Registry{}
	for _, r := range regs {
		m[r.Name()] = r
	}
	return &Installer{
		DB: d, Servers: servers, Registries: m, Log: log,
		HTTP:  &http.Client{Timeout: 5 * time.Minute},
		locks: map[string]*sync.Mutex{},
	}
}

// lock serialises plugin changes per server.
func (in *Installer) lock(serverID string) func() {
	in.mu.Lock()
	l, ok := in.locks[serverID]
	if !ok {
		l = &sync.Mutex{}
		in.locks[serverID] = l
	}
	in.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (in *Installer) registry(source string) (Registry, error) {
	r, ok := in.Registries[source]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownSource, source)
	}
	return r, nil
}

func (in *Installer) pluginsDir(serverID string) (db.Server, string, error) {
	inst, err := in.Servers.Get(serverID)
	if err != nil {
		return db.Server{}, "", err
	}
	srv := inst.Server()
	dir, err := fsutil.SafePath(srv.Directory, "plugins")
	if err != nil {
		return srv, "", err
	}
	return srv, dir, os.MkdirAll(dir, 0o750)
}

// Resolve finds the release to install: a specific version, or the latest
// compatible with the server's Minecraft version.
func (in *Installer) Resolve(ctx context.Context, serverID, source, projectID, versionID string) (*Release, error) {
	reg, err := in.registry(source)
	if err != nil {
		return nil, err
	}
	if versionID != "" {
		return reg.Version(ctx, projectID, versionID)
	}
	inst, err := in.Servers.Get(serverID)
	if err != nil {
		return nil, err
	}
	return reg.Latest(ctx, projectID, inst.Server().MCVersion)
}

// Install adds a plugin from a registry. Installing a project that is
// already installed updates it instead.
func (in *Installer) Install(ctx context.Context, serverID, source, projectID, versionID string) (*db.Plugin, error) {
	rel, err := in.Resolve(ctx, serverID, source, projectID, versionID)
	if err != nil {
		return nil, err
	}
	return in.Apply(ctx, serverID, rel)
}

// Apply downloads and installs a resolved release, replacing any existing
// install of the same project.
func (in *Installer) Apply(ctx context.Context, serverID string, rel *Release) (*db.Plugin, error) {
	staged, err := in.Stage(ctx, serverID, rel)
	if err != nil {
		return nil, err
	}
	return staged.Commit(ctx)
}

// Staged is a verified download waiting to be moved into plugins/.
type Staged struct {
	in       *Installer
	serverID string
	dir      string
	tmp      string
	sha256   string
	rel      *Release
}

// Stage downloads and verifies a release into a temp file inside plugins/
// without touching installed plugins.
func (in *Installer) Stage(ctx context.Context, serverID string, rel *Release) (*Staged, error) {
	_, dir, err := in.pluginsDir(serverID)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(rel.Filename)
	if !strings.HasSuffix(strings.ToLower(name), ".jar") {
		return nil, fmt.Errorf("release file %q is not a .jar", rel.Filename)
	}
	if _, err := fsutil.SafePath(dir, name); err != nil {
		return nil, err
	}
	// .part suffix: Paper only loads *.jar, so a staged file is never loaded.
	tmp, err := fsutil.SafePath(dir, ".staged-"+name+".part")
	if err != nil {
		return nil, err
	}
	sum, err := fsutil.DownloadVerified(ctx, in.HTTP, rel.URL, rel.Checksum, tmp, 0o640, http.Header{"User-Agent": {UserAgent}})
	if err != nil {
		return nil, fmt.Errorf("download %s %s: %w", rel.ProjectName, rel.Version, err)
	}
	return &Staged{in: in, serverID: serverID, dir: dir, tmp: tmp, sha256: sum, rel: rel}, nil
}

// Abort discards the staged download.
func (s *Staged) Abort() { os.Remove(s.tmp) }

// Commit moves the staged JAR into plugins/, removes the previous version of
// the same project and records it in the DB.
func (s *Staged) Commit(ctx context.Context) (*db.Plugin, error) {
	defer os.Remove(s.tmp)
	unlock := s.in.lock(s.serverID)
	defer unlock()

	rel := s.rel
	name := filepath.Base(rel.Filename)
	final, err := fsutil.SafePath(s.dir, name)
	if err != nil {
		return nil, err
	}
	existing, err := s.in.findBySource(ctx, s.serverID, rel.Source, rel.ProjectID)
	if err != nil {
		return nil, err
	}
	// Another tracked plugin (manual or other project) owning the filename?
	if other, _ := s.in.findByFilename(ctx, s.serverID, name); other != nil && (existing == nil || other.ID != existing.ID) {
		return nil, fmt.Errorf("%w: %s already exists in plugins/ (from %s)", server.ErrConflict, name, other.Source)
	}
	if err := os.Rename(s.tmp, final); err != nil {
		return nil, err
	}
	pid := rel.ProjectID
	if existing != nil {
		if existing.Filename != name {
			if old, err := fsutil.SafePath(s.dir, existing.Filename); err == nil {
				os.Remove(old)
			}
		}
		existing.Name, existing.Version, existing.Filename, existing.SHA256 = rel.ProjectName, rel.Version, name, s.sha256
		if err := s.in.DB.UpdatePlugin(ctx, existing); err != nil {
			return nil, err
		}
		s.in.Log.Info("plugin updated", "server_id", s.serverID, "plugin", rel.ProjectName, "version", rel.Version)
		return existing, nil
	}
	p := &db.Plugin{
		ServerID: s.serverID, Name: rel.ProjectName, Version: rel.Version, Source: rel.Source,
		SourceID: &pid, Filename: name, SHA256: s.sha256,
	}
	if err := s.in.DB.CreatePlugin(ctx, p); err != nil {
		os.Remove(final)
		return nil, err
	}
	s.in.Log.Info("plugin installed", "server_id", s.serverID, "plugin", rel.ProjectName, "version", rel.Version)
	return p, nil
}

func (in *Installer) findBySource(ctx context.Context, serverID, source, projectID string) (*db.Plugin, error) {
	list, err := in.DB.ListPlugins(ctx, serverID)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.Source == source && p.SourceID != nil && *p.SourceID == projectID {
			return p, nil
		}
	}
	return nil, nil
}

func (in *Installer) findByFilename(ctx context.Context, serverID, name string) (*db.Plugin, error) {
	list, err := in.DB.ListPlugins(ctx, serverID)
	if err != nil {
		return nil, err
	}
	for _, p := range list {
		if p.Filename == name {
			return p, nil
		}
	}
	return nil, nil
}

// Update installs the latest compatible release of an installed plugin.
// It returns (plugin, false) when already up to date.
func (in *Installer) Update(ctx context.Context, serverID, pluginID string) (*db.Plugin, bool, error) {
	p, err := in.DB.PluginByID(ctx, serverID, pluginID)
	if err != nil {
		return nil, false, err
	}
	if p.Source == db.SourceManual || p.SourceID == nil {
		return nil, false, fmt.Errorf("%w: manually installed plugins cannot be updated automatically", server.ErrInvalid)
	}
	rel, err := in.Resolve(ctx, serverID, p.Source, *p.SourceID, "")
	if err != nil {
		return nil, false, err
	}
	if rel.Version == p.Version {
		return p, false, nil
	}
	np, err := in.Apply(ctx, serverID, rel)
	return np, err == nil, err
}

// Remove deletes a plugin's JAR and record. Its config folder is kept.
func (in *Installer) Remove(ctx context.Context, serverID, pluginID string) error {
	unlock := in.lock(serverID)
	defer unlock()
	p, err := in.DB.PluginByID(ctx, serverID, pluginID)
	if err != nil {
		return err
	}
	_, dir, err := in.pluginsDir(serverID)
	if err != nil {
		return err
	}
	path, err := fsutil.SafePath(dir, p.Filename)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return in.DB.DeletePlugin(ctx, p.ID)
}

// List syncs the DB with plugins/ (recording untracked JARs as manual,
// dropping records whose file is gone) and returns the result.
func (in *Installer) List(ctx context.Context, serverID string) ([]*db.Plugin, error) {
	unlock := in.lock(serverID)
	defer unlock()
	_, dir, err := in.pluginsDir(serverID)
	if err != nil {
		return nil, err
	}
	tracked, err := in.DB.ListPlugins(ctx, serverID)
	if err != nil {
		return nil, err
	}
	byFile := map[string]*db.Plugin{}
	for _, p := range tracked {
		byFile[p.Filename] = p
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".jar") {
			continue
		}
		onDisk[name] = true
		if byFile[name] != nil {
			continue
		}
		path := filepath.Join(dir, name)
		sum, err := fsutil.FileSHA256(path)
		if err != nil {
			continue
		}
		meta := readPluginMeta(path)
		p := &db.Plugin{ServerID: serverID, Name: meta.Name, Version: meta.Version, Source: db.SourceManual, Filename: name, SHA256: sum}
		if p.Name == "" {
			p.Name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		if p.Version == "" {
			p.Version = "unknown"
		}
		if err := in.DB.CreatePlugin(ctx, p); err != nil {
			in.Log.Warn("record manual plugin", "file", name, "err", err)
		}
	}
	for _, p := range tracked {
		if !onDisk[p.Filename] {
			in.DB.DeletePlugin(ctx, p.ID)
		}
	}
	return in.DB.ListPlugins(ctx, serverID)
}

type pluginMeta struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// readPluginMeta reads name/version from paper-plugin.yml or plugin.yml
// inside a JAR. Reads are size-capped since the JAR is untrusted.
func readPluginMeta(path string) pluginMeta {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return pluginMeta{}
	}
	defer zr.Close()
	for _, want := range []string{"paper-plugin.yml", "plugin.yml"} {
		for _, f := range zr.File {
			if f.Name != want || f.UncompressedSize64 > 1<<20 {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			raw, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				continue
			}
			var m pluginMeta
			if yaml.Unmarshal(raw, &m) == nil && m.Name != "" {
				return m
			}
		}
	}
	return pluginMeta{}
}

// IsNotFound reports whether err means the project or version doesn't exist.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

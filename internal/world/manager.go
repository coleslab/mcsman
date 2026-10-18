package world

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcsman/internal/backup"
	"mcsman/internal/db"
	"mcsman/internal/fsutil"
	"mcsman/internal/server"
)

// Limits on uploaded world archives (zip bombs, pathological archives).
var (
	MaxUncompressed int64 = 20 << 30 // 20 GiB
	MaxEntries            = 500_000
)

var ErrNewerWorld = fmt.Errorf("%w: world was saved by a newer Minecraft version than this server", server.ErrConflict)

// Manager imports and exports worlds.
type Manager struct {
	DB      *db.DB
	Servers *server.Manager
	Backups *backup.Manager
	Log     *slog.Logger
}

// Inspection describes an uploaded world before import.
type Inspection struct {
	LevelName         string `json:"level_name"`
	VersionName       string `json:"version_name"`
	DataVersion       int32  `json:"data_version"`
	ServerDataVersion int32  `json:"server_data_version"` // 0 if unknown
	Newer             bool   `json:"newer"`
	Older             bool   `json:"older"`
	root              string // directory inside the zip containing level.dat
}

// Inspect validates a world zip: level.dat at the root or one level deep,
// parseable, and compares its DataVersion with the server's.
func (m *Manager) Inspect(zipPath, serverID string) (*Inspection, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("%w: not a valid zip file", server.ErrInvalid)
	}
	defer zr.Close()
	if len(zr.File) > MaxEntries {
		return nil, fmt.Errorf("%w: archive has too many entries", server.ErrInvalid)
	}
	var level *zip.File
	root := ""
	for _, f := range zr.File {
		name := strings.TrimPrefix(path.Clean("/"+f.Name), "/")
		switch {
		case name == "level.dat":
			level, root = f, ""
		case level == nil && strings.Count(name, "/") == 1 && path.Base(name) == "level.dat" && !strings.HasPrefix(name, "__MACOSX"):
			level, root = f, path.Dir(name)
		}
		if level != nil && root == "" {
			break
		}
	}
	if level == nil {
		return nil, fmt.Errorf("%w: no level.dat found at the top of the zip (or one folder deep)", server.ErrInvalid)
	}
	rc, err := level.Open()
	if err != nil {
		return nil, err
	}
	info, err := ParseLevelDat(rc)
	rc.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", server.ErrInvalid, err)
	}
	in := &Inspection{LevelName: info.LevelName, VersionName: info.VersionName, DataVersion: info.DataVersion, root: root}
	if serverID != "" {
		inst, err := m.Servers.Get(serverID)
		if err != nil {
			return nil, err
		}
		if dv, err := ServerDataVersion(inst.Server().Directory); err == nil {
			in.ServerDataVersion = dv
			in.Newer = info.DataVersion > dv
			in.Older = info.DataVersion < dv
		}
	}
	return in, nil
}

// ServerDataVersion reads world_version from version.json inside server.jar.
func ServerDataVersion(serverDir string) (int32, error) {
	zr, err := zip.OpenReader(filepath.Join(serverDir, "server.jar"))
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "version.json" || f.UncompressedSize64 > 1<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		defer rc.Close()
		var v struct {
			WorldVersion int32 `json:"world_version"`
		}
		if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&v); err != nil || v.WorldVersion == 0 {
			return 0, errors.New("server.jar version.json has no world_version")
		}
		return v.WorldVersion, nil
	}
	return 0, errors.New("server.jar has no version.json")
}

// ImportOptions controls Import.
type ImportOptions struct {
	SourceName string
	ImportedBy *string
	Force      bool // import even if the world is newer than the server
}

// Import replaces a stopped server's world with the one in zipPath.
func (m *Manager) Import(ctx context.Context, serverID, zipPath string, opts ImportOptions) (*db.WorldImport, error) {
	in, err := m.Inspect(zipPath, serverID)
	if err != nil {
		return nil, err
	}
	if in.Newer && !opts.Force {
		return nil, ErrNewerWorld
	}
	inst, err := m.Servers.Get(serverID)
	if err != nil {
		return nil, err
	}
	// 4. Must be stopped; block starts while we work.
	release, err := inst.BeginExclusive("world import")
	if err != nil {
		return nil, err
	}
	defer release()
	srv := inst.Server()

	// 5. Back up the current world, if there is one.
	var backupID *string
	if _, err := backup.WorldDirs(srv.Directory); err == nil {
		b, err := m.Backups.Create(ctx, serverID, backup.Options{Tag: "pre-import", CreatedBy: opts.ImportedBy})
		if err != nil {
			return nil, fmt.Errorf("pre-import backup: %w", err)
		}
		backupID = &b.ID
	}

	// 6. Extract to temp dirs, then swap. Bukkit-layout archives (as
	// exported from pre-26.1 servers) carry the Nether and End as sibling
	// <root>_nether / <root>_the_end folders; map those too.
	level := backup.LevelName(srv.Directory)
	tmp, err := os.MkdirTemp(srv.Directory, ".import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	type mapping struct{ src, dst, staged string }
	maps := []mapping{{in.root, level, filepath.Join(tmp, "0")}}
	if in.root != "" {
		maps = append(maps,
			mapping{in.root + "_nether", level + "_nether", filepath.Join(tmp, "1")},
			mapping{in.root + "_the_end", level + "_the_end", filepath.Join(tmp, "2")})
	}
	for _, mp := range maps {
		if err := os.Mkdir(mp.staged, 0o750); err != nil {
			return nil, err
		}
		if err := extractWorld(zipPath, mp.src, mp.staged); err != nil {
			return nil, err
		}
	}

	stamp := time.Now().Format("20060102150405")
	var olds []string
	// The old world's separate dimension folders belong to the old world.
	for _, d := range []string{level, level + "_nether", level + "_the_end"} {
		p, err := fsutil.SafePath(srv.Directory, d)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(p); err == nil {
			old := filepath.Join(srv.Directory, ".old-"+d+"-"+stamp)
			if err := os.Rename(p, old); err != nil {
				return nil, err
			}
			olds = append(olds, old)
		}
	}
	for i, mp := range maps {
		if i > 0 { // optional dimension folders: skip if the zip had none
			if entries, _ := os.ReadDir(mp.staged); len(entries) == 0 {
				continue
			}
		}
		live, err := fsutil.SafePath(srv.Directory, mp.dst)
		if err != nil {
			return nil, err
		}
		if err := os.Rename(mp.staged, live); err != nil {
			return nil, err
		}
	}
	for _, o := range olds {
		os.RemoveAll(o)
	}

	// 7. Record it.
	name := opts.SourceName
	if name == "" {
		name = in.LevelName
	}
	ver := in.VersionName
	if ver == "" {
		ver = fmt.Sprintf("DataVersion %d", in.DataVersion)
	}
	wi := &db.WorldImport{ServerID: serverID, ImportedBy: opts.ImportedBy, SourceName: name,
		SourceMCVersion: ver, PreImportBackupID: backupID}
	if err := m.DB.CreateWorldImport(ctx, wi); err != nil {
		return nil, err
	}
	m.Log.Info("world imported", "server", srv.Name, "source", name, "version", ver)
	return wi, nil
}

// extractWorld extracts entries under root/ from the zip into dst. Every
// path goes through SafePath; symlinks are rejected; total size and entry
// count are capped; session.lock is skipped.
func extractWorld(zipPath, root, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	prefix := ""
	if root != "" {
		prefix = root + "/"
	}
	var total int64
	for _, f := range zr.File {
		name := strings.TrimPrefix(path.Clean("/"+f.Name), "/")
		if prefix != "" {
			if !strings.HasPrefix(name+"/", prefix) {
				continue
			}
			name = strings.TrimPrefix(name, root)
			name = strings.TrimPrefix(name, "/")
		}
		if name == "" || path.Base(name) == "session.lock" || strings.HasPrefix(name, "__MACOSX") {
			continue
		}
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 || (!mode.IsDir() && !mode.IsRegular()) {
			return fmt.Errorf("%w: %s is not a regular file", server.ErrInvalid, f.Name)
		}
		target, err := fsutil.SafePath(dst, name)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", server.ErrInvalid, f.Name, err)
		}
		if mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		if err := extractFile(f, target, &total); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string, total *int64) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	// Don't trust the declared size: count actual bytes.
	remaining := MaxUncompressed - *total
	n, err := io.Copy(out, io.LimitReader(rc, remaining+1))
	*total += n
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if *total > MaxUncompressed {
		return fmt.Errorf("%w: archive expands beyond %d GiB", server.ErrInvalid, MaxUncompressed>>30)
	}
	return nil
}

// Export streams a zip of the server's world directories to w. A running
// server is flushed and auto-save paused for the duration.
func (m *Manager) Export(ctx context.Context, serverID string, w io.Writer) error {
	inst, err := m.Servers.Get(serverID)
	if err != nil {
		return err
	}
	srv := inst.Server()
	dirs, err := backup.WorldDirs(srv.Directory)
	if err != nil {
		return err
	}
	if inst.Status() == db.StatusRunning {
		if _, err := inst.Exec("save-all flush"); err != nil {
			return fmt.Errorf("save-all: %w", err)
		}
		if _, err := inst.Exec("save-off"); err != nil {
			return fmt.Errorf("save-off: %w", err)
		}
		var once sync.Once
		defer once.Do(func() {
			if _, err := inst.Exec("save-on"); err != nil {
				m.Log.Error("save-on after export failed", "server", srv.Name, "err", err)
			}
		})
	}
	zw := zip.NewWriter(w)
	for _, d := range dirs {
		base := filepath.Join(srv.Directory, d)
		err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.Name() == "session.lock" || !(e.Type().IsRegular() || e.IsDir()) {
				return nil
			}
			rel, _ := filepath.Rel(srv.Directory, p)
			info, err := e.Info()
			if err != nil {
				return err
			}
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if e.IsDir() {
				hdr.Name += "/"
				_, err := zw.CreateHeader(hdr)
				return err
			}
			hdr.Method = zip.Deflate
			fw, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.CopyN(fw, f, info.Size())
			return err
		})
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

package java

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mcsman/internal/fsutil"
)

const DefaultBaseURL = "https://api.adoptium.net"

// Manager installs Temurin JREs under CacheDir/<major>/.
type Manager struct {
	CacheDir string
	BaseURL  string
	HTTP     *http.Client
	Log      *slog.Logger

	mu    sync.Mutex
	locks map[int]*sync.Mutex
}

func NewManager(cacheDir string, log *slog.Logger) *Manager {
	return &Manager{
		CacheDir: cacheDir,
		BaseURL:  DefaultBaseURL,
		HTTP:     &http.Client{Timeout: 10 * time.Minute},
		Log:      log,
		locks:    map[int]*sync.Mutex{},
	}
}

func (m *Manager) lockFor(major int) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[major]
	if !ok {
		l = &sync.Mutex{}
		m.locks[major] = l
	}
	return l
}

// BinPath is where the java binary for major lives once installed.
func (m *Manager) BinPath(major int) string {
	return filepath.Join(m.CacheDir, strconv.Itoa(major), "bin", "java")
}

// Installed reports whether major is already installed.
func (m *Manager) Installed(major int) bool {
	_, err := os.Stat(m.BinPath(major))
	return err == nil
}

// Resolve is the server.Manager Java hook: it takes a major version string
// and returns the java binary path, installing the JRE first if needed.
func (m *Manager) Resolve(version string) (string, error) {
	major, err := strconv.Atoi(version)
	if err != nil {
		return "", fmt.Errorf("invalid java version %q", version)
	}
	return m.Ensure(context.Background(), major)
}

// Ensure installs the latest Temurin JRE for major if it is not present.
func (m *Manager) Ensure(ctx context.Context, major int) (string, error) {
	bin := m.BinPath(major)
	if m.Installed(major) {
		return bin, nil
	}
	l := m.lockFor(major)
	l.Lock()
	defer l.Unlock()
	if m.Installed(major) { // installed while we waited
		return bin, nil
	}

	asset, err := m.latest(ctx, major)
	if err != nil {
		return "", err
	}
	m.Log.Info("downloading java", "major", major, "release", asset.release, "size_mb", asset.size>>20)

	if err := os.MkdirAll(m.CacheDir, 0o750); err != nil {
		return "", err
	}
	tarball := filepath.Join(m.CacheDir, fmt.Sprintf(".jre-%d.tar.gz", major))
	defer os.Remove(tarball)
	if _, err := fsutil.DownloadVerified(ctx, m.HTTP, asset.link, fsutil.SHA256(asset.checksum), tarball, 0o640, nil); err != nil {
		return "", fmt.Errorf("download java %d: %w", major, err)
	}

	tmp, err := os.MkdirTemp(m.CacheDir, fmt.Sprintf(".extract-%d-", major))
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := fsutil.ExtractTarGz(tarball, tmp, fsutil.ExtractOptions{Strip: 1, AllowSymlinks: true}); err != nil {
		return "", fmt.Errorf("extract java %d: %w", major, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "bin", "java")); err != nil {
		return "", fmt.Errorf("java %d archive has no bin/java", major)
	}
	if err := os.Rename(tmp, filepath.Join(m.CacheDir, strconv.Itoa(major))); err != nil {
		return "", err
	}
	m.Log.Info("java installed", "major", major, "path", bin)
	return bin, nil
}

type asset struct {
	link, checksum, release string
	size                    int64
}

func (m *Manager) latest(ctx context.Context, major int) (*asset, error) {
	q := url.Values{"os": {"linux"}, "architecture": {"x64"}, "image_type": {"jre"}}
	u := fmt.Sprintf("%s/v3/assets/latest/%d/hotspot?%s", m.BaseURL, major, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("adoptium: %s", resp.Status)
	}
	var list []struct {
		ReleaseName string `json:"release_name"`
		Binary      struct {
			Package struct {
				Checksum string `json:"checksum"`
				Link     string `json:"link"`
				Size     int64  `json:"size"`
			} `json:"package"`
		} `json:"binary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	if len(list) == 0 || list[0].Binary.Package.Link == "" || list[0].Binary.Package.Checksum == "" {
		return nil, fmt.Errorf("no Temurin JRE %d available for linux/x64", major)
	}
	p := list[0].Binary.Package
	return &asset{link: p.Link, checksum: p.Checksum, release: list[0].ReleaseName, size: p.Size}, nil
}

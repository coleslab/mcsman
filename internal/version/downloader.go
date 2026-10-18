package version

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"mcsman/internal/fsutil"
)

// JarStore keeps verified Paper JARs under <data_dir>/jars/paper/<version>/.
type JarStore struct {
	Dir    string // <data_dir>/jars/paper
	Client *Client

	mu sync.Mutex // serialises downloads
}

// Ensure returns the path of the cached JAR for b, downloading and verifying
// it first if needed.
func (s *JarStore) Ensure(ctx context.Context, b *Build) (string, error) {
	vdir, err := fsutil.SafePath(s.Dir, b.Version)
	if err != nil {
		return "", err
	}
	dst, err := fsutil.SafePath(vdir, filepath.Base(b.Filename))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sum, err := fsutil.FileSHA256(dst); err == nil && sum == b.SHA256 {
		return dst, nil
	}
	hdr := http.Header{"User-Agent": {UserAgent}}
	if _, err := fsutil.DownloadVerified(ctx, s.Client.HTTP, b.URL, fsutil.SHA256(b.SHA256), dst, 0o640, hdr); err != nil {
		return "", fmt.Errorf("download paper %s build %d: %w", b.Version, b.ID, err)
	}
	return dst, nil
}

// InstallLatest downloads the latest build of version v (if not cached) and
// copies it to serverDir/server.jar atomically.
func (s *JarStore) InstallLatest(ctx context.Context, v, serverDir string) (*Build, error) {
	b, err := s.Client.LatestBuild(ctx, v)
	if err != nil {
		return nil, err
	}
	src, err := s.Ensure(ctx, b)
	if err != nil {
		return nil, err
	}
	if err := copyFileAtomic(src, filepath.Join(serverDir, "server.jar"), 0o640); err != nil {
		return nil, err
	}
	return b, nil
}

func copyFileAtomic(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".jar-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

package fsutil

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractOptions controls ExtractTarGz.
type ExtractOptions struct {
	Strip         int  // leading path components to drop
	AllowSymlinks bool // permit symlinks that resolve inside dst
}

// ExtractTarGz extracts archive into dst. Every entry is confined to dst via
// SafePath; symlinks (if allowed) must resolve inside dst; hard links and
// device files are always rejected.
func ExtractTarGz(archive, dst string, opts ExtractOptions) error {
	strip := opts.Strip
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		parts := strings.Split(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"), "/")
		if len(parts) <= strip {
			continue
		}
		rel := filepath.Join(parts[strip:]...)
		if rel == "" || rel == "." {
			continue
		}
		target, err := SafePath(dst, rel)
		if err != nil {
			return fmt.Errorf("%s: %w", h.Name, err)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := os.FileMode(0o640)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o750
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if !opts.AllowSymlinks {
				return fmt.Errorf("%s: symlinks are not allowed", h.Name)
			}
			linkTarget := filepath.Join(filepath.Dir(target), h.Linkname)
			if filepath.IsAbs(h.Linkname) || !strings.HasPrefix(filepath.Clean(linkTarget), filepath.Clean(dst)+string(os.PathSeparator)) {
				return fmt.Errorf("%s: symlink escapes archive root", h.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unsupported tar entry type %c", h.Name, h.Typeflag)
		}
	}
}

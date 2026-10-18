package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// progressWriter counts bytes and reports every reportEvery bytes.
type progressWriter struct {
	n, next, every int64
	report         func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if p.report != nil && p.n >= p.next {
		p.report(p.n)
		p.next = p.n + p.every
	}
	return len(b), nil
}

// writeArchive tars and gzips the given directories (relative to root) into
// w, returning the SHA-256 of the compressed stream and its size. Only
// regular files and directories are archived; session.lock is skipped.
func writeArchive(w io.Writer, root string, dirs []string, report func(int64)) (string, int64, error) {
	h := sha256.New()
	pw := &progressWriter{every: 8 << 20, report: report}
	gz := gzip.NewWriter(io.MultiWriter(w, h, pw))
	tw := tar.NewWriter(gz)

	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(root, d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if e.Name() == "session.lock" {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return nil // skip symlinks, sockets, etc.
			}
			hdr, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(rel)
			if info.IsDir() {
				hdr.Name += "/"
			}
			hdr.Uname, hdr.Gname, hdr.Uid, hdr.Gid = "", "", 0, 0
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			// Copy exactly the header size; a file growing mid-copy (server
			// still writing) must not corrupt the tar stream.
			if _, err := io.CopyN(tw, f, hdr.Size); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			return nil
		})
		if err != nil {
			return "", 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), pw.n, nil
}

// verifyArchive re-reads the archive end to end, checking gzip/tar integrity
// and that its SHA-256 matches want. It returns the top-level names inside.
func verifyArchive(path, want string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(f, h))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var tops []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("corrupt archive: %w", err)
		}
		top := firstComponent(hdr.Name)
		if !seen[top] {
			seen[top] = true
			tops = append(tops, top)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return nil, fmt.Errorf("corrupt archive: %w", err)
		}
	}
	// Drain any trailing bytes so the hash covers the whole file.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return nil, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return nil, errors.New("archive checksum mismatch")
	}
	return tops, nil
}

func firstComponent(name string) string {
	name = filepath.ToSlash(filepath.Clean(name))
	for i, r := range name {
		if r == '/' {
			return name[:i]
		}
	}
	return name
}

// Package fsutil holds filesystem helpers that every user-influenced file
// operation must go through.
package fsutil

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ErrTraversal is returned when a path resolves outside its base directory.
var ErrTraversal = errors.New("path traversal attempt")

// SafePath joins userInput onto base and guarantees the result lies strictly
// under base. It also resolves symlinks in the existing portion of the path so
// a symlink inside base cannot point outside it.
func SafePath(base, userInput string) (string, error) {
	cleanBase := filepath.Clean(base)
	resolved := filepath.Clean(filepath.Join(cleanBase, userInput))
	if !within(cleanBase, resolved) {
		return "", ErrTraversal
	}

	realBase, err := filepath.EvalSymlinks(cleanBase)
	if err != nil {
		if os.IsNotExist(err) {
			return resolved, nil
		}
		return "", err
	}
	// Walk up to the deepest existing ancestor and check its real location.
	probe := resolved
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if real != realBase && !within(realBase, real) {
				return "", ErrTraversal
			}
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe || !within(cleanBase, parent) {
			break
		}
		probe = parent
	}
	return resolved, nil
}

func within(base, p string) bool {
	return strings.HasPrefix(p, base+string(os.PathSeparator))
}

// WriteFileAtomic writes data to a temp file in the same directory and renames
// it into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
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
	return os.Rename(tmp.Name(), path)
}

// Checksum is an expected digest of a download.
type Checksum struct {
	Algo string // "sha256" or "sha512"
	Hex  string
}

// SHA256 is shorthand for a SHA-256 checksum.
func SHA256(hexDigest string) Checksum { return Checksum{Algo: "sha256", Hex: hexDigest} }

func (c Checksum) hasher() (hash.Hash, []byte, error) {
	want, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(c.Hex)))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid expected checksum %q", c.Hex)
	}
	var h hash.Hash
	switch c.Algo {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return nil, nil, fmt.Errorf("unsupported checksum algorithm %q", c.Algo)
	}
	if len(want) != h.Size() {
		return nil, nil, fmt.Errorf("invalid expected %s checksum length", c.Algo)
	}
	return h, want, nil
}

// DownloadVerified downloads url to a temp file next to dst, verifies its
// digest against sum in constant time and only then renames it to dst. On
// mismatch the temp file is removed and dst is untouched. It returns the
// SHA-256 of the file (computed alongside the expected digest).
func DownloadVerified(ctx context.Context, client *http.Client, url string, sum Checksum, dst string, perm os.FileMode, header http.Header) (string, error) {
	h, want, err := sum.hasher()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: unexpected status %s", resp.Status)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	s256 := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h, s256), resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return "", errors.New("checksum mismatch")
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return hex.EncodeToString(s256.Sum(nil)), nil
}

// FileSHA256 returns the hex SHA-256 of the file at path.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

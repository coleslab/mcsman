package fsutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSafePath(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "plugins"), 0o750); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		in string
		ok bool
	}{
		{"plugins/foo.jar", true},
		{"plugins/new/dir/x", true},
		{"./plugins/../world", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../x", false},
		{"plugins/../../x", false},
		{"/etc/passwd", true}, // joined under base, not absolute
		{"escape/x", false},
		{"escape", false},
	}
	for _, c := range cases {
		got, err := SafePath(base, c.in)
		if c.ok && err != nil {
			t.Errorf("SafePath(%q) unexpected error %v", c.in, err)
		}
		if !c.ok && !errors.Is(err, ErrTraversal) {
			t.Errorf("SafePath(%q) = %q, want traversal error", c.in, got)
		}
	}
}

func TestDownloadVerified(t *testing.T) {
	body := []byte("jar bytes")
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "a.jar")
	if err := DownloadVerified(context.Background(), srv.Client(), srv.URL, good, dst, 0o640, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("stat %v %v", fi, err)
	}

	bad := filepath.Join(dir, "b.jar")
	wrong := hex.EncodeToString(make([]byte, 32))
	if err := DownloadVerified(context.Background(), srv.Client(), srv.URL, wrong, bad, 0o640, nil); err == nil {
		t.Fatal("expected mismatch error")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("unverified file was written")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

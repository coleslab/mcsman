package java

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mcsman/internal/fsutil"
)

type entry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func makeTarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body))}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, e.body)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func fakeAdoptium(t *testing.T, archive []byte, hits *int) *httptest.Server {
	sum := sha256.Sum256(archive)
	var mu sync.Mutex
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/assets/latest/") {
			if r.URL.Query().Get("image_type") != "jre" {
				http.Error(w, "want jre", 400)
				return
			}
			fmt.Fprintf(w, `[{"release_name":"jdk-21+1","binary":{"package":{"checksum":"%s","link":"%s/jre.tar.gz","size":%d}}}]`,
				hex.EncodeToString(sum[:]), srv.URL, len(archive))
			return
		}
		mu.Lock()
		*hits++
		mu.Unlock()
		w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureInstallsOnce(t *testing.T) {
	archive := makeTarGz(t, []entry{
		{name: "jdk-21-jre/", typ: tar.TypeDir, mode: 0o755},
		{name: "jdk-21-jre/bin/java", typ: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755},
		{name: "jdk-21-jre/lib/x.so", typ: tar.TypeReg, body: "lib"},
		{name: "jdk-21-jre/legal/link", typ: tar.TypeSymlink, link: "../lib/x.so"},
	})
	hits := 0
	srv := fakeAdoptium(t, archive, &hits)
	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.BaseURL = srv.URL

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Resolve("21"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if hits != 1 {
		t.Errorf("downloaded %d times", hits)
	}
	fi, err := os.Stat(m.BinPath(21))
	if err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("java binary: %v %v", fi, err)
	}
	entries, _ := os.ReadDir(m.CacheDir)
	if len(entries) != 1 {
		t.Errorf("leftover files in cache dir: %v", entries)
	}
}

func TestExtractRejectsEscapes(t *testing.T) {
	cases := map[string][]entry{
		"dotdot":      {{name: "root/../../evil", typ: tar.TypeReg, body: "x"}},
		"abs symlink": {{name: "root/l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"rel symlink": {{name: "root/l", typ: tar.TypeSymlink, link: "../../outside"}},
		"hardlink":    {{name: "root/h", typ: tar.TypeLink, link: "root/x"}},
	}
	for name, ents := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "a.tar.gz")
			os.WriteFile(p, makeTarGz(t, ents), 0o600)
			dst := filepath.Join(dir, "out")
			os.Mkdir(dst, 0o750)
			if err := fsutil.ExtractTarGz(p, dst, fsutil.ExtractOptions{Strip: 1, AllowSymlinks: true}); err == nil {
				t.Error("expected rejection")
			}
		})
	}
}

func TestBadChecksumNotInstalled(t *testing.T) {
	archive := makeTarGz(t, []entry{{name: "j/bin/java", typ: tar.TypeReg, body: "x", mode: 0o755}})
	hits := 0
	srv := fakeAdoptium(t, archive, &hits)
	m := NewManager(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Serve the checksum of different content.
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/") {
			fmt.Fprintf(w, `[{"release_name":"x","binary":{"package":{"checksum":"%s","link":"%s/jre.tar.gz","size":1}}}]`,
				strings.Repeat("ab", 32), srv.URL)
			return
		}
	}))
	defer tampered.Close()
	m.BaseURL = tampered.URL
	if _, err := m.Ensure(context.Background(), 21); err == nil {
		t.Fatal("expected checksum error")
	}
	if m.Installed(21) {
		t.Error("unverified JRE installed")
	}
}

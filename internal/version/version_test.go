package version

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type fakeFill struct {
	srv       *httptest.Server
	jar       []byte
	sum       string
	listCalls atomic.Int32
	badSum    bool
}

func newFakeFill(t *testing.T) *fakeFill {
	f := &fakeFill{jar: []byte("paper jar bytes")}
	s := sha256.Sum256(f.jar)
	f.sum = hex.EncodeToString(s[:])
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/projects/paper", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "UA required", 400)
			return
		}
		f.listCalls.Add(1)
		fmt.Fprint(w, `{"versions":{"26.1":["26.1.2","26.1.1","26.1-rc-1"],"1.21":["1.21.11","1.21.11-pre5","1.21.4","1.21"]}}`)
	})
	mux.HandleFunc("/v3/projects/paper/versions/1.21.4", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":{"id":"1.21.4","support":{"status":"SUPPORTED"},"java":{"version":{"minimum":21},"flags":{"recommended":["-XX:+UseG1GC"]}}}}`)
	})
	mux.HandleFunc("/v3/projects/paper/versions/1.21.4/builds/latest", func(w http.ResponseWriter, r *http.Request) {
		sum := f.sum
		if f.badSum {
			sum = hex.EncodeToString(make([]byte, 32))
		}
		fmt.Fprintf(w, `{"id":232,"channel":"STABLE","downloads":{"server:default":{"name":"paper-1.21.4-232.jar","checksums":{"sha256":"%s"},"size":15,"url":"%s/obj/paper-1.21.4-232.jar"}}}`, sum, f.srv.URL)
	})
	mux.HandleFunc("/obj/", func(w http.ResponseWriter, r *http.Request) { w.Write(f.jar) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeFill) client() *Client {
	c := NewClient()
	c.BaseURL = f.srv.URL
	return c
}

func TestVersionsFilteredSortedCached(t *testing.T) {
	f := newFakeFill(t)
	c := f.client()
	vs, err := c.Versions(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"26.1.2", "26.1.1", "1.21.11", "1.21.4", "1.21"}
	if fmt.Sprint(vs) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", vs, want)
	}
	c.Versions(context.Background(), false)
	if f.listCalls.Load() != 1 {
		t.Errorf("cache miss: %d calls", f.listCalls.Load())
	}
	c.Versions(context.Background(), true)
	if f.listCalls.Load() != 2 {
		t.Error("force did not refetch")
	}
}

func TestInfoAndUnknown(t *testing.T) {
	c := newFakeFill(t).client()
	info, err := c.Info(context.Background(), "1.21.4")
	if err != nil || info.JavaMinimum != 21 || len(info.JavaFlags) != 1 {
		t.Fatalf("info: %+v %v", info, err)
	}
	if _, err := c.LatestBuild(context.Background(), "9.9"); err != ErrUnknownVersion {
		t.Errorf("unknown version: %v", err)
	}
}

func TestInstallLatestVerifies(t *testing.T) {
	f := newFakeFill(t)
	store := &JarStore{Dir: filepath.Join(t.TempDir(), "jars", "paper"), Client: f.client()}
	serverDir := t.TempDir()
	b, err := store.InstallLatest(context.Background(), "1.21.4", serverDir)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != 232 {
		t.Errorf("build = %d", b.ID)
	}
	got, _ := os.ReadFile(filepath.Join(serverDir, "server.jar"))
	if string(got) != string(f.jar) {
		t.Error("server.jar content mismatch")
	}
	if _, err := os.Stat(filepath.Join(store.Dir, "1.21.4", "paper-1.21.4-232.jar")); err != nil {
		t.Error("jar not cached")
	}

	f.badSum = true
	other := t.TempDir()
	store2 := &JarStore{Dir: filepath.Join(t.TempDir(), "jars"), Client: f.client()}
	if _, err := store2.InstallLatest(context.Background(), "1.21.4", other); err == nil {
		t.Fatal("checksum mismatch not detected")
	}
	if _, err := os.Stat(filepath.Join(other, "server.jar")); !os.IsNotExist(err) {
		t.Error("unverified jar installed")
	}
}

func TestCompare(t *testing.T) {
	if Compare("1.21.10", "1.21.9") != 1 || Compare("26.1", "1.21.11") != 1 || Compare("1.21", "1.21.0") != 0 {
		t.Error("compare wrong")
	}
}

// Package testutil holds helpers shared by package tests.
package testutil

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	fakeOnce sync.Once
	fakePath string
	fakeErr  error
)

// FakeMC builds the fakemc binary once per test process and returns its path.
func FakeMC(t testing.TB) string {
	t.Helper()
	fakeOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		dir, err := os.MkdirTemp("", "fakemc-")
		if err != nil {
			fakeErr = err
			return
		}
		fakePath = filepath.Join(dir, "fakemc")
		cmd := exec.Command("go", "build", "-o", fakePath, "./fakemc")
		cmd.Dir = filepath.Dir(file)
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeErr = &buildError{out: string(out), err: err}
		}
	})
	if fakeErr != nil {
		t.Fatalf("build fakemc: %v", fakeErr)
	}
	return fakePath
}

type buildError struct {
	out string
	err error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// FreePort returns a TCP port that was free a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

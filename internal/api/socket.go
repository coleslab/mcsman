package api

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ListenSocket creates the CLI Unix socket with 0660 permissions, replacing a
// stale socket file. Connections from peers that are neither root, the
// daemon's own user nor members of the daemon's group are dropped.
func ListenSocket(path string, log *slog.Logger) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return nil, fmt.Errorf("%s is in use by another daemon", path)
		}
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return &peerCheckListener{Listener: ln, log: log, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}, nil
}

type peerCheckListener struct {
	net.Listener
	log      *slog.Logger
	uid, gid uint32
}

func (l *peerCheckListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		cred, err := peerCred(c)
		if err != nil {
			l.log.Warn("socket: cannot read peer credentials", "err", err)
			c.Close()
			continue
		}
		if l.allowed(cred) {
			return c, nil
		}
		l.log.Warn("socket: rejected peer", "uid", cred.Uid, "pid", cred.Pid)
		c.Close()
	}
}

func (l *peerCheckListener) allowed(cred *syscall.Ucred) bool {
	if cred.Uid == 0 || cred.Uid == l.uid || cred.Gid == l.gid {
		return true
	}
	groups, err := procGroups(int(cred.Pid))
	if err != nil {
		return false
	}
	for _, g := range groups {
		if g == l.gid {
			return true
		}
	}
	return false
}

func peerCred(c net.Conn) (*syscall.Ucred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil {
		return nil, err
	}
	return cred, credErr
}

// procGroups reads a process's supplementary groups from /proc.
func procGroups(pid int) ([]uint32, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Groups:"); ok {
			var out []uint32
			for _, g := range strings.Fields(rest) {
				n, err := strconv.ParseUint(g, 10, 32)
				if err == nil {
					out = append(out, uint32(n))
				}
			}
			return out, nil
		}
	}
	return nil, errors.New("no Groups line")
}

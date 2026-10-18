package console

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/creack/pty"
)

// Process is a child process attached to a PTY.
type Process struct {
	cmd *exec.Cmd
	pty *os.File
}

// Spawn starts bin with args in dir, attached to a new PTY. The binary is
// executed directly (never through a shell); secrets must not be passed in
// args since they are visible in /proc/<pid>/cmdline.
func Spawn(bin string, args []string, dir string, env []string) (*Process, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 200})
	if err != nil {
		return nil, err
	}
	return &Process{cmd: cmd, pty: f}, nil
}

// PID of the child.
func (p *Process) PID() int { return p.cmd.Process.Pid }

// Write sends raw bytes to the child's stdin.
func (p *Process) Write(b []byte) (int, error) { return p.pty.Write(b) }

// Lines reads output line by line, calling fn for each with trailing CR/LF
// stripped, until the PTY closes.
func (p *Process) Lines(fn func(string)) {
	r := bufio.NewReaderSize(p.pty, 64*1024)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			fn(strings.TrimRight(line, "\r\n"))
		}
		if err != nil { // EIO once the child side closes
			return
		}
	}
}

// Wait blocks until the process exits. Call Close afterwards, once Lines has
// drained the remaining output.
func (p *Process) Wait() error { return p.cmd.Wait() }

// Close releases the PTY; a blocked Lines call returns.
func (p *Process) Close() error { return p.pty.Close() }

// Signal sends sig to the process.
func (p *Process) Signal(sig syscall.Signal) error {
	return p.cmd.Process.Signal(sig)
}

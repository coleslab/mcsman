package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"

	"mcsman/internal/console"
	"mcsman/internal/db"
	"mcsman/internal/events"
)

const (
	ringSize        = 1000
	stableThreshold = 5 * time.Minute // a run this long resets the crash counter
)

// Vars so tests can shorten them.
var (
	stopTimeout = 30 * time.Second
	termTimeout = 10 * time.Second
)

var defaultBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 30 * time.Second}

var (
	ansiRe  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][0-9A-Za-z]|[\x00-\x08\x0b-\x1f]`)
	doneRe  = regexp.MustCompile(`Done \([0-9.,]+s\)! For help`)
	joinRe  = regexp.MustCompile(`: ([A-Za-z0-9_]{1,16}) joined the game`)
	leaveRe = regexp.MustCompile(`: ([A-Za-z0-9_]{1,16}) left the game`)
)

// StripANSI removes terminal escape sequences and control characters.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Instance supervises one server process.
type Instance struct {
	deps *deps

	mu            sync.Mutex
	srv           db.Server // config snapshot; replaced by Manager.Update
	rconPassword  string
	status        string
	proc          *console.Process
	rcon          *console.RCON
	exited        chan struct{} // closed when the current process exits
	stopRequested bool
	crashCount    int
	runningSince  time.Time
	restartTimer  *time.Timer
	ring          ring
	players       map[string]struct{}
	busy          string   // name of an exclusive operation (restore, upgrade); blocks Start
	metrics       *Metrics // latest sample while running
}

// Metrics is a point-in-time performance sample.
type Metrics struct {
	TPS         float64 `json:"tps"`
	MSPT        float64 `json:"mspt"`
	RAMUsedMB   int     `json:"ram_used_mb"`
	RAMMaxMB    int     `json:"ram_max_mb"`
	PlayerCount int     `json:"player_count"`
}

// jvmFlags are Aikar's G1 flags, as recommended by PaperMC's API.
var jvmFlags = []string{
	"-XX:+UseG1GC", "-XX:+ParallelRefProcEnabled", "-XX:MaxGCPauseMillis=200",
	"-XX:+UnlockExperimentalVMOptions", "-XX:+DisableExplicitGC", "-XX:+AlwaysPreTouch",
	"-XX:G1NewSizePercent=30", "-XX:G1MaxNewSizePercent=40", "-XX:G1HeapRegionSize=8M",
	"-XX:G1ReservePercent=20", "-XX:G1HeapWastePercent=5", "-XX:G1MixedGCCountTarget=4",
	"-XX:InitiatingHeapOccupancyPercent=15", "-XX:G1MixedGCLiveThresholdPercent=90",
	"-XX:G1RSetUpdatingPauseTimePercent=5", "-XX:SurvivorRatio=32", "-XX:+PerfDisableSharedMem",
	"-XX:MaxTenuringThreshold=1",
}

func newInstance(d *deps, s *db.Server, rconPassword string) *Instance {
	return &Instance{
		deps:         d,
		srv:          *s,
		rconPassword: rconPassword,
		status:       db.StatusStopped,
		players:      map[string]struct{}{},
	}
}

// Server returns a copy of the instance's config with the live status.
func (i *Instance) Server() db.Server {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := i.srv
	s.Status = i.status
	return s
}

func (i *Instance) Status() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.status
}

// PID of the running process, or 0.
func (i *Instance) PID() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.proc == nil {
		return 0
	}
	return i.proc.PID()
}

// Players returns the names of players seen joining and not yet leaving.
func (i *Instance) Players() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]string, 0, len(i.players))
	for p := range i.players {
		out = append(out, p)
	}
	return out
}

// Start launches the server. It returns once the process is spawned; the
// status moves from starting to running when the server logs "Done". The
// Java runtime is installed first if needed, which may take a while.
func (i *Instance) Start() error {
	if err := i.checkStartable(); err != nil {
		return err
	}
	// Resolve outside the lock: a first-time JRE download can take minutes.
	if _, err := i.deps.java(i.Server().JavaVersion); err != nil {
		return fmt.Errorf("install java: %w", err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.checkStartableLocked(); err != nil {
		return err
	}
	i.cancelRestartLocked()
	i.crashCount = 0
	return i.startLocked()
}

func (i *Instance) checkStartable() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.checkStartableLocked()
}

func (i *Instance) checkStartableLocked() error {
	if i.busy != "" {
		return fmt.Errorf("%w: %s in progress", ErrConflict, i.busy)
	}
	switch i.status {
	case db.StatusStarting, db.StatusRunning, db.StatusStopping:
		return fmt.Errorf("%w: server is %s", ErrConflict, i.status)
	}
	return nil
}

// BeginExclusive marks an operation that needs the server stopped and must
// not race a start (restore, upgrade). Call the returned func when done.
func (i *Instance) BeginExclusive(op string) (func(), error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.busy != "" {
		return nil, fmt.Errorf("%w: %s in progress", ErrConflict, i.busy)
	}
	if i.proc != nil {
		return nil, fmt.Errorf("%w: stop the server first", ErrConflict)
	}
	i.cancelRestartLocked()
	i.busy = op
	return func() {
		i.mu.Lock()
		i.busy = ""
		i.mu.Unlock()
	}, nil
}

// Busy returns the name of the exclusive operation in progress, if any.
func (i *Instance) Busy() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.busy
}

// SetMetrics records a sample and reconciles the player list.
func (i *Instance) SetMetrics(m *Metrics, players []string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.status != db.StatusRunning {
		return
	}
	i.metrics = m
	if players != nil {
		i.players = make(map[string]struct{}, len(players))
		for _, p := range players {
			i.players[p] = struct{}{}
		}
	}
}

// Metrics returns the latest sample, or nil when not running.
func (i *Instance) Metrics() *Metrics {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.metrics == nil || i.status != db.StatusRunning {
		return nil
	}
	m := *i.metrics
	return &m
}

func (i *Instance) startLocked() error {
	s := i.srv
	jar := filepath.Join(s.Directory, "server.jar")
	if _, err := os.Stat(jar); err != nil {
		return fmt.Errorf("%w: %s is missing", ErrInvalid, jar)
	}
	if err := checkPortsFree(s.Port, s.RCONPort); err != nil {
		return err
	}
	if err := writeProperties(s.Directory, map[string]string{
		"server-port":           strconv.Itoa(s.Port),
		"enable-rcon":           "true",
		"rcon.port":             strconv.Itoa(s.RCONPort),
		"rcon.password":         i.rconPassword,
		"broadcast-rcon-to-ops": "false",
	}); err != nil {
		return fmt.Errorf("write server.properties: %w", err)
	}
	javaBin, err := i.deps.java(s.JavaVersion)
	if err != nil {
		return fmt.Errorf("resolve java %s: %w", s.JavaVersion, err)
	}
	// -Xms equal to -Xmx is Paper's recommendation (with AlwaysPreTouch).
	args := []string{fmt.Sprintf("-Xms%dM", s.MaxMemoryMB), fmt.Sprintf("-Xmx%dM", s.MaxMemoryMB)}
	args = append(args, jvmFlags...)
	args = append(args, "-jar", "server.jar", "--nogui")
	proc, err := console.Spawn(javaBin, args, s.Directory, childEnv())
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}

	i.proc = proc
	i.rcon = console.NewRCON(fmt.Sprintf("127.0.0.1:%d", s.RCONPort), i.rconPassword)
	i.exited = make(chan struct{})
	i.stopRequested = false
	i.players = map[string]struct{}{}
	i.setStatusLocked(db.StatusStarting)
	i.logEvent("start", nil)
	i.deps.log.Info("server started", "server", s.Name, "pid", proc.PID())

	linesDone := make(chan struct{})
	go func() {
		defer close(linesDone)
		proc.Lines(i.onLine)
	}()
	go i.supervise(proc, i.exited, i.rcon, linesDone)
	return nil
}

// childEnv is a minimal environment for the Java process. The daemon's own
// environment holds secrets (MCSMAN_JWT_SECRET, webhook URLs) and must not leak.
func childEnv() []string {
	env := []string{"TERM=xterm-256color", "LANG=C.UTF-8"}
	for _, k := range []string{"PATH", "HOME", "TZ"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func (i *Instance) onLine(line string) {
	plain := StripANSI(line)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ring.add(line)
	id := i.srv.ID
	i.deps.bus.Publish(events.New(events.ConsoleOutput, id, map[string]string{"line": line}))

	if i.status == db.StatusStarting && doneRe.MatchString(plain) {
		i.runningSince = time.Now()
		i.setStatusLocked(db.StatusRunning)
		i.deps.bus.Publish(events.New(events.ServerOnline, id, nil))
	}
	if m := joinRe.FindStringSubmatch(plain); m != nil {
		i.players[m[1]] = struct{}{}
		i.deps.bus.Publish(events.New(events.PlayerJoin, id, map[string]string{"mc_username": m[1]}))
	} else if m := leaveRe.FindStringSubmatch(plain); m != nil {
		delete(i.players, m[1])
		i.deps.bus.Publish(events.New(events.PlayerLeave, id, map[string]string{"mc_username": m[1]}))
	}
}

// supervise waits for the process to exit and decides what happens next.
func (i *Instance) supervise(proc *console.Process, exited chan struct{}, rc *console.RCON, linesDone chan struct{}) {
	waitErr := proc.Wait()
	select {
	case <-linesDone:
	case <-time.After(2 * time.Second):
	}
	proc.Close()
	rc.Close()

	i.mu.Lock()
	defer i.mu.Unlock()
	close(exited)
	i.proc, i.rcon, i.metrics = nil, nil, nil
	i.players = map[string]struct{}{}

	if i.stopRequested {
		i.setStatusLocked(db.StatusStopped)
		i.logEvent("stop", nil)
		i.deps.bus.Publish(events.New(events.ServerOffline, i.srv.ID, nil))
		return
	}

	exitCode, signal := -1, ""
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		exitCode = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			signal = ws.Signal().String()
		}
	} else if waitErr == nil {
		exitCode = 0
	}
	if !i.runningSince.IsZero() && time.Since(i.runningSince) > stableThreshold {
		i.crashCount = 0
	}
	i.runningSince = time.Time{}

	willRestart := i.srv.AutoRestart && i.crashCount < len(i.deps.backoff)
	detail := map[string]any{"exit_code": exitCode, "attempt": i.crashCount, "will_restart": willRestart}
	if signal != "" {
		detail["signal"] = signal
	}
	i.logEvent("crash", detail)
	i.deps.bus.Publish(events.New(events.ServerCrash, i.srv.ID, detail))
	i.deps.log.Warn("server exited unexpectedly", "server", i.srv.Name, "exit_code", exitCode, "signal", signal, "will_restart", willRestart)
	i.setStatusLocked(db.StatusCrashed)
	if !willRestart {
		return
	}

	delay := i.deps.backoff[i.crashCount]
	i.crashCount++
	var t *time.Timer
	t = time.AfterFunc(delay, func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.restartTimer != t || i.status != db.StatusCrashed {
			return
		}
		i.restartTimer = nil
		i.logEvent("restart", map[string]any{"attempt": i.crashCount})
		if err := i.startLocked(); err != nil {
			i.deps.log.Error("auto-restart failed", "server", i.srv.Name, "err", err)
		}
	})
	i.restartTimer = t
}

func (i *Instance) cancelRestartLocked() {
	if i.restartTimer != nil {
		i.restartTimer.Stop()
		i.restartTimer = nil
	}
}

// StopOptions controls a graceful stop.
type StopOptions struct {
	Delay  time.Duration // countdown broadcast in-game before stopping
	Reason string        // "stopping" or "restarting", used in the broadcast
}

// Stop gracefully stops the server: optional in-game countdown, RCON "stop",
// then SIGTERM after 30s and SIGKILL after a further 10s. It blocks until the
// process has exited. Cancelling ctx skips the remaining countdown.
func (i *Instance) Stop(ctx context.Context, opts StopOptions) error {
	i.mu.Lock()
	if i.proc == nil {
		// Possibly crashed and waiting to auto-restart: cancel and settle.
		i.cancelRestartLocked()
		if i.status == db.StatusCrashed {
			i.setStatusLocked(db.StatusStopped)
		}
		i.mu.Unlock()
		return nil
	}
	if i.status == db.StatusStopping {
		i.mu.Unlock()
		return fmt.Errorf("%w: server is already stopping", ErrConflict)
	}
	i.stopRequested = true
	i.setStatusLocked(db.StatusStopping)
	proc, rc, exited := i.proc, i.rcon, i.exited
	name := i.srv.Name
	i.mu.Unlock()

	reason := opts.Reason
	if reason == "" {
		reason = "stopping"
	}
	say := func(msg string) {
		if _, err := rc.Exec("say " + msg); err != nil {
			proc.Write([]byte("say " + msg + "\n"))
		}
	}
	countdown(ctx, opts.Delay, exited, func(left time.Duration) {
		say(fmt.Sprintf("Server %s in %s", reason, formatLeft(left)))
	})

	if _, err := rc.Exec("stop"); err != nil {
		proc.Write([]byte("stop\n"))
	}
	if waitFor(exited, stopTimeout) {
		return nil
	}
	i.deps.log.Warn("server did not stop in time, sending SIGTERM", "server", name)
	proc.Signal(syscall.SIGTERM)
	if waitFor(exited, termTimeout) {
		return nil
	}
	i.deps.log.Warn("server ignored SIGTERM, sending SIGKILL", "server", name)
	proc.Signal(syscall.SIGKILL)
	<-exited
	return nil
}

// Restart stops the server (with countdown) and starts it again.
func (i *Instance) Restart(ctx context.Context, delay time.Duration) error {
	if delay > 0 {
		i.deps.bus.Publish(events.New(events.RestartWarning, i.srv.ID, map[string]int{"delay_seconds": int(delay.Seconds())}))
	}
	if err := i.Stop(ctx, StopOptions{Delay: delay, Reason: "restarting"}); err != nil {
		return err
	}
	return i.Start()
}

// Exec runs a command over RCON. The server must be running.
func (i *Instance) Exec(cmd string) (string, error) {
	i.mu.Lock()
	rc, status := i.rcon, i.status
	i.mu.Unlock()
	if rc == nil || status != db.StatusRunning {
		return "", fmt.Errorf("%w: server is not running", ErrConflict)
	}
	return rc.Exec(cmd)
}

// WriteConsole sends raw input to the server's PTY.
func (i *Instance) WriteConsole(b []byte) error {
	i.mu.Lock()
	proc := i.proc
	i.mu.Unlock()
	if proc == nil {
		return fmt.Errorf("%w: server is not running", ErrConflict)
	}
	_, err := proc.Write(b)
	return err
}

// ConsoleSnapshot returns buffered lines and a subscription to subsequent
// events, taken atomically so no line is missed or duplicated.
func (i *Instance) ConsoleSnapshot(buffer int) ([]string, <-chan events.Event, func()) {
	i.mu.Lock()
	defer i.mu.Unlock()
	ch, cancel := i.deps.bus.Subscribe(buffer)
	return i.ring.lines(), ch, cancel
}

// setStatusLocked updates status in memory, DB and on the bus.
func (i *Instance) setStatusLocked(status string) {
	if i.status == status {
		return
	}
	i.status = status
	if err := i.deps.db.SetServerStatus(context.Background(), i.srv.ID, status); err != nil {
		i.deps.log.Error("persist status", "server", i.srv.Name, "err", err)
	}
	i.deps.bus.Publish(events.New(events.ServerStatus, i.srv.ID, map[string]string{"status": status}))
}

func (i *Instance) logEvent(typ string, detail any) {
	var dp *string
	if detail != nil {
		b, _ := json.Marshal(detail)
		s := string(b)
		dp = &s
	}
	if err := i.deps.db.AddServerEvent(context.Background(), i.srv.ID, typ, dp); err != nil {
		i.deps.log.Error("record event", "server", i.srv.Name, "err", err)
	}
}

func waitFor(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// countdown calls announce at the start and at standard checkpoints until
// total has elapsed, ctx is cancelled or the process exits on its own.
func countdown(ctx context.Context, total time.Duration, exited <-chan struct{}, announce func(left time.Duration)) {
	if total <= 0 {
		return
	}
	checkpoints := []time.Duration{10 * time.Minute, 5 * time.Minute, time.Minute, 30 * time.Second,
		10 * time.Second, 5 * time.Second, 4 * time.Second, 3 * time.Second, 2 * time.Second, time.Second}
	deadline := time.Now().Add(total)
	announce(total)
	for _, cp := range checkpoints {
		if cp >= total {
			continue
		}
		select {
		case <-time.After(time.Until(deadline.Add(-cp))):
			announce(cp)
		case <-ctx.Done():
			return
		case <-exited:
			return
		}
	}
	select {
	case <-time.After(time.Until(deadline)):
	case <-ctx.Done():
	case <-exited:
	}
}

func formatLeft(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		n := int(d / time.Minute)
		if n == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", n)
	}
	n := int(d.Round(time.Second) / time.Second)
	if n == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", n)
}

// ring is a fixed-size buffer of the most recent console lines.
type ring struct {
	buf  [ringSize]string
	next int
	full bool
}

func (r *ring) add(s string) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % ringSize
	if r.next == 0 {
		r.full = true
	}
}

func (r *ring) lines() []string {
	if !r.full {
		return append([]string(nil), r.buf[:r.next]...)
	}
	out := make([]string, 0, ringSize)
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}

// deps are shared by all instances of a Manager.
type deps struct {
	db      *db.DB
	bus     *events.Bus
	log     *slog.Logger
	java    func(version string) (string, error)
	backoff []time.Duration
}

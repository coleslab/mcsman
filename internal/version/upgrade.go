package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcsman/internal/backup"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/plugin"
	"mcsman/internal/server"
)

// JavaInstaller installs a Java major version (java.Manager).
type JavaInstaller interface {
	Ensure(ctx context.Context, major int) (string, error)
}

// Upgrader runs Paper version upgrades: a read-only preflight, then a
// background job that stops, backs up, stages, swaps and restarts.
type Upgrader struct {
	Paper   *Client
	Jars    *JarStore
	Java    JavaInstaller
	Plugins *plugin.Installer
	Backups *backup.Manager
	Servers *server.Manager
	DB      *db.DB
	Bus     *events.Bus
	Log     *slog.Logger
	// TailDuration is how long to watch logs after start (default 60s).
	TailDuration time.Duration

	mu   sync.Mutex
	jobs map[string]*Job
}

// Preflight is the report shown before an upgrade. Nothing is changed.
type Preflight struct {
	ServerID       string          `json:"server_id"`
	ServerName     string          `json:"server_name"`
	CurrentVersion string          `json:"current_version"`
	TargetVersion  string          `json:"target_version"`
	TargetBuild    int             `json:"target_build"`
	CurrentJava    int             `json:"current_java"`
	RequiredJava   int             `json:"required_java"`
	JavaChange     bool            `json:"java_change"`
	Plugins        []plugin.Compat `json:"plugins"`
	Warnings       []string        `json:"warnings"`
	Blockers       []string        `json:"blockers"`
	CanUpgrade     bool            `json:"can_upgrade"`

	build *Build
}

// Preflight checks whether serverID can move to target and how plugins fare.
func (u *Upgrader) Preflight(ctx context.Context, serverID, target string) (*Preflight, error) {
	inst, err := u.Servers.Get(serverID)
	if err != nil {
		return nil, err
	}
	srv := inst.Server()
	cur, _ := strconv.Atoi(srv.JavaVersion)
	p := &Preflight{
		ServerID: srv.ID, ServerName: srv.Name, CurrentVersion: srv.MCVersion, TargetVersion: target,
		CurrentJava: cur, Warnings: []string{}, Blockers: []string{},
	}

	// 1. Target must exist.
	ok, err := u.Paper.Exists(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("fetch Paper versions: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: Paper has no release %q", server.ErrInvalid, target)
	}
	switch c := Compare(target, srv.MCVersion); {
	case c < 0:
		p.Blockers = append(p.Blockers, "Downgrading is not supported: worlds saved by a newer version can be corrupted by an older one.")
	case c == 0:
		p.Warnings = append(p.Warnings, "Already on this version; the upgrade will install the latest Paper build.")
	}
	if b := inst.Busy(); b != "" {
		p.Blockers = append(p.Blockers, fmt.Sprintf("A %s is in progress.", b))
	}
	if j := u.Job(serverID); j != nil && j.State == JobRunning {
		p.Blockers = append(p.Blockers, "An upgrade is already running.")
	}
	b, err := u.Paper.LatestBuild(ctx, target)
	if err != nil {
		return nil, err
	}
	p.build, p.TargetBuild = b, b.ID
	if b.Channel != "" && b.Channel != "STABLE" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The latest build for %s is %s, not a stable release.", target, b.Channel))
	}

	// 3. Java requirement.
	info, err := u.Paper.Info(ctx, target)
	if err != nil {
		return nil, err
	}
	p.RequiredJava = max(info.JavaMinimum, cur)
	p.JavaChange = info.JavaMinimum > cur
	if p.JavaChange {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Java %d will be installed (currently %d).", info.JavaMinimum, cur))
	}

	// 2. Plugin compatibility.
	plugins, err := u.Plugins.List(ctx, serverID)
	if err != nil {
		return nil, err
	}
	p.Plugins = u.Plugins.Check(ctx, plugins, target)
	for _, c := range p.Plugins {
		if c.Status == plugin.Amber {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s has no release for %s and may fail to load.", c.Plugin.Name, target))
		}
	}
	p.CanUpgrade = len(p.Blockers) == 0
	return p, nil
}

// Job states.
const (
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// Job is a running or finished upgrade.
type Job struct {
	ServerID       string    `json:"server_id"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	State          string    `json:"state"`
	Phase          string    `json:"phase"`
	Error          string    `json:"error,omitempty"`
	BackupID       string    `json:"backup_id,omitempty"`
	PluginsUpdated []string  `json:"plugins_updated"`
	PluginErrors   []string  `json:"plugin_errors"`
	Steps          []JobStep `json:"steps"`
	StartedAt      int64     `json:"started_at"`
	FinishedAt     int64     `json:"finished_at,omitempty"`
}

type JobStep struct {
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
	TS     int64  `json:"ts"`
}

// Job returns a snapshot of the latest job for a server, or nil.
func (u *Upgrader) Job(serverID string) *Job {
	u.mu.Lock()
	defer u.mu.Unlock()
	j, ok := u.jobs[serverID]
	if !ok {
		return nil
	}
	c := *j
	c.Steps = append([]JobStep(nil), j.Steps...)
	c.PluginsUpdated = append([]string{}, j.PluginsUpdated...)
	c.PluginErrors = append([]string{}, j.PluginErrors...)
	return &c
}

func (u *Upgrader) step(j *Job, phase, detail string) {
	u.mu.Lock()
	j.Phase = phase
	j.Steps = append(j.Steps, JobStep{Phase: phase, Detail: detail, TS: time.Now().Unix()})
	u.mu.Unlock()
	u.Bus.Publish(events.New(events.UpgradeProgress, j.ServerID, map[string]string{"phase": phase, "detail": detail}))
	u.Log.Info("upgrade", "server_id", j.ServerID, "phase", phase, "detail", detail)
}

// ConfirmOptions controls an upgrade run.
type ConfirmOptions struct {
	Delay     time.Duration // in-game countdown before stopping
	CreatedBy *string
}

// Confirm re-runs the preflight and, if allowed, starts the upgrade in the
// background. Progress is published as upgrade_progress events.
func (u *Upgrader) Confirm(ctx context.Context, serverID, target string, opts ConfirmOptions) (*Job, error) {
	pre, err := u.Preflight(ctx, serverID, target)
	if err != nil {
		return nil, err
	}
	if !pre.CanUpgrade {
		return nil, fmt.Errorf("%w: %s", server.ErrConflict, pre.Blockers[0])
	}
	j := &Job{
		ServerID: serverID, From: pre.CurrentVersion, To: target, State: JobRunning,
		PluginsUpdated: []string{}, PluginErrors: []string{}, StartedAt: time.Now().Unix(),
	}
	u.mu.Lock()
	if u.jobs == nil {
		u.jobs = map[string]*Job{}
	}
	if old, ok := u.jobs[serverID]; ok && old.State == JobRunning {
		u.mu.Unlock()
		return nil, fmt.Errorf("%w: an upgrade is already running", server.ErrConflict)
	}
	u.jobs[serverID] = j
	u.mu.Unlock()

	go u.run(context.WithoutCancel(ctx), j, pre, opts)
	return u.Job(serverID), nil
}

func (u *Upgrader) finish(j *Job, err error) {
	u.mu.Lock()
	j.FinishedAt = time.Now().Unix()
	if err != nil {
		j.State, j.Error = JobFailed, err.Error()
	} else {
		j.State = JobSucceeded
	}
	snapshot := *j
	u.mu.Unlock()

	detail, _ := json.Marshal(map[string]any{
		"from": j.From, "to": j.To, "state": snapshot.State, "error": snapshot.Error,
		"backup_id": snapshot.BackupID, "plugin_errors": snapshot.PluginErrors,
	})
	ds := string(detail)
	u.DB.AddServerEvent(context.Background(), j.ServerID, "upgrade", &ds)
	if err != nil {
		u.step(j, "failed", err.Error())
	} else {
		u.step(j, "done", fmt.Sprintf("Upgraded %s → %s", j.From, j.To))
	}
	u.Bus.Publish(events.New(events.UpgradeComplete, j.ServerID, snapshot))
}

func (u *Upgrader) run(ctx context.Context, j *Job, pre *Preflight, opts ConfirmOptions) {
	inst, err := u.Servers.Get(j.ServerID)
	if err != nil {
		u.finish(j, err)
		return
	}

	// 1. Stop (graceful, with countdown).
	if inst.Status() != db.StatusStopped {
		u.step(j, "stop", fmt.Sprintf("Stopping server (countdown %s)", opts.Delay))
		if err := inst.Stop(ctx, server.StopOptions{Delay: opts.Delay, Reason: "upgrading"}); err != nil {
			u.finish(j, fmt.Errorf("stop: %w", err))
			return
		}
	}
	release, err := inst.BeginExclusive("upgrade")
	if err != nil {
		u.finish(j, err)
		return
	}

	err = u.stageAndApply(ctx, j, inst, pre, opts)
	release()
	if err != nil {
		u.finish(j, err)
		return
	}

	// 7-8. Start and watch the logs for plugin load errors.
	errs, err := u.startAndTail(ctx, j, inst)
	u.mu.Lock()
	j.PluginErrors = errs
	u.mu.Unlock()
	u.finish(j, err)
}

// stageAndApply backs up, downloads everything, then swaps it all in. Any
// failure before the swap leaves the server untouched.
func (u *Upgrader) stageAndApply(ctx context.Context, j *Job, inst *server.Instance, pre *Preflight, opts ConfirmOptions) error {
	srv := inst.Server()

	// 2. Backup.
	u.step(j, "backup", "Backing up world")
	b, err := u.Backups.Create(ctx, srv.ID, backup.Options{Tag: "pre-upgrade-" + srv.MCVersion, CreatedBy: opts.CreatedBy})
	if err != nil {
		return fmt.Errorf("pre-upgrade backup: %w", err)
	}
	u.mu.Lock()
	j.BackupID = b.ID
	u.mu.Unlock()

	// 3. Paper JAR.
	u.step(j, "download", fmt.Sprintf("Downloading Paper %s build %d", pre.TargetVersion, pre.TargetBuild))
	jar, err := u.Jars.Ensure(ctx, pre.build)
	if err != nil {
		return err
	}

	// 4. Java.
	if pre.JavaChange {
		u.step(j, "java", fmt.Sprintf("Installing Java %d", pre.RequiredJava))
		if _, err := u.Java.Ensure(ctx, pre.RequiredJava); err != nil {
			return fmt.Errorf("install java %d: %w", pre.RequiredJava, err)
		}
	}

	// 5. Stage plugin updates (green ones with a newer release).
	var staged []*plugin.Staged
	defer func() {
		for _, s := range staged {
			s.Abort()
		}
	}()
	for _, c := range pre.Plugins {
		if c.Status != plugin.Green || c.Release == nil || c.Release.Version == c.Plugin.Version {
			continue
		}
		u.step(j, "plugins", fmt.Sprintf("Downloading %s %s", c.Plugin.Name, c.Release.Version))
		s, err := u.Plugins.Stage(ctx, srv.ID, c.Release)
		if err != nil {
			return err
		}
		staged = append(staged, s)
	}

	// 6. Swap everything in.
	u.step(j, "apply", "Installing new server JAR and plugins")
	if err := copyFileAtomic(jar, filepath.Join(srv.Directory, "server.jar"), 0o640); err != nil {
		return err
	}
	for _, s := range staged {
		p, err := s.Commit(ctx)
		if err != nil {
			// The new JAR is already in place; report but keep going so the
			// server is at least consistent with its recorded version.
			u.Log.Error("upgrade: plugin commit failed", "err", err)
			continue
		}
		u.mu.Lock()
		j.PluginsUpdated = append(j.PluginsUpdated, p.Name+" "+p.Version)
		u.mu.Unlock()
	}
	staged = nil
	mc, jv := pre.TargetVersion, strconv.Itoa(pre.RequiredJava)
	if _, err := u.Servers.Update(ctx, srv.ID, server.UpdateParams{MCVersion: &mc, JavaVersion: &jv}); err != nil {
		return fmt.Errorf("record new version: %w", err)
	}
	return nil
}

var pluginErrorRe = regexp.MustCompile(`(?i)(could not load '|error occurred while (enabling|loading)|unknown/missing dependency|invalidpluginexception|unknowndependencyexception|ambiguous plugin name|plugin .* failed)`)

// StartTimeout bounds how long an upgraded server may take to finish
// starting. Version jumps can trigger world migrations (Paper waits 30s
// before migrating, then converts chunks), so this is generous.
var StartTimeout = 15 * time.Minute

func (u *Upgrader) startAndTail(ctx context.Context, j *Job, inst *server.Instance) ([]string, error) {
	tail := u.TailDuration
	if tail == 0 {
		tail = 60 * time.Second
	}
	ch, cancel := u.Bus.Subscribe(1024)
	defer cancel()

	u.step(j, "start", "Starting server")
	if err := inst.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	u.step(j, "verify", fmt.Sprintf("Waiting for startup, then watching logs for %s", tail))
	var errs []string
	startDeadline := time.NewTimer(StartTimeout)
	defer startDeadline.Stop()
	var tailDone <-chan time.Time // armed once the server is up
	for {
		select {
		case <-startDeadline.C:
			if tailDone == nil {
				return errs, fmt.Errorf("server did not finish starting within %s", StartTimeout)
			}
		case <-tailDone:
			return errs, nil
		case <-ctx.Done():
			return errs, ctx.Err()
		case e := <-ch:
			if e.ServerID != j.ServerID {
				continue
			}
			switch e.Type {
			case events.ServerOnline:
				if tailDone == nil {
					tailDone = time.After(tail)
				}
			case events.ServerCrash:
				return errs, errors.New("server crashed after upgrade; the pre-upgrade backup can be restored")
			case events.ConsoleOutput:
				line := server.StripANSI(e.Data.(map[string]string)["line"])
				if pluginErrorRe.MatchString(line) && len(errs) < 50 {
					errs = append(errs, line)
				}
			}
		}
	}
}

// Summary renders a job for notifications: title, description, success.
func (j Job) Summary() (string, string, bool) {
	if j.State == JobFailed {
		return fmt.Sprintf("Upgrade to %s failed", j.To), j.Error, false
	}
	desc := fmt.Sprintf("Paper %s → %s", j.From, j.To)
	if len(j.PluginsUpdated) > 0 {
		desc += "\nUpdated: " + strings.Join(j.PluginsUpdated, ", ")
	}
	if len(j.PluginErrors) > 0 {
		desc += fmt.Sprintf("\n⚠ %d plugin error(s) after start — check the console.", len(j.PluginErrors))
	}
	return "Upgrade complete", desc, true
}

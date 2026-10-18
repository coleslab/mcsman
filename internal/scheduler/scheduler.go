// Package scheduler runs per-server scheduled backups and restarts.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"mcsman/internal/backup"
	"mcsman/internal/cronspec"
	"mcsman/internal/db"
	"mcsman/internal/server"
)

// Scheduler owns a cron instance with up to two entries per server.
type Scheduler struct {
	Servers       *server.Manager
	Backups       *backup.Manager
	DB            *db.DB
	Log           *slog.Logger
	DefaultBackup string        // daemon-wide backup schedule (config backup.default_interval)
	RestartDelay  time.Duration // in-game countdown before scheduled restarts (default 5m)

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]jobs
}

type jobs struct {
	backup, restart cron.EntryID
}

// Start schedules all servers and starts the cron loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	s.cron = cron.New(cron.WithLocation(time.Local))
	s.entries = map[string]jobs{}
	s.mu.Unlock()
	for _, inst := range s.Servers.All() {
		s.Reload(inst.Server().ID)
	}
	s.cron.Start()
}

// Stop halts scheduling and waits for running jobs (bounded by ctx).
func (s *Scheduler) Stop(ctx context.Context) {
	done := s.cron.Stop()
	select {
	case <-done.Done():
	case <-ctx.Done():
	}
}

// Reload (re)schedules a server after its settings change.
func (s *Scheduler) Reload(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(serverID)
	inst, err := s.Servers.Get(serverID)
	if err != nil {
		return
	}
	srv := inst.Server()
	var j jobs
	if sched := s.parse(srv.Name, "backup", s.BackupSpec(srv)); sched != nil {
		j.backup = s.cron.Schedule(sched, cron.FuncJob(func() { s.runBackup(serverID) }))
	}
	if srv.RestartSchedule != nil {
		if sched := s.parse(srv.Name, "restart", *srv.RestartSchedule); sched != nil {
			j.restart = s.cron.Schedule(sched, cron.FuncJob(func() { s.runRestart(serverID) }))
		}
	}
	s.entries[serverID] = j
}

// Remove unschedules a deleted server.
func (s *Scheduler) Remove(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(serverID)
}

func (s *Scheduler) removeLocked(serverID string) {
	if j, ok := s.entries[serverID]; ok {
		if j.backup != 0 {
			s.cron.Remove(j.backup)
		}
		if j.restart != 0 {
			s.cron.Remove(j.restart)
		}
		delete(s.entries, serverID)
	}
}

// BackupSpec is the effective backup schedule for a server.
func (s *Scheduler) BackupSpec(srv db.Server) string {
	if srv.BackupSchedule != nil {
		return *srv.BackupSchedule
	}
	return s.DefaultBackup
}

func (s *Scheduler) parse(name, kind, spec string) cron.Schedule {
	sched, err := cronspec.Parse(spec)
	if err != nil {
		s.Log.Warn("invalid schedule; ignoring", "server", name, "kind", kind, "spec", spec, "err", err)
		return nil
	}
	return sched
}

// Next returns the next scheduled backup and restart times (nil if none).
func (s *Scheduler) Next(serverID string) (backupAt, restartAt *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.entries[serverID]
	if !ok {
		return nil, nil
	}
	at := func(id cron.EntryID) *time.Time {
		if id == 0 {
			return nil
		}
		if e := s.cron.Entry(id); e.Valid() && !e.Next.IsZero() {
			t := e.Next
			return &t
		}
		return nil
	}
	return at(j.backup), at(j.restart)
}

func (s *Scheduler) runBackup(serverID string) {
	inst, err := s.Servers.Get(serverID)
	if err != nil {
		return
	}
	srv := inst.Server()
	if b := inst.Busy(); b != "" {
		s.Log.Info("scheduled backup skipped", "server", srv.Name, "reason", b+" in progress")
		return
	}
	switch inst.Status() {
	case db.StatusRunning:
	case db.StatusStopped, db.StatusCrashed:
		// A stopped world doesn't change; only back up if it ran since the
		// last backup.
		changed, err := s.DB.ChangedSinceLastBackup(context.Background(), serverID)
		if err != nil || !changed {
			return
		}
		if _, err := backup.WorldDirs(srv.Directory); err != nil {
			return // never started; nothing to back up
		}
	default:
		s.Log.Info("scheduled backup skipped", "server", srv.Name, "status", inst.Status())
		return
	}
	// Failures are logged and published (backup_failed → Discord) by the
	// backup manager.
	if _, err := s.Backups.Create(context.Background(), serverID, backup.Options{Tag: "auto"}); err != nil &&
		errors.Is(err, backup.ErrInProgress) {
		s.Log.Info("scheduled backup skipped", "server", srv.Name, "reason", "another backup is running")
	}
}

func (s *Scheduler) runRestart(serverID string) {
	inst, err := s.Servers.Get(serverID)
	if err != nil || inst.Status() != db.StatusRunning || inst.Busy() != "" {
		return
	}
	delay := s.RestartDelay
	if delay == 0 {
		delay = 5 * time.Minute
	}
	s.Log.Info("scheduled restart", "server", inst.Server().Name, "countdown", delay)
	if err := inst.Restart(context.Background(), delay); err != nil {
		s.Log.Error("scheduled restart failed", "server", inst.Server().Name, "err", err)
	}
}

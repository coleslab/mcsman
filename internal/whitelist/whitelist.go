// Package whitelist adds players to server whitelists: immediately over RCON
// when the server is running, otherwise via a queue applied on next start.
package whitelist

import (
	"context"
	"log/slog"
	"strings"

	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

type Service struct {
	DB      *db.DB
	Servers *server.Manager
	Bus     *events.Bus
	Log     *slog.Logger
}

// Add whitelists mcUsername on a server. It reports whether the change was
// applied now (true) or queued for the next start (false).
func (s *Service) Add(ctx context.Context, serverID, mcUsername string) (bool, error) {
	inst, err := s.Servers.Get(serverID)
	if err != nil {
		return false, err
	}
	if inst.Status() == db.StatusRunning {
		if s.apply(inst, mcUsername) {
			_, err := s.DB.QueueWhitelist(ctx, serverID, mcUsername, true)
			return true, err
		}
	}
	_, err = s.DB.QueueWhitelist(ctx, serverID, mcUsername, false)
	return false, err
}

// apply runs "whitelist add" and reports whether the entry is settled
// (added, already present, or permanently invalid).
func (s *Service) apply(inst *server.Instance, mc string) bool {
	out, err := inst.Exec("whitelist add " + mc)
	if err != nil {
		s.Log.Warn("whitelist add failed; will retry on next start", "server", inst.Server().Name, "player", mc, "err", err)
		return false
	}
	plain := strings.ToLower(server.StripANSI(out))
	if strings.Contains(plain, "does not exist") || strings.Contains(plain, "unknown") {
		s.Log.Warn("whitelist: player not found", "server", inst.Server().Name, "player", mc, "output", out)
	} else {
		s.Log.Info("whitelisted player", "server", inst.Server().Name, "player", mc)
	}
	return true
}

// Run applies queued entries whenever a server comes online.
func (s *Service) Run(ctx context.Context) {
	ch, cancel := s.Bus.Subscribe(64)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			if e.Type == events.ServerOnline {
				s.ApplyPending(ctx, e.ServerID)
			}
		}
	}
}

// ApplyPending applies all queued entries for a running server.
func (s *Service) ApplyPending(ctx context.Context, serverID string) {
	inst, err := s.Servers.Get(serverID)
	if err != nil {
		return
	}
	pending, err := s.DB.PendingWhitelist(ctx, serverID)
	if err != nil {
		s.Log.Error("load whitelist queue", "err", err)
		return
	}
	for _, e := range pending {
		if !s.apply(inst, e.MCUsername) {
			return // RCON trouble; leave the rest for next start
		}
		if err := s.DB.MarkWhitelistApplied(ctx, e.ID); err != nil {
			s.Log.Error("mark whitelist applied", "err", err)
		}
	}
}

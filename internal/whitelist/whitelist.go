// Package whitelist keeps server whitelists in step with server membership:
// changes go out immediately over RCON when the server is running, otherwise
// via a queue applied on next start.
package whitelist

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"mcsman/internal/auth"
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
	return s.change(ctx, serverID, mcUsername, db.WhitelistAdd)
}

// Remove takes mcUsername off a server's whitelist, now or on next start.
func (s *Service) Remove(ctx context.Context, serverID, mcUsername string) (bool, error) {
	return s.change(ctx, serverID, mcUsername, db.WhitelistRemove)
}

func (s *Service) change(ctx context.Context, serverID, mc, action string) (bool, error) {
	// Names reach the server console, so only valid Java usernames pass.
	if !auth.ValidMCUsername(mc) {
		return false, fmt.Errorf("%w: invalid Minecraft username", auth.ErrInvalidInput)
	}
	inst, err := s.Servers.Get(serverID)
	if err != nil {
		return false, err
	}
	if inst.Status() == db.StatusRunning {
		if s.apply(inst, action, mc) {
			_, err := s.DB.QueueWhitelist(ctx, serverID, mc, action, true)
			return true, err
		}
	}
	_, err = s.DB.QueueWhitelist(ctx, serverID, mc, action, false)
	return false, err
}

// apply runs "whitelist add|remove" and reports whether the entry is
// settled (done, already so, or permanently invalid).
func (s *Service) apply(inst *server.Instance, action, mc string) bool {
	out, err := inst.Exec("whitelist " + action + " " + mc)
	if err != nil {
		s.Log.Warn("whitelist "+action+" failed; will retry on next start", "server", inst.Server().Name, "player", mc, "err", err)
		return false
	}
	plain := strings.ToLower(server.StripANSI(out))
	if strings.Contains(plain, "does not exist") || strings.Contains(plain, "unknown") {
		s.Log.Warn("whitelist: player not found", "server", inst.Server().Name, "player", mc, "output", out)
	} else {
		s.Log.Info("whitelist "+action, "server", inst.Server().Name, "player", mc)
	}
	return true
}

// AddMember makes u a member of serverID and whitelists their Minecraft
// name. It reports whether the whitelist change was applied now.
func (s *Service) AddMember(ctx context.Context, serverID string, u *db.User, addedBy *string) (bool, error) {
	if u.MCUsername == nil {
		return false, fmt.Errorf("%w: %s has no Minecraft username", auth.ErrInvalidInput, u.Username)
	}
	if _, err := s.Servers.Get(serverID); err != nil {
		return false, err
	}
	if _, err := s.DB.AddMember(ctx, serverID, u.ID, addedBy); err != nil {
		return false, err
	}
	return s.Add(ctx, serverID, *u.MCUsername)
}

// RemoveMember ends u's membership of serverID and takes them off its
// whitelist, unless another member shares the Minecraft name.
func (s *Service) RemoveMember(ctx context.Context, serverID string, u *db.User) error {
	if err := s.DB.RemoveMember(ctx, serverID, u.ID); err != nil {
		return err
	}
	if u.MCUsername == nil || s.nameInUse(ctx, serverID, *u.MCUsername) {
		return nil
	}
	_, err := s.Remove(ctx, serverID, *u.MCUsername)
	return err
}

// RemoveUser un-whitelists u on every server they belong to. Call it
// before deleting the user; the memberships themselves cascade.
func (s *Service) RemoveUser(ctx context.Context, u *db.User) error {
	ids, err := s.DB.ServersForUser(ctx, u.ID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.RemoveMember(ctx, id, u); err != nil {
			s.Log.Error("whitelist remove on user delete", "server", id, "err", err)
		}
	}
	return nil
}

// Rename moves a user's whitelist entries from old to new on every server
// they belong to. Either name may be nil. Call it after the new name is
// saved, so the user's own entry doesn't count as another member's.
func (s *Service) Rename(ctx context.Context, userID string, from, to *string) error {
	ids, err := s.DB.ServersForUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if from != nil && (to == nil || !strings.EqualFold(*from, *to)) && !s.nameInUse(ctx, id, *from) {
			if _, err := s.Remove(ctx, id, *from); err != nil {
				return err
			}
		}
		if to != nil {
			if _, err := s.Add(ctx, id, *to); err != nil {
				return err
			}
		}
	}
	return nil
}

// nameInUse reports whether a current member of serverID has Minecraft
// name mc.
func (s *Service) nameInUse(ctx context.Context, serverID, mc string) bool {
	members, err := s.DB.ListMembers(ctx, serverID)
	if err != nil {
		return true // be safe: don't un-whitelist on a lookup error
	}
	for _, m := range members {
		if m.MCUsername != nil && strings.EqualFold(*m.MCUsername, mc) {
			return true
		}
	}
	return false
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
		if !s.apply(inst, e.Action, e.MCUsername) {
			return // RCON trouble; leave the rest for next start
		}
		if err := s.DB.MarkWhitelistApplied(ctx, e.ID); err != nil {
			s.Log.Error("mark whitelist applied", "err", err)
		}
	}
}

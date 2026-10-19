// Package events is an in-process pub/sub bus. Server instances publish;
// the WebSocket hub, Discord notifier and DB event log subscribe.
package events

import (
	"sync"
	"time"
)

// Event types. The WebSocket ones match the spec's envelope; the rest are
// internal notifications consumed by Discord and the event log.
const (
	ConsoleOutput   = "console_output"
	Metrics         = "metrics"
	ServerStatus    = "server_status"
	PlayerJoin      = "player_join"
	PlayerLeave     = "player_leave"
	BackupProgress  = "backup_progress"
	UpgradeProgress = "upgrade_progress"

	ServerOnline    = "server_online"
	ServerOffline   = "server_offline"
	ServerCrash     = "server_crash"
	RestartWarning  = "restart_warning"
	BackupComplete  = "backup_complete"
	BackupFailed    = "backup_failed"
	UpgradeComplete = "upgrade_complete"
	MembersChanged  = "members_changed"
)

// Event is the spec's WebSocket envelope.
type Event struct {
	Type     string `json:"type"`
	ServerID string `json:"server_id"`
	TS       int64  `json:"ts"`
	Data     any    `json:"data"`
}

// New builds an event stamped with the current time.
func New(typ, serverID string, data any) Event {
	return Event{Type: typ, ServerID: serverID, TS: time.Now().Unix(), Data: data}
}

// Bus fans events out to subscribers. Publish never blocks: a subscriber
// whose buffer is full misses the event.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: make(map[chan Event]struct{})} }

// Subscribe returns a channel of events and a cancel func that closes it.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	ch := make(chan Event, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

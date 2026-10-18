package plugin

import (
	"context"
	"errors"
	"sync"

	"mcsman/internal/db"
)

// Compatibility statuses for an upgrade preflight.
const (
	Green = "green" // a release exists for the target version
	Amber = "amber" // tracked in a registry but no release for the target
	Gray  = "gray"  // manual or unknown source; cannot check
)

// Compat is one plugin's compatibility with a target Minecraft version.
type Compat struct {
	Plugin  *db.Plugin `json:"plugin"`
	Status  string     `json:"status"`
	Release *Release   `json:"release,omitempty"` // the release to install for green
	Error   string     `json:"error,omitempty"`
}

// Check classifies each plugin against targetMC, querying registries
// concurrently.
func (in *Installer) Check(ctx context.Context, plugins []*db.Plugin, targetMC string) []Compat {
	out := make([]Compat, len(plugins))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, p := range plugins {
		out[i] = Compat{Plugin: p, Status: Gray}
		if p.Source == db.SourceManual || p.SourceID == nil {
			continue
		}
		reg, err := in.registry(p.Source)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rel, err := reg.Latest(ctx, *p.SourceID, targetMC)
			switch {
			case err == nil:
				out[i].Status, out[i].Release = Green, rel
			case errors.Is(err, ErrNoCompatible):
				out[i].Status = Amber
			default:
				// Registry trouble: we can't say it's compatible.
				out[i].Status, out[i].Error = Amber, err.Error()
			}
		}()
	}
	wg.Wait()
	return out
}

// Package plugin searches Modrinth and Hangar, installs verified plugin JARs
// and checks plugin compatibility with Minecraft versions.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"mcsman/internal/fsutil"
)

var (
	ErrNotFound      = errors.New("plugin not found")
	ErrNoCompatible  = errors.New("no compatible release")
	ErrUnverifiable  = errors.New("release has no checksum or is hosted externally; refusing to install unverified file")
	ErrUnknownSource = errors.New("unknown plugin source")
)

// UserAgent is sent on every registry request. Modrinth's terms require a
// descriptive agent with contact details; the daemon sets it from config.
var UserAgent = "mcsman/1.0"

// SearchResult is one project from a registry search.
type SearchResult struct {
	Source      string `json:"source"`
	ID          string `json:"id"` // Modrinth project ID or Hangar "owner/slug"
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Downloads   int64  `json:"downloads"`
	IconURL     string `json:"icon_url"`
	PageURL     string `json:"page_url"`
}

// Release is one downloadable version of a project.
type Release struct {
	Source      string          `json:"source"`
	ProjectID   string          `json:"project_id"`
	ProjectName string          `json:"project_name"`
	Version     string          `json:"version"`    // human version string
	VersionID   string          `json:"version_id"` // registry version identifier
	Filename    string          `json:"filename"`
	URL         string          `json:"-"`
	Checksum    fsutil.Checksum `json:"-"`
	Published   time.Time       `json:"published"`
}

// Registry is a plugin source.
type Registry interface {
	Name() string
	Search(ctx context.Context, query, mcVersion string, limit int) ([]SearchResult, error)
	// Latest returns the newest stable release for mcVersion (ErrNoCompatible
	// if there is none).
	Latest(ctx context.Context, projectID, mcVersion string) (*Release, error)
	// Version returns a specific release.
	Version(ctx context.Context, projectID, versionID string) (*Release, error)
}

type httpClient struct {
	base string
	http *http.Client
}

func (c *httpClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", c.base, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

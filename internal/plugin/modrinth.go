package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"mcsman/internal/fsutil"
)

const ModrinthBaseURL = "https://api.modrinth.com"

// Loaders a Paper server can run.
var paperLoaders = []string{"paper", "bukkit", "spigot"}

type Modrinth struct{ c httpClient }

func NewModrinth(base string) *Modrinth {
	if base == "" {
		base = ModrinthBaseURL
	}
	return &Modrinth{c: httpClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}}
}

func (m *Modrinth) Name() string { return "modrinth" }

func jsonList(v ...string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (m *Modrinth) Search(ctx context.Context, query, mcVersion string, limit int) ([]SearchResult, error) {
	facets := [][]string{{"project_type:plugin"}, {"categories:paper", "categories:bukkit", "categories:spigot"}}
	if mcVersion != "" {
		facets = append(facets, []string{"versions:" + mcVersion})
	}
	fb, _ := json.Marshal(facets)
	q := url.Values{"query": {query}, "facets": {string(fb)}, "limit": {fmt.Sprint(limit)}}
	var resp struct {
		Hits []struct {
			ProjectID   string `json:"project_id"`
			Slug        string `json:"slug"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Author      string `json:"author"`
			Downloads   int64  `json:"downloads"`
			IconURL     string `json:"icon_url"`
		} `json:"hits"`
	}
	if err := m.c.getJSON(ctx, "/v2/search?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		out = append(out, SearchResult{
			Source: "modrinth", ID: h.ProjectID, Slug: h.Slug, Name: h.Title, Description: h.Description,
			Author: h.Author, Downloads: h.Downloads, IconURL: h.IconURL,
			PageURL: "https://modrinth.com/plugin/" + h.Slug,
		})
	}
	return out, nil
}

type modrinthVersion struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Name          string    `json:"name"`
	VersionNumber string    `json:"version_number"`
	VersionType   string    `json:"version_type"`
	Published     time.Time `json:"date_published"`
	Loaders       []string  `json:"loaders"`
	Files         []struct {
		Filename string            `json:"filename"`
		URL      string            `json:"url"`
		Primary  bool              `json:"primary"`
		Hashes   map[string]string `json:"hashes"`
	} `json:"files"`
}

func (m *Modrinth) projectName(ctx context.Context, id string) string {
	var p struct {
		Title string `json:"title"`
	}
	if m.c.getJSON(ctx, "/v2/project/"+url.PathEscape(id), &p) == nil {
		return p.Title
	}
	return id
}

func (m *Modrinth) toRelease(ctx context.Context, v *modrinthVersion) (*Release, error) {
	var file *struct {
		Filename string            `json:"filename"`
		URL      string            `json:"url"`
		Primary  bool              `json:"primary"`
		Hashes   map[string]string `json:"hashes"`
	}
	for i := range v.Files {
		if v.Files[i].Primary || file == nil {
			file = &v.Files[i]
		}
	}
	if file == nil {
		return nil, ErrNoCompatible
	}
	// Modrinth publishes sha512 and sha1; sha512 is the strong one.
	sum := file.Hashes["sha512"]
	if sum == "" {
		return nil, ErrUnverifiable
	}
	return &Release{
		Source: "modrinth", ProjectID: v.ProjectID, ProjectName: m.projectName(ctx, v.ProjectID),
		Version: v.VersionNumber, VersionID: v.ID, Filename: file.Filename, URL: file.URL,
		Checksum: fsutil.Checksum{Algo: "sha512", Hex: sum}, Published: v.Published,
	}, nil
}

func (m *Modrinth) Latest(ctx context.Context, projectID, mcVersion string) (*Release, error) {
	q := url.Values{"loaders": {jsonList(paperLoaders...)}}
	if mcVersion != "" {
		q.Set("game_versions", jsonList(mcVersion))
	}
	var versions []modrinthVersion
	if err := m.c.getJSON(ctx, "/v2/project/"+url.PathEscape(projectID)+"/version?"+q.Encode(), &versions); err != nil {
		return nil, err
	}
	// Newest first; prefer releases over beta/alpha.
	for _, want := range []string{"release", "beta"} {
		for i := range versions {
			if versions[i].VersionType == want {
				return m.toRelease(ctx, &versions[i])
			}
		}
	}
	return nil, ErrNoCompatible
}

func (m *Modrinth) Version(ctx context.Context, projectID, versionID string) (*Release, error) {
	var v modrinthVersion
	if err := m.c.getJSON(ctx, "/v2/version/"+url.PathEscape(versionID), &v); err != nil {
		return nil, err
	}
	if v.ProjectID != projectID {
		// Modrinth accepts slugs too; resolve the canonical project ID.
		var p struct {
			ID string `json:"id"`
		}
		if err := m.c.getJSON(ctx, "/v2/project/"+url.PathEscape(projectID), &p); err != nil || p.ID != v.ProjectID {
			return nil, fmt.Errorf("version %s does not belong to project %s", versionID, projectID)
		}
	}
	return m.toRelease(ctx, &v)
}

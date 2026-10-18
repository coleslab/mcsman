package plugin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcsman/internal/fsutil"
)

const HangarBaseURL = "https://hangar.papermc.io"

type Hangar struct{ c httpClient }

func NewHangar(base string) *Hangar {
	if base == "" {
		base = HangarBaseURL
	}
	return &Hangar{c: httpClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}}
}

func (h *Hangar) Name() string { return "hangar" }

// splitID turns "owner/slug" into its parts.
func splitID(id string) (string, string, error) {
	owner, slug, ok := strings.Cut(id, "/")
	if !ok || owner == "" || slug == "" || strings.Contains(slug, "/") {
		return "", "", fmt.Errorf("%w: hangar id must be owner/slug", ErrNotFound)
	}
	return owner, slug, nil
}

func (h *Hangar) Search(ctx context.Context, query, mcVersion string, limit int) ([]SearchResult, error) {
	q := url.Values{"q": {query}, "platform": {"PAPER"}, "limit": {fmt.Sprint(limit)}}
	if mcVersion != "" {
		q.Set("version", mcVersion)
	}
	var resp struct {
		Result []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			AvatarURL   string `json:"avatarUrl"`
			Namespace   struct {
				Owner string `json:"owner"`
				Slug  string `json:"slug"`
			} `json:"namespace"`
			Stats struct {
				Downloads int64 `json:"downloads"`
			} `json:"stats"`
		} `json:"result"`
	}
	if err := h.c.getJSON(ctx, "/api/v1/projects?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Result))
	for _, p := range resp.Result {
		id := p.Namespace.Owner + "/" + p.Namespace.Slug
		out = append(out, SearchResult{
			Source: "hangar", ID: id, Slug: p.Namespace.Slug, Name: p.Name, Description: p.Description,
			Author: p.Namespace.Owner, Downloads: p.Stats.Downloads, IconURL: p.AvatarURL,
			PageURL: "https://hangar.papermc.io/" + id,
		})
	}
	return out, nil
}

type hangarVersion struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Channel   struct {
		Name string `json:"name"`
	} `json:"channel"`
	Downloads map[string]struct {
		FileInfo *struct {
			Name       string `json:"name"`
			SHA256Hash string `json:"sha256Hash"`
		} `json:"fileInfo"`
		ExternalURL *string `json:"externalUrl"`
		DownloadURL *string `json:"downloadUrl"`
	} `json:"downloads"`
}

func (h *Hangar) toRelease(id string, v *hangarVersion) (*Release, error) {
	d, ok := v.Downloads["PAPER"]
	if !ok {
		return nil, ErrNoCompatible
	}
	if d.FileInfo == nil || d.FileInfo.SHA256Hash == "" || d.DownloadURL == nil {
		return nil, ErrUnverifiable
	}
	_, slug, _ := splitID(id)
	return &Release{
		Source: "hangar", ProjectID: id, ProjectName: slug, Version: v.Name, VersionID: v.Name,
		Filename: d.FileInfo.Name, URL: *d.DownloadURL,
		Checksum: fsutil.SHA256(d.FileInfo.SHA256Hash), Published: v.CreatedAt,
	}, nil
}

func (h *Hangar) Latest(ctx context.Context, projectID, mcVersion string) (*Release, error) {
	owner, slug, err := splitID(projectID)
	if err != nil {
		return nil, err
	}
	q := url.Values{"platform": {"PAPER"}, "limit": {"25"}}
	if mcVersion != "" {
		q.Set("platformVersion", mcVersion)
	}
	var resp struct {
		Result []hangarVersion `json:"result"`
	}
	path := fmt.Sprintf("/api/v1/projects/%s/%s/versions?%s", url.PathEscape(owner), url.PathEscape(slug), q.Encode())
	if err := h.c.getJSON(ctx, path, &resp); err != nil {
		return nil, err
	}
	// Results are newest first. Prefer the Release channel, then Beta;
	// never pick snapshot/dev builds automatically.
	for _, want := range []string{"release", "beta"} {
		for i := range resp.Result {
			if strings.EqualFold(resp.Result[i].Channel.Name, want) {
				return h.toRelease(projectID, &resp.Result[i])
			}
		}
	}
	return nil, ErrNoCompatible
}

func (h *Hangar) Version(ctx context.Context, projectID, versionID string) (*Release, error) {
	owner, slug, err := splitID(projectID)
	if err != nil {
		return nil, err
	}
	var v hangarVersion
	path := fmt.Sprintf("/api/v1/projects/%s/%s/versions/%s", url.PathEscape(owner), url.PathEscape(slug), url.PathEscape(versionID))
	if err := h.c.getJSON(ctx, path, &v); err != nil {
		return nil, err
	}
	return h.toRelease(projectID, &v)
}

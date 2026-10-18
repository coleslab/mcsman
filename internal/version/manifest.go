// Package version fetches Paper versions and builds from the PaperMC Fill v3
// API and downloads verified server JARs.
package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://fill.papermc.io"
	cacheTTL       = time.Hour
)

// UserAgent is sent on every request. PaperMC asks clients to identify
// themselves with contact details; the daemon sets this from config.
var UserAgent = "mcsman/1.0"

var ErrUnknownVersion = errors.New("unknown Paper version")

// Client talks to the Fill v3 API and caches the version list.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu        sync.Mutex
	versions  []string
	fetchedAt time.Time
}

func NewClient() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Build is one downloadable Paper build.
type Build struct {
	Version  string `json:"version"`
	ID       int    `json:"id"`
	Channel  string `json:"channel"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// VersionInfo carries the Java requirements for a Minecraft version.
type VersionInfo struct {
	ID          string   `json:"id"`
	JavaMinimum int      `json:"java_minimum"`
	JavaFlags   []string `json:"java_flags"`
	Supported   bool     `json:"supported"`
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrUnknownVersion
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("paper api %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Versions returns release versions (no pre-releases/RCs), newest first.
// The list is cached for an hour; force bypasses the cache.
func (c *Client) Versions(ctx context.Context, force bool) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.versions != nil && time.Since(c.fetchedAt) < cacheTTL {
		return c.versions, nil
	}
	var resp struct {
		Versions map[string][]string `json:"versions"`
	}
	if err := c.get(ctx, "/v3/projects/paper", &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, group := range resp.Versions {
		for _, v := range group {
			if isRelease(v) {
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return Compare(out[i], out[j]) > 0 })
	c.versions, c.fetchedAt = out, time.Now()
	return out, nil
}

// Exists reports whether v is a known release version.
func (c *Client) Exists(ctx context.Context, v string) (bool, error) {
	vs, err := c.Versions(ctx, false)
	if err != nil {
		return false, err
	}
	for _, x := range vs {
		if x == v {
			return true, nil
		}
	}
	return false, nil
}

// Info returns the Java requirements for version v.
func (c *Client) Info(ctx context.Context, v string) (*VersionInfo, error) {
	var resp struct {
		Version struct {
			ID      string `json:"id"`
			Support struct {
				Status string `json:"status"`
			} `json:"support"`
			Java struct {
				Version struct {
					Minimum int `json:"minimum"`
				} `json:"version"`
				Flags struct {
					Recommended []string `json:"recommended"`
				} `json:"flags"`
			} `json:"java"`
		} `json:"version"`
	}
	if err := c.get(ctx, "/v3/projects/paper/versions/"+url.PathEscape(v), &resp); err != nil {
		return nil, err
	}
	return &VersionInfo{
		ID:          resp.Version.ID,
		JavaMinimum: resp.Version.Java.Version.Minimum,
		JavaFlags:   resp.Version.Java.Flags.Recommended,
		Supported:   resp.Version.Support.Status == "SUPPORTED",
	}, nil
}

// LatestBuild returns the newest build for version v, preferring STABLE.
func (c *Client) LatestBuild(ctx context.Context, v string) (*Build, error) {
	var resp struct {
		ID        int    `json:"id"`
		Channel   string `json:"channel"`
		Downloads map[string]struct {
			Name      string `json:"name"`
			Checksums struct {
				SHA256 string `json:"sha256"`
			} `json:"checksums"`
			Size int64  `json:"size"`
			URL  string `json:"url"`
		} `json:"downloads"`
	}
	if err := c.get(ctx, "/v3/projects/paper/versions/"+url.PathEscape(v)+"/builds/latest", &resp); err != nil {
		return nil, err
	}
	d, ok := resp.Downloads["server:default"]
	if !ok || d.Checksums.SHA256 == "" || d.URL == "" {
		return nil, fmt.Errorf("paper %s build %d has no server download", v, resp.ID)
	}
	return &Build{
		Version: v, ID: resp.ID, Channel: resp.Channel,
		Filename: d.Name, URL: d.URL, SHA256: d.Checksums.SHA256, Size: d.Size,
	}, nil
}

// isRelease filters out snapshots, pre-releases and release candidates.
func isRelease(v string) bool {
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return v != ""
}

// Compare compares dotted numeric versions: -1, 0 or 1.
func Compare(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

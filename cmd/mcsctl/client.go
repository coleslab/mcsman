package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// client talks to the daemon's REST API over the Unix socket.
type client struct {
	socket string
	http   *http.Client
}

func newClient(socket string) *client {
	return &client{
		socket: socket,
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		}},
	}
}

type apiError struct {
	Status int
	Msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Msg, e.Status) }

// do sends a JSON request and decodes a JSON response into out (if non-nil).
func (c *client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://mcsman"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach daemon at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &apiError{Status: resp.StatusCode, Msg: e.Error}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func serverPath(name string, rest string) string {
	return "/api/servers/" + url.PathEscape(name) + rest
}

// serverInfo mirrors the admin server view.
type serverInfo struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Directory         string   `json:"directory"`
	MCVersion         string   `json:"mc_version"`
	JavaVersion       string   `json:"java_version"`
	Port              int      `json:"port"`
	RCONPort          int      `json:"rcon_port"`
	MaxMemoryMB       int      `json:"max_memory_mb"`
	AutoRestart       bool     `json:"auto_restart"`
	CreatedAt         int64    `json:"created_at"`
	Status            string   `json:"status"`
	ConnectionAddress *string  `json:"connection_address"`
	PID               int      `json:"pid"`
	PlayerCount       int      `json:"player_count"`
	Players           []string `json:"players"`
}

func ago(unix int64) string {
	return time.Since(time.Unix(unix, 0)).Round(time.Second).String()
}

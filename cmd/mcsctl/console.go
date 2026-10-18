package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"

	"github.com/gorilla/websocket"
)

type wsEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// cmdConsole attaches to a server console. Output streams to stdout; each
// line typed on stdin is sent to the server. Ctrl-D or Ctrl-C detaches
// (the server keeps running).
func cmdConsole(c *client, name string) error {
	d := websocket.Dialer{
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var nd net.Dialer
			return nd.DialContext(ctx, "unix", c.socket)
		},
	}
	conn, resp, err := d.Dial("ws://mcsman"+serverPath(name, "/console"), http.Header{})
	if err != nil {
		if resp != nil {
			var e struct {
				Error string `json:"error"`
			}
			json.NewDecoder(resp.Body).Decode(&e)
			if e.Error != "" {
				return fmt.Errorf("%s (HTTP %d)", e.Error, resp.StatusCode)
			}
		}
		return err
	}
	defer conn.Close()
	fmt.Fprintf(os.Stderr, "Attached to %s. Type commands and press Enter; Ctrl-D to detach.\n", name)

	done := make(chan error, 1)
	go func() {
		for {
			var e wsEvent
			if err := conn.ReadJSON(&e); err != nil {
				done <- fmt.Errorf("connection closed: %w", err)
				return
			}
			switch e.Type {
			case "console_output":
				var d struct {
					Line string `json:"line"`
				}
				json.Unmarshal(e.Data, &d)
				fmt.Println(d.Line)
			case "server_status":
				var d struct {
					Status string `json:"status"`
				}
				json.Unmarshal(e.Data, &d)
				fmt.Fprintf(os.Stderr, "\x1b[2m[mcsman] status: %s\x1b[0m\n", d.Status)
			}
		}
	}()
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			msg := map[string]string{"type": "input", "data": sc.Text() + "\n"}
			if err := conn.WriteJSON(msg); err != nil {
				done <- err
				return
			}
		}
		done <- nil // EOF: detach
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	select {
	case err := <-done:
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		if err == nil {
			fmt.Fprintln(os.Stderr, "Detached.")
		}
		return err
	case <-sig:
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		fmt.Fprintln(os.Stderr, "\nDetached.")
		return nil
	}
}

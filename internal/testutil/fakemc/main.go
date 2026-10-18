// Command fakemc imitates a Paper server closely enough to exercise mcsman
// without Java: it reads server.properties, prints Paper-style startup logs,
// binds the game port, serves RCON and reacts to console input.
//
// Console/RCON commands: stop, crash, say <msg>, list, tps, mspt,
// save-all, save-off, save-on, whitelist add <name>.
// Marker files in the working directory alter behaviour: "fakemc.ignore-stop"
// makes it ignore the stop command and "fakemc.ignore-term" makes it ignore
// SIGTERM (to exercise the stop escalation path).
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	outMu   sync.Mutex
	players []string
)

func logf(format string, a ...any) {
	outMu.Lock()
	defer outMu.Unlock()
	fmt.Printf("[%s INFO]: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

func main() {
	props := readProps("server.properties")
	if exists("fakemc.ignore-term") {
		signal.Ignore(syscall.SIGTERM)
	}

	logf("Starting minecraft server version 1.21.1")
	makeWorld()
	game, err := net.Listen("tcp", "127.0.0.1:"+props["server-port"])
	if err != nil {
		logf("**** FAILED TO BIND TO PORT! %v", err)
		os.Exit(1)
	}
	defer game.Close()
	go func() {
		for {
			c, err := game.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	if props["enable-rcon"] == "true" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+props["rcon.port"])
		if err != nil {
			logf("Failed to start RCON: %v", err)
			os.Exit(1)
		}
		go serveRCON(ln, props["rcon.password"])
		logf("RCON running on 127.0.0.1:%s", props["rcon.port"])
	}
	time.Sleep(200 * time.Millisecond)
	logf(`Done (0.200s)! For help, type "help"`)

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		out := handle(strings.TrimSpace(sc.Text()))
		if out != "" {
			logf("%s", out)
		}
	}
	// Stdin closed (PTY hung up): behave like a killed server.
	os.Exit(1)
}

// makeWorld creates a small world directory like a real server would.
func makeWorld() {
	os.MkdirAll("world/region", 0o750)
	if !exists("world/level.dat") {
		os.WriteFile("world/level.dat", []byte("level"), 0o640)
		os.WriteFile("world/region/r.0.0.mca", make([]byte, 64<<10), 0o640)
	}
	os.WriteFile("world/session.lock", []byte("lock"), 0o640)
}

// record appends each received command to fakemc.commands for tests.
func record(cmd string) {
	f, err := os.OpenFile("fakemc.commands", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, cmd)
}

func handle(cmd string) string {
	if cmd != "" {
		record(cmd)
	}
	switch {
	case cmd == "stop" && exists("fakemc.ignore-stop"):
		return "Ignoring stop"
	case cmd == "stop":
		logf("Stopping the server")
		go func() { time.Sleep(100 * time.Millisecond); os.Exit(0) }()
		return "Stopping server"
	case cmd == "crash":
		logf("Encountered an unexpected exception")
		os.Exit(1)
	case strings.HasPrefix(cmd, "say "):
		return "[Server] " + strings.TrimPrefix(cmd, "say ")
	case cmd == "list":
		return fmt.Sprintf("There are %d of a max of 20 players online: %s", len(players), strings.Join(players, ", "))
	case cmd == "tps":
		return "§6TPS from last 1m, 5m, 15m: §a20.0, §a19.98, §a*20.0"
	case cmd == "mspt":
		return "§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §a1.2§7/§a0.8§7/§a3.4§e, §a1.3§7/§a0.8§7/§a4.0§e, §a1.1§7/§a0.7§7/§a5.2"
	case cmd == "save-all" || cmd == "save-all flush":
		return "Saved the game"
	case cmd == "save-off":
		return "Automatic saving is now disabled"
	case cmd == "save-on":
		return "Automatic saving is now enabled"
	case strings.HasPrefix(cmd, "whitelist add "):
		return "Added " + strings.TrimPrefix(cmd, "whitelist add ") + " to the whitelist"
	case strings.HasPrefix(cmd, "fake-join "):
		name := strings.TrimPrefix(cmd, "fake-join ")
		players = append(players, name)
		return name + " joined the game"
	case cmd == "":
		return ""
	}
	return "Unknown command. Type \"/help\" for help."
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readProps(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			m[k] = v
		}
	}
	return m
}

// Minimal RCON server (Source RCON protocol, as used by Minecraft).
const (
	typeResponse = 0
	typeCommand  = 2
	typeAuthResp = 2
	typeAuth     = 3
)

func serveRCON(ln net.Listener, password string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			authed := false
			for {
				id, typ, body, err := readPacket(c)
				if err != nil {
					return
				}
				switch typ {
				case typeAuth:
					if body == password {
						authed = true
						writePacket(c, id, typeAuthResp, "")
					} else {
						writePacket(c, -1, typeAuthResp, "")
					}
				case typeCommand:
					if !authed {
						return
					}
					writePacket(c, id, typeResponse, handle(body))
				}
			}
		}()
	}
}

func readPacket(r io.Reader) (int32, int32, string, error) {
	var size int32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return 0, 0, "", err
	}
	if size < 10 || size > 4110 {
		return 0, 0, "", fmt.Errorf("bad size %d", size)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, 0, "", err
	}
	id := int32(binary.LittleEndian.Uint32(buf[0:4]))
	typ := int32(binary.LittleEndian.Uint32(buf[4:8]))
	return id, typ, string(buf[8 : size-2]), nil
}

func writePacket(w io.Writer, id, typ int32, body string) {
	size := int32(len(body) + 10)
	buf := make([]byte, 4+size)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(size))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:12], uint32(typ))
	copy(buf[12:], body)
	w.Write(buf)
}

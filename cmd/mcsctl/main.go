// Command mcsctl is the admin CLI for the mcsman daemon. It talks to the
// daemon over its Unix socket; access to the socket is the authorisation.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

const usage = `mcsctl — admin CLI for mcsman

Usage:
  mcsctl [--socket PATH] [--json] <command> [args]

Commands:
  init [--username NAME]                 create the first admin account
  server list
  server status <name>
  server create <name> --version V --accept-eula [--port N] [--rcon-port N]
                [--memory MB] [--java N] [--address HOST]
  server start <name>
  server stop <name> [--delay SECONDS]
  server restart <name> [--delay SECONDS]
  server console <name>                  attach to the live console (Ctrl-D to detach)
  server cmd <name> <command...>         run a command via RCON
  server delete <name>

  backup create <server> [--tag manual]
  backup list <server>
  backup restore <server> <backup-id>   server must be stopped

  invite create [--expires 48h] [--whitelist <server>]
  invite list
  invite revoke <invite-id>

  user list
  user role <username> admin|player
  user remove <username>

Environment:
  MCSCTL_SOCKET   socket path (default /run/mcsman/mcsman.sock)
`

var jsonOut bool

func main() {
	socket := envOr("MCSCTL_SOCKET", "/run/mcsman/mcsman.sock")
	fs := flag.NewFlagSet("mcsctl", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.StringVar(&socket, "socket", socket, "daemon socket path")
	fs.BoolVar(&jsonOut, "json", false, "print raw JSON")
	fs.Parse(os.Args[1:])

	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		os.Exit(2)
	}
	c := newClient(socket)
	if err := dispatch(c, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func dispatch(c *client, args []string) error {
	switch args[0] {
	case "init":
		return cmdInit(c, args[1:])
	case "server":
		if len(args) < 2 {
			return errors.New("missing server subcommand")
		}
		return dispatchServer(c, args[1], args[2:])
	case "backup":
		if len(args) < 2 {
			return errors.New("missing backup subcommand")
		}
		return dispatchBackup(c, args[1], args[2:])
	case "invite":
		if len(args) < 2 {
			return errors.New("missing invite subcommand")
		}
		return dispatchInvite(c, args[1], args[2:])
	case "user":
		if len(args) < 2 {
			return errors.New("missing user subcommand")
		}
		return dispatchUser(c, args[1], args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (see mcsctl help)", args[0])
}

func dispatchServer(c *client, sub string, args []string) error {
	switch sub {
	case "list":
		return cmdServerList(c)
	case "status":
		return withName(args, func(n string, _ []string) error { return cmdServerStatus(c, n) })
	case "create":
		return cmdServerCreate(c, args)
	case "start":
		return withName(args, func(n string, _ []string) error {
			var s serverInfo
			if err := c.do(http.MethodPost, serverPath(n, "/start"), nil, &s); err != nil {
				return err
			}
			fmt.Printf("%s: %s (pid %d)\n", s.Name, s.Status, s.PID)
			return nil
		})
	case "stop", "restart":
		return withName(args, func(n string, rest []string) error {
			fs := flag.NewFlagSet(sub, flag.ExitOnError)
			delay := fs.Int("delay", 0, "countdown in seconds")
			fs.Parse(rest)
			if *delay > 0 {
				fmt.Printf("%s: %s in %ds…\n", n, map[string]string{"stop": "stopping", "restart": "restarting"}[sub], *delay)
			}
			var s serverInfo
			err := c.do(http.MethodPost, serverPath(n, "/"+sub+"?wait=true"), map[string]int{"delay_seconds": *delay}, &s)
			if err != nil {
				return err
			}
			fmt.Printf("%s: %s\n", s.Name, s.Status)
			return nil
		})
	case "console":
		return withName(args, func(n string, _ []string) error { return cmdConsole(c, n) })
	case "cmd":
		return withName(args, func(n string, rest []string) error {
			if len(rest) == 0 {
				return errors.New("missing command")
			}
			var out struct {
				Output string `json:"output"`
			}
			if err := c.do(http.MethodPost, serverPath(n, "/command"), map[string]string{"command": strings.Join(rest, " ")}, &out); err != nil {
				return err
			}
			fmt.Println(out.Output)
			return nil
		})
	case "delete":
		return withName(args, func(n string, _ []string) error {
			if !confirm(fmt.Sprintf("Delete server %q and ALL its data and backups? Type the name to confirm: ", n), n) {
				return errors.New("aborted")
			}
			if err := c.do(http.MethodDelete, serverPath(n, ""), map[string]string{"confirm": n}, nil); err != nil {
				return err
			}
			fmt.Println("deleted", n)
			return nil
		})
	}
	return fmt.Errorf("unknown server subcommand %q", sub)
}

func withName(args []string, fn func(name string, rest []string) error) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("missing server name")
	}
	return fn(args[0], args[1:])
}

func cmdInit(c *client, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	username := fs.String("username", "", "admin username")
	fs.Parse(args)
	if *username == "" {
		*username = prompt("Admin username: ")
	}
	pw, err := readPassword("Password: ")
	if err != nil {
		return err
	}
	again, err := readPassword("Confirm password: ")
	if err != nil {
		return err
	}
	if pw != again {
		return errors.New("passwords do not match")
	}
	if err := c.do(http.MethodPost, "/api/init", map[string]string{"username": *username, "password": pw}, nil); err != nil {
		return err
	}
	fmt.Printf("Admin %q created. Log in at the web UI.\n", *username)
	return nil
}

func cmdServerList(c *client) error {
	var list []serverInfo
	if err := c.do(http.MethodGet, "/api/servers", nil, &list); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(list)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tVERSION\tPORT\tPLAYERS\tADDRESS")
	for _, s := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", s.Name, s.Status, s.MCVersion, s.Port, s.PlayerCount, deref(s.ConnectionAddress))
	}
	return tw.Flush()
}

func cmdServerStatus(c *client, name string) error {
	var s serverInfo
	if err := c.do(http.MethodGet, serverPath(name, ""), nil, &s); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(s)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	row := func(k string, v any) { fmt.Fprintf(tw, "%s:\t%v\n", k, v) }
	row("Name", s.Name)
	row("ID", s.ID)
	row("Status", s.Status)
	if s.PID != 0 {
		row("PID", s.PID)
	}
	row("Minecraft", s.MCVersion)
	row("Java", s.JavaVersion)
	row("Port", s.Port)
	row("RCON port", s.RCONPort)
	row("Memory", fmt.Sprintf("%d MB", s.MaxMemoryMB))
	row("Auto-restart", s.AutoRestart)
	row("Address", deref(s.ConnectionAddress))
	row("Directory", s.Directory)
	row("Players", fmt.Sprintf("%d %v", s.PlayerCount, s.Players))
	row("Created", ago(s.CreatedAt)+" ago")
	return tw.Flush()
}

func cmdServerCreate(c *client, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("missing server name")
	}
	name := args[0]
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	version := fs.String("version", "", "Minecraft version (required)")
	port := fs.Int("port", 0, "game port (default 25565)")
	rconPort := fs.Int("rcon-port", 0, "RCON port (default port+10)")
	memory := fs.Int("memory", 0, "max memory in MB (default 2048)")
	javaV := fs.String("java", "", "Java major version (default: derived from MC version)")
	address := fs.String("address", "", "public connection address shown to players")
	eula := fs.Bool("accept-eula", false, "accept the Minecraft EULA (https://aka.ms/MinecraftEULA)")
	fs.Parse(args[1:])
	if *version == "" {
		return errors.New("--version is required")
	}
	body := map[string]any{
		"name": name, "mc_version": *version, "port": *port, "rcon_port": *rconPort,
		"max_memory_mb": *memory, "java_version": *javaV, "accept_eula": *eula,
	}
	if *address != "" {
		body["connection_address"] = *address
	}
	var s serverInfo
	if err := c.do(http.MethodPost, "/api/servers", body, &s); err != nil {
		return err
	}
	if jsonOut {
		return printJSON(s)
	}
	fmt.Printf("Created %s (port %d, rcon %d, java %s)\nDirectory: %s\n", s.Name, s.Port, s.RCONPort, s.JavaVersion, s.Directory)
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var stdin = bufio.NewReader(os.Stdin)

func prompt(msg string) string {
	fmt.Print(msg)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func confirm(msg, want string) bool { return prompt(msg) == want }

func readPassword(msg string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return prompt(msg), nil
	}
	fmt.Print(msg)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return string(b), err
}

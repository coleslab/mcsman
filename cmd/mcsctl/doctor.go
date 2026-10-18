package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mcsman/internal/buildinfo"
	"mcsman/internal/config"
)

// systemInfo mirrors GET /api/system (socket only).
type systemInfo struct {
	Version        string   `json:"version"`
	Users          int      `json:"users"`
	Listen         string   `json:"listen"`
	BaseURL        string   `json:"base_url"`
	TrustedProxies []string `json:"trusted_proxies"`
	DataDir        string   `json:"data_dir"`
	DataFreeBytes  *uint64  `json:"data_free_bytes"`
}

func cmdVersion(c *client) error {
	fmt.Println("mcsctl", buildinfo.Version)
	var s systemInfo
	if err := c.do(http.MethodGet, "/api/system", nil, &s); err != nil {
		fmt.Println("daemon: unreachable:", err)
		return nil
	}
	fmt.Println("daemon", s.Version)
	return nil
}

// report prints doctor results and counts failures.
type report struct{ fails int }

func (r *report) ok(format string, a ...any) { fmt.Printf("ok    "+format+"\n", a...) }
func (r *report) warn(fix, format string, a ...any) {
	fmt.Printf("warn  "+format+"\n", a...)
	if fix != "" {
		fmt.Println("      fix:", fix)
	}
}
func (r *report) fail(fix, format string, a ...any) {
	r.fails++
	fmt.Printf("FAIL  "+format+"\n", a...)
	if fix != "" {
		fmt.Println("      fix:", fix)
	}
}
func (r *report) skip(format string, a ...any) { fmt.Printf("skip  "+format+"\n", a...) }

// cmdDoctor checks the installation and prints a fix for each problem. It
// runs as any socket user; with sudo it also checks the config and secret.
func cmdDoctor(c *client, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/mcsman/mcsman.yaml", "config file")
	envPath := fs.String("env", "/etc/mcsman/mcsman.env", "service EnvironmentFile")
	fs.Parse(args)
	r := &report{}

	unitUser := checkService(r)
	checkConfig(r, *cfgPath, *envPath, unitUser)
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(*cfgPath), "*.pacnew")); len(m) > 0 {
		r.warn("sudo pacdiff", "new default config waiting to be merged: %s", strings.Join(m, " "))
	}
	checkDaemon(r, c, unitUser)

	if r.fails > 0 {
		return fmt.Errorf("%d problem(s) found", r.fails)
	}
	fmt.Println("\nNo problems found.")
	return nil
}

// checkService finds the active mcsman@USER unit and returns USER ("" if none).
func checkService(r *report) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		r.skip("systemd service (no systemctl)")
		return ""
	}
	out, err := exec.Command("systemctl", "list-units", "mcsman@*.service", "--state=active", "--no-legend", "--plain").Output()
	if err != nil {
		r.skip("systemd service: %v", err)
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		r.fail("sudo mcsman-setup (or: sudo systemctl enable --now mcsman@$USER)", "no mcsman@USER service is running")
		return ""
	}
	u := strings.TrimSuffix(strings.TrimPrefix(fields[0], "mcsman@"), ".service")
	r.ok("service %s is running", fields[0])
	return u
}

func checkConfig(r *report, cfgPath, envPath, unitUser string) {
	fi, err := os.Stat(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		r.fail("reinstall the package or copy packaging/mcsman.example.yaml", "%s does not exist", cfgPath)
		return
	}
	if err != nil {
		r.skip("config: %v (run with sudo to check config and secret)", err)
		return
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		// The daemon refuses to load it, so contents can't be checked yet.
		r.fail("sudo chmod 600 "+cfgPath, "%s has mode %#o, must be 0600", cfgPath, perm)
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && unitUser != "" {
		owner := strconv.Itoa(int(st.Uid))
		if u, err := user.LookupId(owner); err == nil {
			owner = u.Username
		}
		if owner != unitUser {
			r.fail(fmt.Sprintf("sudo chown %s: %s", unitUser, cfgPath), "%s is owned by %s, but mcsman runs as %s", cfgPath, owner, unitUser)
		}
	}

	env, envErr := readEnvFile(envPath)
	switch {
	case envErr != nil:
		r.skip("secret: %v (run with sudo to check it)", envErr)
		// Let the rest of the config validate without the secret.
		env = map[string]string{"MCSMAN_JWT_SECRET": strings.Repeat("x", 32)}
	case len(env["MCSMAN_JWT_SECRET"]) < 32:
		r.fail("sudo mcsman-setup secret", "MCSMAN_JWT_SECRET in %s is missing or shorter than 32 characters", envPath)
		env["MCSMAN_JWT_SECRET"] = strings.Repeat("x", 32)
	default:
		r.ok("secret is set")
	}
	if _, err := config.LoadWithEnv(cfgPath, func(k string) string { return env[k] }); err != nil {
		if errors.Is(err, os.ErrPermission) {
			r.skip("config contents: %v (run with sudo)", err)
			return
		}
		r.fail("edit "+cfgPath, "config is invalid: %v", err)
		return
	}
	r.ok("config %s is valid", cfgPath)
}

// readEnvFile parses a systemd EnvironmentFile (KEY=VALUE, # comments,
// optional quotes); later assignments win, as in systemd.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}

func checkDaemon(r *report, c *client, unitUser string) {
	var s systemInfo
	if err := c.do(http.MethodGet, "/api/system", nil, &s); err != nil {
		fix := "check the log: journalctl -u mcsman@" + orDefault(unitUser, "USER") + " -e"
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ECONNRESET) || strings.Contains(err.Error(), "EOF") {
			fix = "add yourself to the service user's group (sudo usermod -aG " + orDefault(unitUser, "USER") + " $USER), then log in again"
		}
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			fix = "the daemon is older than mcsctl; restart it: sudo systemctl restart mcsman@" + orDefault(unitUser, "USER")
		}
		r.fail(fix, "cannot query the daemon: %v", err)
		return
	}
	r.ok("daemon %s reachable over the socket", s.Version)
	if s.Version != buildinfo.Version {
		r.warn("restart it: sudo systemctl restart mcsman@"+orDefault(unitUser, "USER"),
			"daemon runs %s but mcsctl is %s (updated without restarting?)", s.Version, buildinfo.Version)
	}
	if s.Users == 0 {
		r.fail("mcsctl init", "no users yet; nobody can log in")
	} else {
		r.ok("%d user(s)", s.Users)
	}
	if s.DataFreeBytes != nil && *s.DataFreeBytes < 5<<30 {
		r.warn("free space or lower backup keep counts", "only %.1f GiB free in %s", float64(*s.DataFreeBytes)/(1<<30), s.DataDir)
	}
	checkBaseURL(r, s)
	checkServers(r, c)
}

func checkBaseURL(r *report, s systemInfo) {
	switch {
	case s.BaseURL == "":
		r.warn("set http.base_url in the config (sudo mcsman-setup)", "http.base_url is empty; invite links won't work")
		return
	case strings.Contains(s.BaseURL, "example.com"):
		r.warn("set http.base_url to your real URL (sudo mcsman-setup)", "http.base_url is still the example (%s)", s.BaseURL)
		return
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	resp, err := hc.Get(strings.TrimSuffix(s.BaseURL, "/") + "/api/status")
	tunnelFix := fmt.Sprintf("point your tunnel/proxy at the address mcsman listens on (%s); for cloudflared in Docker use the network's gateway IP, not localhost", s.Listen)
	if err != nil {
		r.fail(tunnelFix+", and check DNS", "%s is not reachable: %v", s.BaseURL, err)
		return
	}
	defer resp.Body.Close()
	var body struct {
		Servers json.RawMessage `json:"servers"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.Servers == nil {
		r.fail(tunnelFix, "%s answers HTTP %d, but not as mcsman", s.BaseURL, resp.StatusCode)
		return
	}
	r.ok("%s reaches mcsman", s.BaseURL)
	if strings.HasPrefix(s.BaseURL, "https://") && len(s.TrustedProxies) == 0 {
		r.warn("add your tunnel/proxy's IP or subnet to http.trusted_proxies (sudo mcsman-setup)",
			"http.trusted_proxies is empty; behind a tunnel every login shares one rate limit")
	}
}

func checkServers(r *report, c *client) {
	var list []serverInfo
	if err := c.do(http.MethodGet, "/api/servers", nil, &list); err != nil {
		r.fail("", "cannot list servers: %v", err)
		return
	}
	if len(list) == 0 {
		r.warn("mcsctl server create NAME --version V --accept-eula", "no servers yet")
		return
	}
	var rcon []string
	for _, s := range list {
		rcon = append(rcon, strconv.Itoa(s.RCONPort))
		if s.ConnectionAddress == nil || *s.ConnectionAddress == "" {
			r.warn("set it in Server → Settings → Public address (e.g. your playit.gg address)",
				"%s has no public address; players won't see where to connect", s.Name)
		}
		if s.Status == "crashed" {
			r.warn("check the console: mcsctl server console "+s.Name, "%s crashed", s.Name)
		}
		if s.Status != "stopped" && s.Status != "crashed" {
			continue
		}
		for _, p := range []int{s.Port, s.RCONPort} {
			if ln, err := net.Listen("tcp", ":"+strconv.Itoa(p)); err != nil {
				r.fail(fmt.Sprintf("see what uses it: ss -ltnp 'sport = :%d'", p), "%s: port %d is taken by another program", s.Name, p)
			} else {
				ln.Close()
			}
		}
	}
	r.ok("%d server(s) checked", len(list))
	fmt.Printf("note  RCON ports (%s) listen on all interfaces: never port-forward or tunnel them\n", strings.Join(rcon, ", "))
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Package metrics samples TPS, MSPT, memory and players from running servers
// and broadcasts them on the event bus.
package metrics

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcsman/internal/events"
	"mcsman/internal/server"
)

const DefaultInterval = 10 * time.Second

// Collector polls running servers on an interval.
type Collector struct {
	Servers  *server.Manager
	Bus      *events.Bus
	Log      *slog.Logger
	Interval time.Duration
}

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	interval := c.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.SampleAll()
		}
	}
}

// SampleAll samples every running server concurrently.
func (c *Collector) SampleAll() {
	var wg sync.WaitGroup
	for _, inst := range c.Servers.Running() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, players, err := Sample(inst)
			if err != nil {
				c.Log.Debug("metrics sample failed", "server", inst.Server().Name, "err", err)
				return
			}
			inst.SetMetrics(m, players)
			c.Bus.Publish(events.New(events.Metrics, inst.Server().ID, m))
		}()
	}
	wg.Wait()
}

// Sample collects one metrics sample from inst.
func Sample(inst *server.Instance) (*server.Metrics, []string, error) {
	srv := inst.Server()
	m := &server.Metrics{RAMMaxMB: srv.MaxMemoryMB}

	out, err := inst.Exec("list")
	if err != nil {
		return nil, nil, err
	}
	count, players, ok := ParseList(out)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected list output %q", out)
	}
	m.PlayerCount = count

	if out, err := inst.Exec("tps"); err == nil {
		m.TPS = ParseTPS(out)
	}
	if out, err := inst.Exec("mspt"); err == nil {
		m.MSPT = ParseMSPT(out)
	}
	if pid := inst.PID(); pid != 0 {
		m.RAMUsedMB = rssMB(pid)
	}
	return m, players, nil
}

var (
	colorRe = regexp.MustCompile(`§.`)
	floatRe = regexp.MustCompile(`\d+(?:\.\d+)?`)
	listRe  = regexp.MustCompile(`There are (\d+) (?:of a max of|out of maximum) \d+ players online:?\s*(.*)`)
)

func stripColors(s string) string { return colorRe.ReplaceAllString(s, "") }

// ParseList parses the output of "list".
func ParseList(out string) (int, []string, bool) {
	m := listRe.FindStringSubmatch(stripColors(out))
	if m == nil {
		return 0, nil, false
	}
	n, _ := strconv.Atoi(m[1])
	players := []string{}
	for _, p := range strings.Split(m[2], ",") {
		if p = strings.TrimSpace(p); p != "" {
			players = append(players, p)
		}
	}
	return n, players, true
}

// ParseTPS returns the 1-minute TPS from Paper's "tps" output.
func ParseTPS(out string) float64 {
	s := stripColors(out)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	if f := floatRe.FindString(s); f != "" {
		v, _ := strconv.ParseFloat(f, 64)
		return v
	}
	return 0
}

// ParseMSPT returns the average tick time over the shortest window from
// Paper's "mspt" output.
func ParseMSPT(out string) float64 {
	s := stripColors(out)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	if f := floatRe.FindString(s); f != "" {
		v, _ := strconv.ParseFloat(f, 64)
		return v
	}
	return 0
}

// rssMB reads a process's resident memory from /proc.
func rssMB(pid int) int {
	f, err := os.Open(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "VmRSS:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			return kb / 1024
		}
	}
	return 0
}

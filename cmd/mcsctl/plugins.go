package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

type pluginInfo struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Version  string  `json:"version"`
	Source   string  `json:"source"`
	SourceID *string `json:"source_id"`
	Filename string  `json:"filename"`
}

func dispatchPlugin(c *client, sub string, args []string) error {
	switch sub {
	case "search":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New("missing search query")
		}
		query := args[0]
		fs := flag.NewFlagSet("search", flag.ExitOnError)
		mc := fs.String("mc-version", "", "only plugins supporting this Minecraft version")
		source := fs.String("source", "", "modrinth or hangar")
		fs.Parse(args[1:])
		q := url.Values{"q": {query}, "mc_version": {*mc}, "source": {*source}}
		var out struct {
			Results []struct {
				Source, ID, Name, Author, Description string
				Downloads                             int64
			} `json:"results"`
			Errors map[string]string `json:"errors"`
		}
		if err := c.do(http.MethodGet, "/api/plugins/search?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(out)
		}
		for src := range out.Errors {
			fmt.Fprintf(os.Stderr, "warning: %s search unavailable\n", src)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SOURCE\tID\tNAME\tAUTHOR\tDOWNLOADS")
		for _, r := range out.Results {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", r.Source, r.ID, r.Name, r.Author, r.Downloads)
		}
		return tw.Flush()

	case "install":
		return withName(args, func(n string, rest []string) error {
			if len(rest) == 0 {
				return errors.New("missing plugin id (modrinth project id/slug or hangar owner/slug)")
			}
			id := rest[0]
			fs := flag.NewFlagSet("install", flag.ExitOnError)
			version := fs.String("version", "", "specific version id")
			fs.Parse(rest[1:])
			source := "modrinth"
			if strings.Contains(id, "/") {
				source = "hangar"
			}
			var p pluginInfo
			if err := c.do(http.MethodPost, serverPath(n, "/plugins"),
				map[string]string{"source": source, "source_id": id, "version": *version}, &p); err != nil {
				return err
			}
			fmt.Printf("Installed %s %s (%s). Restart %s to load it.\n", p.Name, p.Version, p.Filename, n)
			return nil
		})

	case "list":
		return withName(args, func(n string, _ []string) error {
			var list []pluginInfo
			if err := c.do(http.MethodGet, serverPath(n, "/plugins"), nil, &list); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(list)
			}
			if len(list) == 0 {
				fmt.Println("No plugins.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tVERSION\tSOURCE\tFILE")
			for _, p := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Version, p.Source, p.Filename)
			}
			return tw.Flush()
		})

	case "update":
		return withName(args, func(n string, rest []string) error {
			var ids []string
			if len(rest) == 1 && rest[0] != "--all" {
				ids = rest
			} else if len(rest) == 1 && rest[0] == "--all" {
				var ups []struct {
					PluginID string `json:"plugin_id"`
				}
				if err := c.do(http.MethodGet, serverPath(n, "/plugins/updates"), nil, &ups); err != nil {
					return err
				}
				for _, u := range ups {
					ids = append(ids, u.PluginID)
				}
				if len(ids) == 0 {
					fmt.Println("All plugins are up to date.")
					return nil
				}
			} else {
				return errors.New("usage: mcsctl plugin update <server> <plugin-id>|--all")
			}
			for _, id := range ids {
				var out struct {
					Plugin  pluginInfo `json:"plugin"`
					Updated bool       `json:"updated"`
				}
				if err := c.do(http.MethodPost, serverPath(n, "/plugins/"+url.PathEscape(id)+"/update"), nil, &out); err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
					continue
				}
				if out.Updated {
					fmt.Printf("Updated %s to %s\n", out.Plugin.Name, out.Plugin.Version)
				} else {
					fmt.Printf("%s is up to date (%s)\n", out.Plugin.Name, out.Plugin.Version)
				}
			}
			return nil
		})

	case "remove":
		return withName(args, func(n string, rest []string) error {
			if len(rest) == 0 {
				return errors.New("missing plugin id")
			}
			if err := c.do(http.MethodDelete, serverPath(n, "/plugins/"+url.PathEscape(rest[0])), nil, nil); err != nil {
				return err
			}
			fmt.Println("Removed. Its config folder was kept.")
			return nil
		})
	}
	return fmt.Errorf("unknown plugin subcommand %q", sub)
}

type preflight struct {
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	TargetBuild    int    `json:"target_build"`
	CurrentJava    int    `json:"current_java"`
	RequiredJava   int    `json:"required_java"`
	JavaChange     bool   `json:"java_change"`
	Plugins        []struct {
		Plugin  pluginInfo `json:"plugin"`
		Status  string     `json:"status"`
		Release *struct {
			Version string `json:"version"`
		} `json:"release"`
	} `json:"plugins"`
	Warnings   []string `json:"warnings"`
	Blockers   []string `json:"blockers"`
	CanUpgrade bool     `json:"can_upgrade"`
}

func printPreflight(p preflight) {
	fmt.Printf("Paper %s → %s (build %d)\n", p.CurrentVersion, p.TargetVersion, p.TargetBuild)
	if p.JavaChange {
		fmt.Printf("Java %d → %d\n", p.CurrentJava, p.RequiredJava)
	}
	if len(p.Plugins) > 0 {
		fmt.Println("\nPlugins:")
		icon := map[string]string{"green": "\x1b[32m●\x1b[0m", "amber": "\x1b[33m●\x1b[0m", "gray": "\x1b[90m●\x1b[0m"}
		for _, c := range p.Plugins {
			note := map[string]string{"amber": "no release for target", "gray": "manual, not checked"}[c.Status]
			if c.Status == "green" && c.Release != nil {
				note = c.Plugin.Version + " → " + c.Release.Version
				if c.Release.Version == c.Plugin.Version {
					note = "compatible"
				}
			}
			fmt.Printf("  %s %-24s %s\n", icon[c.Status], c.Plugin.Name, note)
		}
	}
	for _, w := range p.Warnings {
		fmt.Printf("\x1b[33mwarning:\x1b[0m %s\n", w)
	}
	for _, b := range p.Blockers {
		fmt.Printf("\x1b[31mblocked:\x1b[0m %s\n", b)
	}
}

func dispatchUpgrade(c *client, sub string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: mcsctl upgrade %s <server> <target-version>", sub)
	}
	name, target := args[0], args[1]
	var p preflight
	if err := c.do(http.MethodPost, serverPath(name, "/upgrade"), map[string]string{"target_version": target}, &p); err != nil {
		return err
	}
	switch sub {
	case "preflight":
		if jsonOut {
			return printJSON(p)
		}
		printPreflight(p)
		return nil
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		delay := fs.Int("delay", -1, "in-game countdown seconds (default: 60 if players online)")
		fs.Parse(args[2:])
		printPreflight(p)
		if !p.CanUpgrade {
			return errors.New("upgrade blocked")
		}
		if !confirm("\nProceed? The server will be stopped and backed up first. [type yes]: ", "yes") {
			return errors.New("aborted")
		}
		body := map[string]any{"target_version": target}
		if *delay >= 0 {
			body["delay_seconds"] = *delay
		}
		if err := c.do(http.MethodPost, serverPath(name, "/upgrade/confirm"), body, nil); err != nil {
			return err
		}
		return followUpgrade(c, name)
	}
	return fmt.Errorf("unknown upgrade subcommand %q", sub)
}

func followUpgrade(c *client, name string) error {
	seen := 0
	for {
		var out struct {
			Job *struct {
				State          string   `json:"state"`
				Error          string   `json:"error"`
				PluginsUpdated []string `json:"plugins_updated"`
				PluginErrors   []string `json:"plugin_errors"`
				BackupID       string   `json:"backup_id"`
				Steps          []struct {
					Phase, Detail string
				} `json:"steps"`
			} `json:"job"`
		}
		if err := c.do(http.MethodGet, serverPath(name, "/upgrade/status"), nil, &out); err != nil {
			return err
		}
		j := out.Job
		if j == nil {
			return errors.New("no upgrade job found")
		}
		for ; seen < len(j.Steps); seen++ {
			fmt.Printf("  [%s] %s\n", j.Steps[seen].Phase, j.Steps[seen].Detail)
		}
		if j.State != "running" {
			for _, u := range j.PluginsUpdated {
				fmt.Println("  updated plugin:", u)
			}
			for _, e := range j.PluginErrors {
				fmt.Println("  \x1b[33mplugin error:\x1b[0m", e)
			}
			if j.State == "failed" {
				if j.BackupID != "" {
					fmt.Printf("Restore the pre-upgrade backup with: mcsctl backup restore %s %s\n", name, j.BackupID)
				}
				return errors.New(j.Error)
			}
			return nil
		}
		time.Sleep(time.Second)
	}
}

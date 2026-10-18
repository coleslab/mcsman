package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"
)

type backupInfo struct {
	ID        string  `json:"id"`
	Filename  string  `json:"filename"`
	SizeBytes int64   `json:"size_bytes"`
	SHA256    string  `json:"sha256"`
	Tag       *string `json:"tag"`
	CreatedAt int64   `json:"created_at"`
}

func dispatchBackup(c *client, sub string, args []string) error {
	switch sub {
	case "create":
		return withName(args, func(n string, rest []string) error {
			fs := flag.NewFlagSet("create", flag.ExitOnError)
			tag := fs.String("tag", "manual", "backup tag")
			fs.Parse(rest)
			fmt.Fprintf(os.Stderr, "Backing up %s…\n", n)
			var b backupInfo
			if err := c.do(http.MethodPost, serverPath(n, "/backups?wait=true"), map[string]string{"tag": *tag}, &b); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(b)
			}
			fmt.Printf("Created %s (%s)\nID: %s\n", b.Filename, humanBytes(b.SizeBytes), b.ID)
			return nil
		})
	case "list":
		return withName(args, func(n string, _ []string) error {
			var list []backupInfo
			if err := c.do(http.MethodGet, serverPath(n, "/backups"), nil, &list); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(list)
			}
			if len(list) == 0 {
				fmt.Println("No backups.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tCREATED\tTAG\tSIZE")
			for _, b := range list {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", b.ID, time.Unix(b.CreatedAt, 0).Format("2006-01-02 15:04"), deref(b.Tag), humanBytes(b.SizeBytes))
			}
			return tw.Flush()
		})
	case "restore":
		return withName(args, func(n string, rest []string) error {
			if len(rest) == 0 {
				return errors.New("missing backup id")
			}
			id := rest[0]
			if !confirm(fmt.Sprintf("Replace %s's world with backup %s? (current world is backed up first) [type yes]: ", n, id), "yes") {
				return errors.New("aborted")
			}
			if err := c.do(http.MethodPost, serverPath(n, "/backups/"+id+"/restore"), nil, nil); err != nil {
				return err
			}
			fmt.Println("Restored.")
			return nil
		})
	}
	return fmt.Errorf("unknown backup subcommand %q", sub)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"
)

func dispatchInvite(c *client, sub string, args []string) error {
	switch sub {
	case "create":
		fs := flag.NewFlagSet("invite create", flag.ExitOnError)
		expires := fs.String("expires", "", "validity, e.g. 48h or 7d (default from config)")
		server := fs.String("whitelist", "", "server to whitelist the new player on")
		fs.Parse(args)
		body := map[string]any{"expires_in": *expires}
		if *server != "" {
			body["whitelist_server_id"] = *server
		}
		var out struct {
			ID         string  `json:"id"`
			URL        string  `json:"url"`
			ExpiresAt  int64   `json:"expires_at"`
			ServerName *string `json:"whitelist_server_name"`
		}
		if err := c.do(http.MethodPost, "/api/invites", body, &out); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(out)
		}
		fmt.Println(out.URL)
		fmt.Fprintf(os.Stderr, "Expires %s", time.Unix(out.ExpiresAt, 0).Format("2006-01-02 15:04"))
		if out.ServerName != nil {
			fmt.Fprintf(os.Stderr, "; whitelists on %s", *out.ServerName)
		}
		fmt.Fprintln(os.Stderr, ". Share this link once; it works for one person.")
		return nil
	case "list":
		var list []struct {
			ID         string  `json:"id"`
			CreatedAt  int64   `json:"created_at"`
			ExpiresAt  int64   `json:"expires_at"`
			ServerName *string `json:"whitelist_server_name"`
		}
		if err := c.do(http.MethodGet, "/api/invites", nil, &list); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(list)
		}
		if len(list) == 0 {
			fmt.Println("No pending invites.")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tCREATED\tEXPIRES\tWHITELIST")
		for _, i := range list {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", i.ID, time.Unix(i.CreatedAt, 0).Format("2006-01-02 15:04"),
				time.Unix(i.ExpiresAt, 0).Format("2006-01-02 15:04"), deref(i.ServerName))
		}
		return tw.Flush()
	case "revoke":
		if len(args) == 0 {
			return errors.New("missing invite id")
		}
		if err := c.do(http.MethodDelete, "/api/invites/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Println("Revoked.")
		return nil
	}
	return fmt.Errorf("unknown invite subcommand %q", sub)
}

func dispatchUser(c *client, sub string, args []string) error {
	switch sub {
	case "list":
		var list []struct {
			Username    string  `json:"username"`
			MCUsername  *string `json:"mc_username"`
			Role        string  `json:"role"`
			CreatedAt   int64   `json:"created_at"`
			LastLoginAt *int64  `json:"last_login_at"`
		}
		if err := c.do(http.MethodGet, "/api/users", nil, &list); err != nil {
			return err
		}
		if jsonOut {
			return printJSON(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "USERNAME\tROLE\tMINECRAFT\tLAST LOGIN")
		for _, u := range list {
			last := "never"
			if u.LastLoginAt != nil {
				last = ago(*u.LastLoginAt) + " ago"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Username, u.Role, deref(u.MCUsername), last)
		}
		return tw.Flush()
	case "role":
		if len(args) < 2 || (args[1] != "admin" && args[1] != "player") {
			return errors.New("usage: mcsctl user role <username> admin|player")
		}
		if err := c.do(http.MethodPatch, "/api/users/"+url.PathEscape(args[0]), map[string]string{"role": args[1]}, nil); err != nil {
			return err
		}
		fmt.Printf("%s is now %s.\n", args[0], args[1])
		return nil
	case "remove":
		if len(args) == 0 {
			return errors.New("missing username")
		}
		if !confirm(fmt.Sprintf("Remove user %q? Type the username to confirm: ", args[0]), args[0]) {
			return errors.New("aborted")
		}
		if err := c.do(http.MethodDelete, "/api/users/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Println("Removed", args[0])
		return nil
	}
	return fmt.Errorf("unknown user subcommand %q", sub)
}

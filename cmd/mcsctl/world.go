package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func dispatchWorld(c *client, sub string, args []string) error {
	switch sub {
	case "import":
		return withName(args, func(n string, rest []string) error {
			if len(rest) == 0 {
				return errors.New("missing path to world zip")
			}
			path := rest[0]
			fs := flag.NewFlagSet("import", flag.ExitOnError)
			label := fs.String("name", "", "label for import history (default: world name)")
			force := fs.Bool("force", false, "import even if the world is newer than the server")
			fs.Parse(rest[1:])
			if !confirm(fmt.Sprintf("Replace %s's world with %s? (the current world is backed up first) [type yes]: ", n, filepath.Base(path)), "yes") {
				return errors.New("aborted")
			}
			return uploadWorld(c, n, path, *label, *force)
		})
	case "export":
		return withName(args, func(n string, rest []string) error {
			fs := flag.NewFlagSet("export", flag.ExitOnError)
			out := fs.String("output", fmt.Sprintf("%s-world-%s.zip", n, time.Now().Format("2006-01-02")), "output file")
			fs.Parse(rest)
			return downloadTo(c, serverPath(n, "/worlds/export"), *out)
		})
	}
	return fmt.Errorf("unknown world subcommand %q", sub)
}

// uploadWorld streams the zip as multipart without buffering it in memory.
func uploadWorld(c *client, name, path, label string, force bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if label != "" {
				if err := mw.WriteField("source_name", label); err != nil {
					return err
				}
			}
			if force {
				if err := mw.WriteField("force", "true"); err != nil {
					return err
				}
			}
			fw, err := mw.CreateFormFile("world", filepath.Base(path))
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, f); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	req, _ := http.NewRequest(http.MethodPost, "http://mcsman"+serverPath(name, "/worlds/import"), pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	fmt.Fprintln(os.Stderr, "Uploading and importing…")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Error           string `json:"error"`
		SourceName      string `json:"source_name"`
		SourceMCVersion string `json:"source_mc_version"`
		Inspection      *struct {
			VersionName       string `json:"version_name"`
			DataVersion       int32  `json:"data_version"`
			ServerDataVersion int32  `json:"server_data_version"`
		} `json:"inspection"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 400 {
		if out.Inspection != nil {
			return fmt.Errorf("%s (world DataVersion %d, server %d); rerun with --force to import anyway",
				out.Error, out.Inspection.DataVersion, out.Inspection.ServerDataVersion)
		}
		return &apiError{Status: resp.StatusCode, Msg: out.Error}
	}
	fmt.Printf("Imported %q (Minecraft %s). Start the server to play it.\n", out.SourceName, out.SourceMCVersion)
	return nil
}

func downloadTo(c *client, path, out string) error {
	resp, err := c.http.Get("http://mcsman" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return &apiError{Status: resp.StatusCode, Msg: e.Error}
	}
	tmp := out + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, out); err != nil {
		return err
	}
	fmt.Printf("Saved %s (%s)\n", out, humanBytes(n))
	return nil
}

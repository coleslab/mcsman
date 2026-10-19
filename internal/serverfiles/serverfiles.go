// Package serverfiles reads and edits a server's text config files: Paper's
// YAML, plugin configs and server.properties. Only allowlisted paths are
// reachable, writes check for concurrent changes and syntax, and the
// server.properties keys mcsman manages can't be changed or read back.
package serverfiles

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"mcsman/internal/fsutil"
)

// MaxSize caps editable files; plugin configs are far smaller.
const MaxSize = 1 << 20

var (
	ErrNotEditable = errors.New("this file can't be edited here")
	ErrTooLarge    = errors.New("file is larger than 1 MiB")
	ErrNotText     = errors.New("file is not valid UTF-8 text")
	ErrConflict    = errors.New("the file changed on disk since it was loaded")
)

// SyntaxError reports where a YAML or JSON file fails to parse.
type SyntaxError struct {
	Line int    `json:"line"`
	Msg  string `json:"message"`
}

func (e *SyntaxError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("syntax error on line %d: %s", e.Line, e.Msg)
	}
	return "syntax error: " + e.Msg
}

// rootFiles are the editable files outside plugins/ and world folders.
var rootFiles = []string{
	"server.properties",
	"bukkit.yml", "spigot.yml", "commands.yml", "permissions.yml", "help.yml",
	"config/paper-global.yml", "config/paper-world-defaults.yml",
}

var pluginExts = map[string]bool{
	".yml": true, ".yaml": true, ".json": true, ".properties": true,
	".toml": true, ".conf": true, ".txt": true,
}

// maxPluginDepth limits plugins/<plugin>/a/b/c/file.yml.
const maxPluginDepth = 6

// managedKeys are server.properties keys mcsman writes on every start; edits
// to them are ignored. rcon.password is also masked when read.
var managedKeys = map[string]bool{
	"server-port": true, "enable-rcon": true, "rcon.port": true,
	"rcon.password": true, "broadcast-rcon-to-ops": true,
}

const passwordMask = "********"

// File is an editable file.
type File struct {
	Path    string `json:"path"`
	Group   string `json:"group"` // "Server" or the plugin's folder name
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

// Allowed reports whether rel (slash-separated, relative to the server
// directory) is on the allowlist. It doesn't touch the filesystem.
func Allowed(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || path.Clean(rel) != rel {
		return false
	}
	for _, f := range rootFiles {
		if rel == f {
			return true
		}
	}
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == ".." || p == "." || strings.HasPrefix(p, ".") {
			return false
		}
	}
	if parts[0] == "plugins" {
		return len(parts) >= 3 && len(parts) <= maxPluginDepth && pluginExts[strings.ToLower(path.Ext(rel))]
	}
	// <world>/paper-world.yml
	return len(parts) == 2 && parts[1] == "paper-world.yml"
}

// resolve checks rel against the allowlist and returns its absolute path if
// it is a regular file (not a symlink) of editable size.
func resolve(dir, rel string) (string, os.FileInfo, error) {
	if !Allowed(rel) {
		return "", nil, ErrNotEditable
	}
	p, err := fsutil.SafePath(dir, filepath.FromSlash(rel))
	if err != nil {
		return "", nil, ErrNotEditable
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "", nil, err
	}
	if !fi.Mode().IsRegular() {
		return "", nil, ErrNotEditable
	}
	if fi.Size() > MaxSize {
		return "", nil, ErrTooLarge
	}
	return p, fi, nil
}

// List returns the editable files under dir, server files first, then
// plugin files grouped by plugin folder.
func List(dir string) ([]File, error) {
	out := []File{}
	add := func(rel, group string) {
		if _, fi, err := resolve(dir, rel); err == nil {
			out = append(out, File{Path: rel, Group: group, Size: fi.Size(), ModTime: fi.ModTime().Unix()})
		}
	}
	for _, f := range rootFiles {
		add(f, "Server")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "plugins" && !strings.HasPrefix(e.Name(), ".") {
			add(e.Name()+"/paper-world.yml", "Server")
		}
	}

	plugins := filepath.Join(dir, "plugins")
	var pluginFiles []File
	err = filepath.WalkDir(plugins, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == plugins && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return nil // unreadable entry: skip it
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		if d.IsDir() {
			if p != plugins && (strings.HasPrefix(d.Name(), ".") || depth >= maxPluginDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && depth >= 3 {
			if _, fi, err := resolve(dir, rel); err == nil {
				group := strings.Split(rel, "/")[1]
				pluginFiles = append(pluginFiles, File{Path: rel, Group: group, Size: fi.Size(), ModTime: fi.ModTime().Unix()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pluginFiles, func(i, j int) bool {
		a, b := pluginFiles[i], pluginFiles[j]
		if !strings.EqualFold(a.Group, b.Group) {
			return strings.ToLower(a.Group) < strings.ToLower(b.Group)
		}
		return a.Path < b.Path
	})
	return append(out, pluginFiles...), nil
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Read returns a file's content for editing (secrets masked) and the
// SHA-256 of what is on disk, to pass back to Write.
func Read(dir, rel string) ([]byte, string, error) {
	p, _, err := resolve(dir, rel)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	if !utf8.Valid(raw) {
		return nil, "", ErrNotText
	}
	return Mask(rel, raw), digest(raw), nil
}

// Mask hides secrets in a file's content: the RCON password in
// server.properties. Other files are returned unchanged.
func Mask(rel string, content []byte) []byte {
	if rel != "server.properties" {
		return content
	}
	var out bytes.Buffer
	eachLine(content, func(line, key string) {
		if key == "rcon.password" {
			if _, v, _ := cutProperty(line); v != "" {
				line = line[:strings.Index(line, "=")+1] + passwordMask
			}
		}
		out.WriteString(line + "\n")
	})
	return out.Bytes()
}

// Write replaces a file's content. baseSHA must match the file on disk
// (from Read) or ErrConflict is returned. YAML and JSON must parse unless
// force is set. It returns the previous content (masked, for revision
// history) and the new on-disk SHA-256.
func Write(dir, rel string, content []byte, baseSHA string, force bool) (prev []byte, sha string, err error) {
	p, fi, err := resolve(dir, rel)
	if err != nil {
		return nil, "", err
	}
	if len(content) > MaxSize {
		return nil, "", ErrTooLarge
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, "", ErrNotText
	}
	current, err := os.ReadFile(p)
	if err != nil {
		return nil, "", err
	}
	if digest(current) != baseSHA {
		return nil, "", ErrConflict
	}
	if rel == "server.properties" {
		content = protectManaged(current, content)
	}
	if !force {
		if err := Check(rel, content); err != nil {
			return nil, "", err
		}
	}
	if err := fsutil.WriteFileAtomic(p, content, fi.Mode().Perm()); err != nil {
		return nil, "", err
	}
	return Mask(rel, current), digest(content), nil
}

var yamlLineRe = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// Check validates YAML and JSON syntax; other formats pass.
func Check(rel string, content []byte) error {
	switch strings.ToLower(path.Ext(rel)) {
	case ".yml", ".yaml":
		var n yaml.Node
		if err := yaml.Unmarshal(content, &n); err != nil {
			msg := err.Error()
			if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
				line, _ := strconv.Atoi(m[1])
				return &SyntaxError{Line: line, Msg: m[2]}
			}
			return &SyntaxError{Msg: strings.TrimPrefix(msg, "yaml: ")}
		}
	case ".json":
		var v any
		if err := json.Unmarshal(content, &v); err != nil {
			var se *json.SyntaxError
			if errors.As(err, &se) {
				return &SyntaxError{Line: bytes.Count(content[:se.Offset], []byte("\n")) + 1, Msg: se.Error()}
			}
			return &SyntaxError{Msg: err.Error()}
		}
	}
	return nil
}

// protectManaged keeps the on-disk lines for mcsman-managed keys, whatever
// the edit did to them (including the masked password).
func protectManaged(current, edited []byte) []byte {
	keep := map[string]string{}
	var order []string
	eachLine(current, func(line, key string) {
		if managedKeys[key] {
			if _, ok := keep[key]; !ok {
				order = append(order, key)
			}
			keep[key] = line
		}
	})
	var out bytes.Buffer
	seen := map[string]bool{}
	eachLine(edited, func(line, key string) {
		if managedKeys[key] {
			if l, ok := keep[key]; ok && !seen[key] {
				out.WriteString(l + "\n")
			}
			seen[key] = true
			return
		}
		out.WriteString(line + "\n")
	})
	for _, k := range order {
		if !seen[k] {
			out.WriteString(keep[k] + "\n")
		}
	}
	return out.Bytes()
}

// eachLine calls fn for every line of a properties file with its key ("" for
// blanks and comments).
func eachLine(content []byte, fn func(line, key string)) {
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 64*1024), MaxSize)
	for sc.Scan() {
		line := sc.Text()
		k, _, _ := cutProperty(line)
		fn(line, k)
	}
}

func cutProperty(line string) (key, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") {
		return "", "", false
	}
	k, v, ok := strings.Cut(t, "=")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

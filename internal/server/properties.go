package server

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcsman/internal/fsutil"
)

// writeProperties merges set into dir/server.properties, preserving any other
// keys and comments, and writes it atomically with 0640 permissions.
func writeProperties(dir string, set map[string]string) error {
	path := filepath.Join(dir, "server.properties")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	pending := make(map[string]string, len(set))
	for k, v := range set {
		pending[k] = v
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(existing))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "!") {
			if k, _, ok := strings.Cut(trimmed, "="); ok {
				k = strings.TrimSpace(k)
				if v, ok := pending[k]; ok {
					out.WriteString(k + "=" + v + "\n")
					delete(pending, k)
					continue
				}
			}
		}
		out.WriteString(line + "\n")
	}
	keys := make([]string, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.WriteString(k + "=" + pending[k] + "\n")
	}
	return fsutil.WriteFileAtomic(path, out.Bytes(), 0o640)
}

// readProperties parses dir/server.properties into a map.
func readProperties(dir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "!") {
			continue
		}
		if k, v, ok := strings.Cut(t, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m, nil
}

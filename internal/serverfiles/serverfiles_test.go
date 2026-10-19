package serverfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"server.properties":                "#Minecraft server properties\nmotd=Hello\nserver-port=25565\nrcon.password=s3cret-rcon\nenable-rcon=true\n",
		"bukkit.yml":                       "settings:\n  allow-end: true\n",
		"config/paper-global.yml":          "proxies: {}\n",
		"world/paper-world.yml":            "a: 1\n",
		"plugins/Essentials/config.yml":    "ops-name-color: '4'\n",
		"plugins/Essentials/kits/x.json":   "{}\n",
		"plugins/Essentials.jar":           "PK",
		"plugins/Foo/a/b/c/d/too-deep.yml": "x: 1\n",
		"plugins/.hidden/config.yml":       "x: 1\n",
		"ops.json":                         "[]",
		"eula.txt":                         "eula=true\n",
	}
	for rel, c := range files {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(c), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAllowed(t *testing.T) {
	for rel, want := range map[string]bool{
		"server.properties":              true,
		"config/paper-global.yml":        true,
		"world/paper-world.yml":          true,
		"plugins/Essentials/config.yml":  true,
		"plugins/Essentials/kits/x.json": true,
		"plugins/Essentials.jar":         false,
		"plugins/config.yml":             false,
		"plugins/X/run.sh":               false,
		"plugins/.hidden/config.yml":     false,
		"plugins/X/../../eula.txt":       false,
		"../outside/plugins/x/c.yml":     false,
		"/etc/passwd":                    false,
		"ops.json":                       false,
		"whitelist.json":                 false,
		"eula.txt":                       false,
		"server.jar":                     false,
		"world/level.dat":                false,
		"plugins\\X\\c.yml":              false,
		"":                               false,
	} {
		if got := Allowed(rel); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", rel, got, want)
		}
	}
}

func TestList(t *testing.T) {
	dir := setup(t)
	files, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Group+":"+f.Path)
	}
	want := []string{
		"Server:server.properties", "Server:bukkit.yml", "Server:config/paper-global.yml",
		"Server:world/paper-world.yml",
		"Essentials:plugins/Essentials/config.yml", "Essentials:plugins/Essentials/kits/x.json",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List:\n got %v\nwant %v", got, want)
	}
}

func TestSymlinksRejected(t *testing.T) {
	dir := setup(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.yml"), []byte("k: v\n"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "plugins", "Evil"))
	os.Symlink(filepath.Join(outside, "secret.yml"), filepath.Join(dir, "plugins", "Essentials", "link.yml"))
	for _, rel := range []string{"plugins/Evil/secret.yml", "plugins/Essentials/link.yml"} {
		if _, _, err := Read(dir, rel); !errors.Is(err, ErrNotEditable) {
			t.Errorf("Read(%s): %v, want ErrNotEditable", rel, err)
		}
	}
	files, _ := List(dir)
	for _, f := range files {
		if strings.Contains(f.Path, "Evil") || strings.Contains(f.Path, "link") {
			t.Errorf("listed symlink %s", f.Path)
		}
	}
}

func TestReadWriteConflictAndSyntax(t *testing.T) {
	dir := setup(t)
	rel := "plugins/Essentials/config.yml"
	content, sha, err := Read(dir, rel)
	if err != nil || string(content) != "ops-name-color: '4'\n" {
		t.Fatalf("read: %q %v", content, err)
	}
	if _, _, err := Write(dir, rel, []byte("a: [1\n"), sha, false); err == nil {
		t.Fatal("invalid YAML accepted")
	} else if se := (*SyntaxError)(nil); !errors.As(err, &se) || se.Line == 0 {
		t.Errorf("want SyntaxError with line, got %v", err)
	}
	if _, _, err := Write(dir, rel, []byte("a: 1\n"), "stale", false); !errors.Is(err, ErrConflict) {
		t.Errorf("stale sha: %v", err)
	}
	prev, sha2, err := Write(dir, rel, []byte("a: 1\n"), sha, false)
	if err != nil || string(prev) != string(content) || sha2 == sha {
		t.Fatalf("write: %q %v", prev, err)
	}
	// force saves an unparseable file.
	if _, _, err := Write(dir, rel, []byte("a: [1\n"), sha2, true); err != nil {
		t.Errorf("forced write: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, rel)); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	if _, _, err := Write(dir, "plugins/Essentials/kits/x.json", []byte("{,}"), "", false); !errors.Is(err, ErrConflict) {
		t.Errorf("json with wrong sha: %v", err)
	}
	_, jsha, _ := Read(dir, "plugins/Essentials/kits/x.json")
	if _, _, err := Write(dir, "plugins/Essentials/kits/x.json", []byte("{\n,}"), jsha, false); err == nil {
		t.Error("invalid JSON accepted")
	}
	if _, _, err := Write(dir, "plugins/Essentials/new.yml", []byte("a: 1\n"), "", false); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("creating a file: %v", err)
	}
	if _, _, err := Write(dir, rel, []byte("a\x00"), sha2, true); !errors.Is(err, ErrNotText) {
		t.Errorf("NUL byte: %v", err)
	}
}

func TestTooLarge(t *testing.T) {
	dir := setup(t)
	os.WriteFile(filepath.Join(dir, "bukkit.yml"), make([]byte, MaxSize+1), 0o640)
	if _, _, err := Read(dir, "bukkit.yml"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("read big file: %v", err)
	}
}

func TestServerPropertiesManagedKeys(t *testing.T) {
	dir := setup(t)
	content, sha, err := Read(dir, "server.properties")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "s3cret") || !strings.Contains(string(content), "rcon.password="+passwordMask) {
		t.Fatalf("password not masked:\n%s", content)
	}
	// Edit motd, try to change the port, keep the masked password and drop enable-rcon.
	edited := strings.Replace(string(content), "motd=Hello", "motd=Welcome", 1)
	edited = strings.Replace(edited, "server-port=25565", "server-port=1", 1)
	edited = strings.Replace(edited, "enable-rcon=true\n", "", 1)
	prev, _, err := Write(dir, "server.properties", []byte(edited), sha, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prev), "s3cret") {
		t.Error("revision content exposes the RCON password")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "server.properties"))
	for _, want := range []string{"motd=Welcome", "server-port=25565", "rcon.password=s3cret-rcon", "enable-rcon=true", "#Minecraft server properties"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %q in:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), passwordMask) || strings.Contains(string(raw), "server-port=1\n") {
		t.Errorf("managed keys overwritten:\n%s", raw)
	}
}

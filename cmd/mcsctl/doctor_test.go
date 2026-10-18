package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcsman.env")
	os.WriteFile(p, []byte("# comment\nMCSMAN_JWT_SECRET=\n\nA = \"quoted\"\nMCSMAN_JWT_SECRET=later-wins\n#B=x\n"), 0o600)
	env, err := readEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["MCSMAN_JWT_SECRET"] != "later-wins" || env["A"] != "quoted" || env["B"] != "" {
		t.Errorf("env: %v", env)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secret = "0123456789abcdef0123456789abcdef"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseDefaultsAndEnv(t *testing.T) {
	raw := []byte(`
discord:
  webhooks:
    - name: a
      events: {server_crash: true}
auth:
  refresh_token_ttl: 3d
`)
	cfg, err := parse(raw, env(map[string]string{
		"MCSMAN_JWT_SECRET":        secret,
		"MCSMAN_DISCORD_WEBHOOK_0": "https://discord.example/x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.JWTSecret != secret {
		t.Error("jwt secret not overlaid from env")
	}
	if cfg.Discord.Webhooks[0].URL != "https://discord.example/x" {
		t.Error("webhook URL not overlaid from env")
	}
	if cfg.Auth.RefreshTokenTTL.D() != 72*time.Hour {
		t.Errorf("refresh ttl = %v", cfg.Auth.RefreshTokenTTL.D())
	}
	if cfg.Auth.AccessTokenTTL.D() != 15*time.Minute {
		t.Errorf("access ttl default = %v", cfg.Auth.AccessTokenTTL.D())
	}
	if cfg.Java.CacheDir != "/var/lib/mcsman/java" {
		t.Errorf("java cache dir = %q", cfg.Java.CacheDir)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"short secret":  "auth: {jwt_secret: short}",
		"bad log level": "auth: {jwt_secret: " + secret + "}\ndaemon: {log_level: loud}",
		"half tls":      "auth: {jwt_secret: " + secret + "}\nhttp: {tls: {cert: /x}}",
		"bad proxy":     "auth: {jwt_secret: " + secret + "}\nhttp: {trusted_proxies: [nope]}",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(raw), env(nil)); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestLoadRequires0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcsman.yaml")
	if err := os.WriteFile(p, []byte("auth: {jwt_secret: "+secret+"}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("want permission error, got %v", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

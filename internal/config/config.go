// Package config loads and validates the mcsman YAML configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration to accept strings like "15m" and "7d" in YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// ParseDuration is time.ParseDuration with added support for a "d" (day) suffix.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

type Config struct {
	Daemon  Daemon  `yaml:"daemon"`
	HTTP    HTTP    `yaml:"http"`
	Auth    Auth    `yaml:"auth"`
	Backup  Backup  `yaml:"backup"`
	Discord Discord `yaml:"discord"`
	Java    Java    `yaml:"java"`
}

type Daemon struct {
	Socket   string `yaml:"socket"`
	DataDir  string `yaml:"data_dir"`
	LogLevel string `yaml:"log_level"`
}

type HTTP struct {
	Listen         string   `yaml:"listen"`
	BaseURL        string   `yaml:"base_url"`
	TLS            TLS      `yaml:"tls"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

type TLS struct {
	Cert string `yaml:"cert"`
	Key  string `yaml:"key"`
}

type Auth struct {
	JWTSecret        string   `yaml:"jwt_secret"`
	AccessTokenTTL   Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL  Duration `yaml:"refresh_token_ttl"`
	InviteDefaultTTL Duration `yaml:"invite_default_ttl"`
}

type Backup struct {
	DefaultKeep     int    `yaml:"default_keep"`
	DefaultInterval string `yaml:"default_interval"` // Go duration or cron expression
}

type Discord struct {
	Webhooks []Webhook `yaml:"webhooks"`
}

type Webhook struct {
	Name    string          `yaml:"name"`
	URL     string          `yaml:"url"`
	Events  map[string]bool `yaml:"events"`
	Servers []string        `yaml:"servers"` // server names; empty = all servers
}

type Java struct {
	CacheDir string `yaml:"cache_dir"`
}

func defaults() Config {
	return Config{
		Daemon: Daemon{
			Socket:   "/run/mcsman/mcsman.sock",
			DataDir:  "/var/lib/mcsman",
			LogLevel: "info",
		},
		HTTP: HTTP{Listen: "0.0.0.0:8420"},
		Auth: Auth{
			AccessTokenTTL:   Duration(15 * time.Minute),
			RefreshTokenTTL:  Duration(7 * 24 * time.Hour),
			InviteDefaultTTL: Duration(48 * time.Hour),
		},
		Backup: Backup{DefaultKeep: 24, DefaultInterval: "1h"},
	}
}

// Load reads the config at path, enforces 0600 permissions, overlays
// environment variables and validates the result.
func Load(path string) (*Config, error) {
	return LoadWithEnv(path, os.Getenv)
}

// LoadWithEnv is Load with environment lookups served by getenv (mcsctl
// doctor reads the service's EnvironmentFile instead of its own env).
func LoadWithEnv(path string, getenv func(string) string) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return nil, fmt.Errorf("config %s has permissions %#o, must be 0600", path, perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(raw, getenv)
}

func parse(raw []byte, getenv func(string) string) (*Config, error) {
	cfg := defaults()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	applyEnv(&cfg, getenv)
	if cfg.Java.CacheDir == "" {
		cfg.Java.CacheDir = cfg.Daemon.DataDir + "/java"
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyEnv(cfg *Config, getenv func(string) string) {
	if v := getenv("MCSMAN_JWT_SECRET"); v != "" {
		cfg.Auth.JWTSecret = v
	}
	for i := range cfg.Discord.Webhooks {
		if v := getenv(fmt.Sprintf("MCSMAN_DISCORD_WEBHOOK_%d", i)); v != "" {
			cfg.Discord.Webhooks[i].URL = v
		}
	}
}

func (c *Config) validate() error {
	var errs []error
	if len(c.Auth.JWTSecret) < 32 {
		errs = append(errs, errors.New("auth.jwt_secret must be at least 32 characters (set MCSMAN_JWT_SECRET)"))
	}
	if c.Daemon.DataDir == "" {
		errs = append(errs, errors.New("daemon.data_dir is required"))
	}
	if c.Daemon.Socket == "" {
		errs = append(errs, errors.New("daemon.socket is required"))
	}
	switch c.Daemon.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("daemon.log_level %q invalid", c.Daemon.LogLevel))
	}
	if (c.HTTP.TLS.Cert == "") != (c.HTTP.TLS.Key == "") {
		errs = append(errs, errors.New("http.tls.cert and http.tls.key must both be set or both empty"))
	}
	for _, p := range c.HTTP.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil && net.ParseIP(p) == nil {
			errs = append(errs, fmt.Errorf("http.trusted_proxies: %q is not an IP or CIDR", p))
		}
	}
	if c.Backup.DefaultKeep < 1 {
		errs = append(errs, errors.New("backup.default_keep must be >= 1"))
	}
	return errors.Join(errs...)
}

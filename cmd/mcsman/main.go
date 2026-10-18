// Command mcsman is the Minecraft server management daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mcsman/internal/api"
	"mcsman/internal/auth"
	"mcsman/internal/config"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/events"
	"mcsman/internal/server"
)

func main() {
	configPath := flag.String("config", "./mcsman.yaml", "path to config file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "mcsman:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	log := newLogger(cfg.Daemon.LogLevel)
	slog.SetDefault(log)

	if err := os.MkdirAll(cfg.Daemon.DataDir, 0o750); err != nil {
		return err
	}
	for _, sub := range []string{"servers", "backups", "jars"} {
		if err := os.MkdirAll(filepath.Join(cfg.Daemon.DataDir, sub), 0o750); err != nil {
			return err
		}
	}

	database, err := db.Open(filepath.Join(cfg.Daemon.DataDir, "mcsman.db"))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	rconSealer, err := crypto.NewSealer(cfg.Auth.JWTSecret, crypto.LabelRCON)
	if err != nil {
		return err
	}
	bus := events.NewBus()
	authSvc := auth.NewService(database, cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL.D(), cfg.Auth.RefreshTokenTTL.D())
	servers := server.NewManager(server.Options{
		DB: database, Bus: bus, Sealer: rconSealer, DataDir: cfg.Daemon.DataDir, Logger: log,
		Java: javaResolver(cfg.Java.CacheDir, log),
	})
	if err := servers.Load(context.Background()); err != nil {
		return fmt.Errorf("load servers: %w", err)
	}

	apiSrv := api.New(cfg, database, authSvc, servers, log)

	httpSrv := &http.Server{
		Addr:              cfg.HTTP.Listen,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	sockSrv := &http.Server{Handler: apiSrv.SocketHandler(), ReadHeaderTimeout: 10 * time.Second}

	sockLn, err := api.ListenSocket(cfg.Daemon.Socket, log)
	if err != nil {
		return fmt.Errorf("listen socket: %w", err)
	}
	httpLn, err := net.Listen("tcp", cfg.HTTP.Listen)
	if err != nil {
		sockLn.Close()
		return fmt.Errorf("listen http: %w", err)
	}

	errc := make(chan error, 2)
	go func() {
		log.Info("socket listening", "path", cfg.Daemon.Socket)
		errc <- sockSrv.Serve(sockLn)
	}()
	go func() {
		if cfg.HTTP.TLS.Cert != "" {
			log.Info("https listening", "addr", cfg.HTTP.Listen)
			errc <- httpSrv.ServeTLS(httpLn, cfg.HTTP.TLS.Cert, cfg.HTTP.TLS.Key)
		} else {
			log.Info("http listening", "addr", cfg.HTTP.Listen)
			errc <- httpSrv.Serve(httpLn)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go housekeeping(ctx, apiSrv, database, log)

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("listener failed", "err", err)
		}
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutCtx)
	sockSrv.Shutdown(shutCtx)
	servers.Shutdown(shutCtx)
	os.Remove(cfg.Daemon.Socket)
	log.Info("stopped")
	return nil
}

func housekeeping(ctx context.Context, a *api.Server, d *db.DB, log *slog.Logger) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Sweep()
			if n++; n%60 == 0 {
				if err := d.PruneSessions(ctx, time.Now().Add(-24*time.Hour).Unix()); err != nil {
					log.Warn("prune sessions", "err", err)
				}
			}
		}
	}
}

// javaResolver finds the java binary for a major version: a managed install
// under cacheDir if present, otherwise java on PATH. Managed downloads
// arrive with the Java manager.
func javaResolver(cacheDir string, log *slog.Logger) func(string) (string, error) {
	return func(version string) (string, error) {
		managed := filepath.Join(cacheDir, version, "bin", "java")
		if _, err := os.Stat(managed); err == nil {
			return managed, nil
		}
		p, err := exec.LookPath("java")
		if err != nil {
			return "", fmt.Errorf("no managed Java %s in %s and no java on PATH", version, cacheDir)
		}
		log.Warn("using system java; managed install not found", "want", version, "path", p)
		return p, nil
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

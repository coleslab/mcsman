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
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mcsman/internal/api"
	"mcsman/internal/auth"
	"mcsman/internal/backup"
	"mcsman/internal/config"
	"mcsman/internal/cronspec"
	"mcsman/internal/crypto"
	"mcsman/internal/db"
	"mcsman/internal/discord"
	"mcsman/internal/events"
	"mcsman/internal/java"
	"mcsman/internal/metrics"
	"mcsman/internal/plugin"
	"mcsman/internal/scheduler"
	"mcsman/internal/server"
	"mcsman/internal/version"
	"mcsman/internal/whitelist"
	"mcsman/internal/world"
	"mcsman/web"
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
	if cfg.HTTP.BaseURL != "" {
		// PaperMC and Modrinth ask clients to identify themselves.
		version.UserAgent = "mcsman/1.0 (+" + cfg.HTTP.BaseURL + ")"
		plugin.UserAgent = version.UserAgent
	}
	bus := events.NewBus()
	authSvc := auth.NewService(database, cfg.Auth.JWTSecret, cfg.Auth.AccessTokenTTL.D(), cfg.Auth.RefreshTokenTTL.D())
	javaMgr := java.NewManager(cfg.Java.CacheDir, log)
	paper := version.NewClient()
	jars := &version.JarStore{Dir: filepath.Join(cfg.Daemon.DataDir, "jars", "paper"), Client: paper}
	servers := server.NewManager(server.Options{
		DB: database, Bus: bus, Sealer: rconSealer, DataDir: cfg.Daemon.DataDir, Logger: log,
		Java:        javaMgr.Resolve,
		Provisioner: &version.Provisioner{Client: paper, Jars: jars},
	})
	if err := servers.Load(context.Background()); err != nil {
		return fmt.Errorf("load servers: %w", err)
	}
	backups := &backup.Manager{
		DB: database, Servers: servers, Bus: bus, Log: log,
		DataDir: cfg.Daemon.DataDir, Keep: cfg.Backup.DefaultKeep,
	}

	wl := &whitelist.Service{DB: database, Servers: servers, Bus: bus, Log: log}
	invites := &auth.Invites{
		DB: database, Auth: authSvc, BaseURL: cfg.HTTP.BaseURL, DefaultTTL: cfg.Auth.InviteDefaultTTL.D(),
	}

	plugins := plugin.NewInstaller(database, servers, log, plugin.NewModrinth(""), plugin.NewHangar(""))
	upgrader := &version.Upgrader{
		Paper: paper, Jars: jars, Java: javaMgr, Plugins: plugins, Backups: backups,
		Servers: servers, DB: database, Bus: bus, Log: log,
	}

	webhookSealer, err := crypto.NewSealer(cfg.Auth.JWTSecret, crypto.LabelWebhook)
	if err != nil {
		return err
	}
	notifier := &discord.Notifier{
		DB: database, Sealer: webhookSealer, Servers: servers, Bus: bus, Log: log,
		HTTP: &http.Client{Timeout: 15 * time.Second},
	}
	if err := notifier.Seed(context.Background(), cfg.Discord.Webhooks); err != nil {
		return fmt.Errorf("seed webhooks: %w", err)
	}
	worlds := &world.Manager{DB: database, Servers: servers, Backups: backups, Log: log}
	if err := cronspec.Validate(cfg.Backup.DefaultInterval); err != nil {
		return fmt.Errorf("backup.default_interval: %w", err)
	}
	sched := &scheduler.Scheduler{
		Servers: servers, Backups: backups, DB: database, Log: log,
		DefaultBackup: cfg.Backup.DefaultInterval,
	}

	apiSrv := api.New(api.Deps{
		Config: cfg, DB: database, Auth: authSvc, Servers: servers, Backups: backups,
		Versions: paper, Bus: bus, Invites: invites, Whitelist: wl,
		Plugins: plugins, Upgrader: upgrader, Discord: notifier, Worlds: worlds, Scheduler: sched,
		Log: log, Web: web.Handler(),
	})

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
	go (&metrics.Collector{Servers: servers, Bus: bus, Log: log}).Run(ctx)
	go wl.Run(ctx)
	go notifier.Run(ctx)
	sched.Start()
	go autostart(servers, log)

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
	sched.Stop(shutCtx)
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

// autostart starts servers flagged to come up with the daemon, one at a
// time so first-run Java downloads don't run in parallel.
func autostart(servers *server.Manager, log *slog.Logger) {
	for _, inst := range servers.All() {
		srv := inst.Server()
		if !srv.Autostart {
			continue
		}
		log.Info("autostarting server", "server", srv.Name)
		if err := inst.Start(); err != nil {
			log.Error("autostart failed", "server", srv.Name, "err", err)
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

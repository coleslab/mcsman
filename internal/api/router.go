// Package api is the HTTP and WebSocket API, served over TCP for the web UI
// and over a Unix socket for mcsctl.
package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"mcsman/internal/auth"
	"mcsman/internal/backup"
	"mcsman/internal/config"
	"mcsman/internal/db"
	"mcsman/internal/discord"
	"mcsman/internal/events"
	"mcsman/internal/plugin"
	"mcsman/internal/scheduler"
	"mcsman/internal/server"
	"mcsman/internal/version"
	"mcsman/internal/whitelist"
	"mcsman/internal/world"
)

// Deps are the API's collaborators.
type Deps struct {
	Config    *config.Config
	DB        *db.DB
	Auth      *auth.Service
	Servers   *server.Manager
	Backups   *backup.Manager
	Versions  *version.Client
	Bus       *events.Bus
	Invites   *auth.Invites
	Whitelist *whitelist.Service
	Plugins   *plugin.Installer
	Upgrader  *version.Upgrader
	Discord   *discord.Notifier
	Worlds    *world.Manager
	Scheduler *scheduler.Scheduler
	Log       *slog.Logger
	// Web serves the embedded UI for non-API paths; nil disables it.
	Web http.Handler
}

// Server holds the API's dependencies.
type Server struct {
	cfg       *config.Config
	db        *db.DB
	auth      *auth.Service
	servers   *server.Manager
	backups   *backup.Manager
	versions  *version.Client
	bus       *events.Bus
	invites   *auth.Invites
	whitelist *whitelist.Service
	plugins   *plugin.Installer
	upgrader  *version.Upgrader
	discord   *discord.Notifier
	worlds    *world.Manager
	scheduler *scheduler.Scheduler
	log       *slog.Logger
	web       http.Handler
	tickets   *ticketStore

	loginLimit   *rateLimiter
	refreshLimit *rateLimiter
	redeemLimit  *rateLimiter
}

func New(d Deps) *Server {
	return &Server{
		cfg: d.Config, db: d.DB, auth: d.Auth, servers: d.Servers, backups: d.Backups,
		versions: d.Versions, bus: d.Bus, invites: d.Invites, whitelist: d.Whitelist,
		plugins: d.Plugins, upgrader: d.Upgrader, discord: d.Discord, worlds: d.Worlds,
		scheduler: d.Scheduler, log: d.Log, web: d.Web,
		tickets:      newTicketStore(),
		loginLimit:   newRateLimiter(10),
		refreshLimit: newRateLimiter(20),
		redeemLimit:  newRateLimiter(5),
	}
}

// Sweep evicts idle rate-limiter entries and expired WebSocket tickets.
// Call periodically.
func (s *Server) Sweep() {
	s.loginLimit.sweep()
	s.refreshLimit.sweep()
	s.redeemLimit.sweep()
	s.tickets.sweep()
}

// Handler returns the HTTP handler for the TCP listener.
func (s *Server) Handler() http.Handler { return s.router() }

// SocketHandler returns the handler for the Unix socket: same routes, with
// every request authenticated as admin.
func (s *Server) SocketHandler() http.Handler { return socketAuth(s.router()) }

func (s *Server) router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(realIP(parseTrusted(s.cfg.HTTP.TrustedProxies)))
	r.Use(requestLogger(s.log))
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Post("/init", s.handleInit)

		r.With(s.loginLimit.middleware).Post("/auth/login", s.handleLogin)
		r.With(s.refreshLimit.middleware).Post("/auth/refresh", s.handleRefresh)
		r.With(s.redeemLimit.middleware).Get("/invites/{token}", s.handleInviteLookup)
		r.With(s.redeemLimit.middleware).Post("/invites/{token}/redeem", s.handleRedeem)
		r.Get("/status", s.handleStatus)

		// The console WebSocket authenticates with a ticket in the query
		// string, since browsers cannot set headers on WebSocket requests.
		r.Get("/servers/{id}/console", s.handleConsole)
		r.Get("/events", s.handleEvents)

		// Large downloads accept ?ticket= so browsers can stream them to
		// disk with a plain link instead of buffering via fetch.
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuthOrTicket, requireAdmin)
			r.Get("/servers/{id}/backups/{backup_id}/download", s.handleDownloadBackup)
			r.Get("/servers/{id}/worlds/export", s.handleExportWorld)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Post("/ws-ticket", s.handleWSTicket)
			r.Post("/users/me/password", s.handleChangePassword)

			r.Get("/servers/{id}", s.handleGetServer)
			r.Get("/servers/{id}/players", s.handlePlayers)
			r.Get("/servers/{id}/backups", s.handleListBackups)
			r.Get("/versions/paper", s.handlePaperVersions)
			r.Get("/servers/{id}/plugins", s.handleListPlugins)
			r.Get("/plugins/search", s.handleSearchPlugins)
			r.Get("/servers/{id}/worlds/imports", s.handleWorldImports)

			r.Group(func(r chi.Router) {
				r.Use(requireAdmin)
				r.Post("/invites", s.handleCreateInvite)
				r.Get("/invites", s.handleListInvites)
				r.Delete("/invites/{id}", s.handleRevokeInvite)
				r.Get("/users", s.handleListUsers)
				r.Patch("/users/{id}", s.handleUpdateUser)
				r.Delete("/users/{id}", s.handleDeleteUser)

				r.Get("/servers", s.handleListServers)
				r.Post("/servers", s.handleCreateServer)
				r.Patch("/servers/{id}", s.handleUpdateServer)
				r.Delete("/servers/{id}", s.handleDeleteServer)
				r.Post("/servers/{id}/start", s.handleStart)
				r.Post("/servers/{id}/stop", s.handleStop)
				r.Post("/servers/{id}/restart", s.handleRestart)
				r.Post("/servers/{id}/command", s.handleCommand)
				r.Get("/servers/{id}/events", s.handleServerEvents)
				r.Post("/servers/{id}/backups", s.handleCreateBackup)
				r.Post("/servers/{id}/backups/{backup_id}/restore", s.handleRestoreBackup)
				r.Delete("/servers/{id}/backups/{backup_id}", s.handleDeleteBackup)
				r.Post("/servers/{id}/plugins", s.handleInstallPlugin)
				r.Get("/servers/{id}/plugins/updates", s.handlePluginUpdates)
				r.Delete("/servers/{id}/plugins/{plugin_id}", s.handleRemovePlugin)
				r.Post("/servers/{id}/plugins/{plugin_id}/update", s.handleUpdatePlugin)
				r.Post("/servers/{id}/upgrade", s.handleUpgradePreflight)
				r.Post("/servers/{id}/upgrade/confirm", s.handleUpgradeConfirm)
				r.Get("/servers/{id}/upgrade/status", s.handleUpgradeStatus)
				r.Post("/servers/{id}/worlds/import", s.handleImportWorld)

				r.Get("/webhooks", s.handleListWebhooks)
				r.Post("/webhooks", s.handleCreateWebhook)
				r.Patch("/webhooks/{id}", s.handleUpdateWebhook)
				r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
				r.Post("/webhooks/{id}/test", s.handleTestWebhook)
			})
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if s.web != nil && !strings.HasPrefix(r.URL.Path, "/api/") {
			s.web.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	})
	return r
}

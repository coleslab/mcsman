// Package api is the HTTP and WebSocket API, served over TCP for the web UI
// and over a Unix socket for mcsctl.
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"mcsman/internal/auth"
	"mcsman/internal/config"
	"mcsman/internal/db"
	"mcsman/internal/server"
)

// Server holds the API's dependencies.
type Server struct {
	cfg     *config.Config
	db      *db.DB
	auth    *auth.Service
	servers *server.Manager
	log     *slog.Logger
	tickets *ticketStore

	loginLimit   *rateLimiter
	refreshLimit *rateLimiter
}

func New(cfg *config.Config, d *db.DB, a *auth.Service, m *server.Manager, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, db: d, auth: a, servers: m, log: log,
		tickets:      newTicketStore(),
		loginLimit:   newRateLimiter(10),
		refreshLimit: newRateLimiter(20),
	}
}

// Sweep evicts idle rate-limiter entries and expired WebSocket tickets.
// Call periodically.
func (s *Server) Sweep() {
	s.loginLimit.sweep()
	s.refreshLimit.sweep()
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

		// The console WebSocket authenticates with a ticket in the query
		// string, since browsers cannot set headers on WebSocket requests.
		r.Get("/servers/{id}/console", s.handleConsole)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Post("/auth/logout", s.handleLogout)
			r.Get("/auth/me", s.handleMe)
			r.Post("/ws-ticket", s.handleWSTicket)

			r.Get("/servers/{id}", s.handleGetServer)
			r.Get("/servers/{id}/players", s.handlePlayers)

			r.Group(func(r chi.Router) {
				r.Use(requireAdmin)
				r.Get("/servers", s.handleListServers)
				r.Post("/servers", s.handleCreateServer)
				r.Patch("/servers/{id}", s.handleUpdateServer)
				r.Delete("/servers/{id}", s.handleDeleteServer)
				r.Post("/servers/{id}/start", s.handleStart)
				r.Post("/servers/{id}/stop", s.handleStop)
				r.Post("/servers/{id}/restart", s.handleRestart)
				r.Post("/servers/{id}/command", s.handleCommand)
				r.Get("/servers/{id}/events", s.handleServerEvents)
			})
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	return r
}

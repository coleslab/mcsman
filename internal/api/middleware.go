package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"mcsman/internal/auth"
	"mcsman/internal/db"
)

type ctxKey int

const (
	principalKey ctxKey = iota
	clientIPKey
	viaSocketKey
)

func principalFrom(ctx context.Context) *auth.Principal {
	p, _ := ctx.Value(principalKey).(*auth.Principal)
	return p
}

func clientIP(r *http.Request) string {
	ip, _ := r.Context().Value(clientIPKey).(string)
	return ip
}

func viaSocket(r *http.Request) bool {
	v, _ := r.Context().Value(viaSocketKey).(bool)
	return v
}

// socketPrincipal is the identity given to mcsctl over the Unix socket.
// Access to the socket (0660) is the authorisation.
var socketPrincipal = &auth.Principal{User: &db.User{Username: "mcsctl", Role: db.RoleAdmin}}

// socketAuth marks requests arriving on the Unix socket as admin.
func socketAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), viaSocketKey, true)
		ctx = context.WithValue(ctx, principalKey, socketPrincipal)
		ctx = context.WithValue(ctx, clientIPKey, "socket")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// realIP resolves the client IP, honouring X-Forwarded-For only when the
// direct peer is a trusted proxy.
func realIP(trusted []*net.IPNet) func(http.Handler) http.Handler {
	isTrusted := func(ip net.IP) bool {
		for _, n := range trusted {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if viaSocket(r) {
				next.ServeHTTP(w, r)
				return
			}
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				host = r.RemoteAddr
			}
			ip := host
			if peer := net.ParseIP(host); peer != nil && isTrusted(peer) {
				// Walk X-Forwarded-For right to left; first untrusted hop is the client.
				hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				for i := len(hops) - 1; i >= 0; i-- {
					h := net.ParseIP(strings.TrimSpace(hops[i]))
					if h == nil {
						break
					}
					ip = h.String()
					if !isTrusted(h) {
						break
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip)))
		})
	}
}

func parseTrusted(list []string) []*net.IPNet {
	var out []*net.IPNet
	for _, s := range list {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return out
}

// requestLogger logs one line per request. It never logs query strings,
// which may carry WebSocket tickets.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r)
			log.Debug("http request",
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"dur", time.Since(start).Round(time.Millisecond), "ip", clientIP(r),
				"req_id", middleware.GetReqID(r.Context()))
		})
	}
}

// requireAuth validates the bearer token unless a principal is already set
// (Unix socket).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFrom(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		p, err := s.auth.Authenticate(r.Context(), tok)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := principalFrom(r.Context()); p == nil || !p.IsAdmin() {
			writeError(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimiter is a per-IP token bucket.
type rateLimiter struct {
	limit rate.Limit
	burst int

	mu      sync.Mutex
	clients map[string]*limiterEntry
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	return &rateLimiter{
		limit:   rate.Every(time.Minute / time.Duration(perMinute)),
		burst:   perMinute,
		clients: map[string]*limiterEntry{},
	}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e, ok := rl.clients[key]
	if !ok {
		e = &limiterEntry{lim: rate.NewLimiter(rl.limit, rl.burst)}
		rl.clients[key] = e
	}
	e.seen = time.Now()
	return e.lim.Allow()
}

// sweep drops entries idle long enough to have refilled completely.
func (rl *rateLimiter) sweep() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, e := range rl.clients {
		if time.Since(e.seen) > 2*time.Minute {
			delete(rl.clients, k)
		}
	}
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !viaSocket(r) && !rl.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

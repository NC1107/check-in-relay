// Package api wires the relay's HTTP endpoints: self-serve key registration, the
// authenticated send fan-out, a health check, and a token-gated admin surface.
package api

import (
	"context"
	"net/http"

	"github.com/nc1107/check-in-relay/internal/config"
	"github.com/nc1107/check-in-relay/internal/fcm"
	"github.com/nc1107/check-in-relay/internal/keys"
	"github.com/nc1107/check-in-relay/internal/ratelimit"
)

// fcmSender is the slice of *fcm.Sender the relay depends on, named as an interface so
// tests can substitute a fake FCM.
type fcmSender interface {
	Send(ctx context.Context, msgs []fcm.Message) []fcm.Result
}

// Server holds dependencies shared by all handlers.
type Server struct {
	cfg         config.Config
	fcm         fcmSender
	keys        *keys.Store
	registerLim *ratelimit.Limiter
	sendLim     *ratelimit.Limiter
}

// New constructs a Server.
func New(cfg config.Config, sender *fcm.Sender, store *keys.Store) *Server {
	return &Server{
		cfg:         cfg,
		fcm:         sender,
		keys:        store,
		registerLim: ratelimit.New(float64(cfg.RegisterPerHour)/60.0, cfg.RegisterBurst),
		sendLim:     ratelimit.New(float64(cfg.SendPerMinute), cfg.SendBurst),
	}
}

// Router builds the HTTP handler. Routing uses net/http's method+path patterns (Go 1.22+),
// keeping the relay free of a router dependency.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/register", s.handleRegister)
	mux.HandleFunc("POST /v1/send", s.handleSend)

	// Admin is only mounted when a token is configured, and every request must carry it.
	if s.cfg.AdminToken != "" {
		mux.HandleFunc("GET /admin/keys", s.requireAdmin(s.handleAdminListKeys))
		mux.HandleFunc("POST /admin/keys/{id}/revoke", s.requireAdmin(s.handleAdminRevokeKey))
	}
	return recoverer(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

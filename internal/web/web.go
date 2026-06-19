// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package web serves the optional read-only diagnostic web UI: a snapshot of
// the latest resolved values plus a small health endpoint. It is a thin
// standard-library HTTP layer over a [state.Store]; a hand-written vanilla
// single-page app (no build step) is embedded at build time, so enabling the
// UI adds no third-party dependencies and keeps the daemon a static binary.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/state"
)

// staticFS holds the compiled single-page app (hand-written vanilla
// HTML/CSS/JS, no build step): index.html at the root and /static/* assets.
//
//go:embed assets
var staticFS embed.FS

// Deps are the web server's collaborators.
type Deps struct {
	Cfg           *config.Config
	Store         *state.Store
	MQTTConnected func() bool // nil → reported as unknown/false
	Logger        *slog.Logger
}

// Server is the diagnostic HTTP server. Build it with [New], run it with [Run].
type Server struct {
	cfg     *config.Config
	store   *state.Store
	mqttUp  func() bool
	log     *slog.Logger
	handler http.Handler
}

// New builds a Server. It does not bind a socket — call [Server.Run].
func New(d Deps) *Server {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	cfg := d.Cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	s := &Server{cfg: cfg, store: d.Store, mqttUp: d.MQTTConnected, log: log}
	s.handler = s.withAuth(s.routes())
	return s
}

// Handler returns the fully wired handler (auth middleware + routes).
func (s *Server) Handler() http.Handler { return s.handler }

// routes wires the REST API and the embedded SPA onto a ServeMux.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/snapshot", s.handleSnapshot)

	sub, err := fs.Sub(staticFS, "assets")
	if err != nil {
		panic("web: embed sub fs: " + err.Error()) // compile-time constant path
	}
	mux.Handle("GET /", http.FileServerFS(sub))
	return mux
}

// withAuth wraps next with HTTP Basic auth when both credentials are set.
func (s *Server) withAuth(next http.Handler) http.Handler {
	if s.cfg.WebUser == "" && s.cfg.WebPassword == "" {
		return next
	}
	wantUser := []byte(s.cfg.WebUser)
	wantPass := []byte(s.cfg.WebPassword)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), wantUser) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), wantPass) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="zendure2mqtt", charset="UTF-8"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run binds Cfg.WebBind and serves until ctx is cancelled, then shuts down
// gracefully. A bind failure surfaces as an error so the daemon tears down.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.WebBind,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	s.log.Info("web.listening", slog.String("bind", s.cfg.WebBind), slog.Bool("auth", s.cfg.WebUser != ""))

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx) //nolint:contextcheck // fresh context for graceful shutdown
		s.log.Info("web.stopped")
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

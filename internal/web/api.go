// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/state"
)

// liveView is the combined payload the SSE stream pushes and that the
// /api/status endpoint mirrors: everything the dashboard needs to redraw
// in one round-trip.
type liveView struct {
	Health   state.Health   `json:"health"`
	Snapshot state.Snapshot `json:"snapshot"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.backend.Health())
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, liveView{
		Health:   s.backend.Health(),
		Snapshot: s.backend.Snapshot(),
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.backend.Snapshot())
}

func (s *Server) handleItems(w http.ResponseWriter, _ *http.Request) {
	items := s.backend.Items()
	if items == nil {
		items = []state.ItemInfo{}
	}
	s.writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, s.backend.Config())
}

// writeRequest is the body of POST /api/write: the unit's serial, the
// item key ([state.ItemInfo.Key]) and the value. Value is decoded as a raw
// JSON value so the client may send a number, string or bool — all get
// normalised to the plain payload an MQTT `set` would carry.
type writeRequest struct {
	SN    string `json:"sn"`
	Key   string `json:"key"`
	Value any    `json:"value"`
}

// writeBodyTimeout bounds how long a POST /api/write client may take to
// deliver its request body. The server's ReadHeaderTimeout only covers
// the header phase and MaxBytesReader only caps the byte count, so
// without this a client trickling the body would hold the handler (and
// its connection/goroutine) indefinitely. A var, not a const, so tests
// can shorten it.
var writeBodyTimeout = 10 * time.Second

func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	// Best-effort: not every ResponseWriter supports read deadlines, and
	// a failure to set one just means the pre-existing behaviour.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(writeBodyTimeout))

	var req writeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if req.SN == "" {
		s.writeError(w, http.StatusBadRequest, "missing 'sn'")
		return
	}
	if req.Key == "" {
		s.writeError(w, http.StatusBadRequest, "missing 'key'")
		return
	}
	value := normaliseValue(req.Value)

	err := s.backend.Write(r.Context(), req.SN, req.Key, value)
	switch {
	case err == nil:
		s.log.Info("web.write_ok",
			slog.String("sn", req.SN), slog.String("key", req.Key), slog.String("value", value))
		s.writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "sn": req.SN, "key": req.Key, "value": value,
		})
	case errors.Is(err, state.ErrUnknownDevice):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, state.ErrNotWritable),
		errors.Is(err, state.ErrInvalidValue):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, state.ErrNotReady):
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		// Transport failures — the unit is known but the write did not
		// go through. The raw error can contain internal details (hosts,
		// cloud responses), so only a generic message goes to the client;
		// the coordinator has logged the specifics already.
		s.writeError(w, http.StatusBadGateway, "write failed")
	}
}

// sseWriteTimeout bounds each SSE frame write. The server deliberately
// has no WriteTimeout (SSE responses are long-lived), so without a
// per-write deadline a client that keeps the TCP connection open but
// stops reading (zero receive window) fills the kernel send buffer and
// blocks the handler in Write/Flush forever — leaking the goroutine,
// the connection and the Changes subscription. A var, not a const, so
// tests can shorten it.
var sseWriteTimeout = 15 * time.Second

// handleEvents streams Server-Sent Events. It pushes a full liveView on
// connect, then on every change notification, plus a periodic tick so
// the uptime counter advances and the connection stays warm through
// idle proxies.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

	changes, cancel := s.backend.Changes()
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	rc := http.NewResponseController(w)
	send := func() bool {
		view := liveView{Health: s.backend.Health(), Snapshot: s.backend.Snapshot()}
		payload, err := json.Marshal(view)
		if err != nil {
			return false
		}
		// Refresh the write deadline per frame (best-effort — not every
		// ResponseWriter supports it): a stalled client then errors the
		// blocked write instead of pinning this goroutine, its connection
		// and the Changes subscription until process exit.
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", payload); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil {
			return false
		}
		return true
	}

	if !send() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send() {
				return
			}
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

// normaliseValue renders a decoded JSON value to the plain payload an MQTT
// `set` carries. json.Number-free: encoding/json gives us
// float64 for numbers, so format those without a trailing ".000000".
func normaliseValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// Integers commonly arrive as float64 (e.g. 3000) — render them
		// without a decimal point so the int parse branch wins downstream.
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// writeJSON encodes v as the response body with the given status. The
// status is already committed via WriteHeader by the time Encode runs, so
// an encode failure (a disconnected client, or the write deadline from
// [Server.withWriteDeadline] expiring) can no longer be turned into an
// error response — it is logged instead of being silently discarded.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("web.response_encode_failed",
			slog.Int("status", status), slog.String("err", err.Error()))
	}
}

// writeError sends a small JSON error envelope.
func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

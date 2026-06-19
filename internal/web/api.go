// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/version"
)

// health is the /api/health payload.
type health struct {
	Version       string    `json:"version"`
	Connection    string    `json:"connection"`
	Language      string    `json:"language"`
	MQTTServer    string    `json:"mqtt_server"`
	MQTTConnected bool      `json:"mqtt_connected"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Devices       int       `json:"devices"`
}

// handleHealth reports build, connectivity and uptime.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	snap := s.store.Snapshot()
	mqttUp := false
	if s.mqttUp != nil {
		mqttUp = s.mqttUp()
	}
	s.writeJSON(w, health{
		Version:       version.String(),
		Connection:    s.cfg.Connection,
		Language:      s.cfg.Language,
		MQTTServer:    s.cfg.MQTTServer,
		MQTTConnected: mqttUp,
		StartedAt:     snap.StartedAt,
		UptimeSeconds: int64(time.Since(snap.StartedAt).Seconds()),
		Devices:       len(snap.Devices),
	})
}

// handleSnapshot returns the latest resolved values per device.
func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, s.store.Snapshot())
}

// writeJSON encodes v as JSON, logging (but not surfacing) an encode error.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("web.encode_failed", slog.String("err", err.Error()))
	}
}

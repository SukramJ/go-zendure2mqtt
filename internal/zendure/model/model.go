// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package model holds the transport-neutral Zendure data model.
//
// Both the local HTTP API (GET /properties/report) and the cloud MQTT
// telemetry (.../properties/report) deliver the same shape: a flat
// properties map plus an optional packData array for per-battery values.
// Parsing once here lets the local and cloud backends feed an identical
// downstream pipeline (process → publish → HA discovery).
package model

import (
	"encoding/json"
	"fmt"
)

// Report is one telemetry snapshot of a Zendure device. Properties and
// PackData are intentionally untyped (map of raw JSON values) because the
// concrete key set differs per product; the catalog decides which keys
// become entities and how they are scaled.
type Report struct {
	Timestamp  int64            `json:"timestamp"`
	MessageID  int64            `json:"messageId"`
	SN         string           `json:"sn"`
	Version    int              `json:"version"`
	Product    string           `json:"product"`
	Properties map[string]any   `json:"properties"`
	PackData   []map[string]any `json:"packData"`
}

// ParseReport decodes a /properties/report payload (local HTTP body or
// cloud MQTT message) into a [Report].
func ParseReport(b []byte) (*Report, error) {
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("model: parse report: %w", err)
	}
	if r.Properties == nil {
		r.Properties = map[string]any{}
	}
	return &r, nil
}

// WriteRequest is the body sent to a device to change properties.
//
// Local transport posts {sn, properties} to /properties/write; cloud
// transport publishes {deviceId, messageId, timestamp, properties} to the
// device's properties/write topic. The shared fields live here; each
// backend adds what its transport needs.
type WriteRequest struct {
	SN         string         `json:"sn,omitempty"`
	DeviceID   string         `json:"deviceId,omitempty"`
	MessageID  int64          `json:"messageId,omitempty"`
	Timestamp  int64          `json:"timestamp,omitempty"`
	Properties map[string]any `json:"properties"`
}

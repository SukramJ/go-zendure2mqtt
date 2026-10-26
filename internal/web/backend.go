// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package web serves the optional embedded status/health dashboard.
//
// It is a thin HTTP layer over a [Backend] (implemented by the
// coordinator): a small REST API plus a Server-Sent-Events stream feed a
// single-page app that is compiled into the binary via go:embed. The
// package depends only on the standard library and the shared
// internal/state DTOs, so enabling the UI adds no third-party
// dependencies and keeps the daemon a single static binary. Layout, API
// and hardening follow go-mtec2mqtt and go-keba2mqtt; what differs is that
// one bridge serves several units, so the snapshot and writes are per
// serial.
package web

import (
	"context"

	"github.com/SukramJ/go-zendure2mqtt/internal/state"
)

// Backend is the read/write surface the web server needs. The
// coordinator satisfies it structurally — defining it here (rather than
// importing the coordinator) keeps the dependency arrow pointing one
// way and makes the handlers trivially testable with a fake.
type Backend interface {
	// Snapshot returns the latest values of every unit.
	Snapshot() state.Snapshot
	// Health returns the operational status surface.
	Health() state.Health
	// Config returns the sanitised, read-only config projection.
	Config() state.ConfigView
	// Items returns what the UI can write, with labels and bounds.
	Items() []state.ItemInfo
	// Write sets one item of the unit sn, synchronously, through the same
	// path as an MQTT `set`; errors are classed by the state.Err* values.
	Write(ctx context.Context, sn, key, value string) error
	// Changes returns a coalesced change-notification channel plus a
	// cancel func to release it.
	Changes() (<-chan struct{}, func())
}

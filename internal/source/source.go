// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package source defines the transport-neutral seam between the bridge
// core and a concrete Zendure backend.
//
// The local backend (HTTP polling) and the cloud backend (MQTT pub/sub)
// both satisfy [Backend]: they surface the same device list, emit the
// same readings, and accept the same writes. The coordinator never knows
// which transport it talks to — only this interface.
package source

import (
	"context"

	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Device identifies one Zendure device across both transports.
//
// Locally a device is reached by Address (IP/host) and addressed by SN.
// In the cloud it is addressed by DeviceID and ProductKey (the MQTT topic
// components); Address is then empty.
type Device struct {
	// SN is the device serial number, used as the stable MQTT identity
	// (topic root segment) regardless of transport.
	SN string
	// DeviceName is an optional human-friendly device name. When non-empty it
	// is used for the HA device name and to seed entity_ids in place of the
	// serial number; when empty the SN-based default ("Zendure <SN>") applies.
	// It never affects MQTT topics or unique_ids, which stay keyed on SN.
	DeviceName string
	// DeviceID is the cloud device key. For local-only devices it equals SN.
	DeviceID string
	// ProductKey is the cloud MQTT topic component. Empty for local devices.
	ProductKey string
	// Model is the human-readable product model (e.g. "solarflow 2400 ac").
	Model string
	// Address is the local host or IP (e.g. "192.168.1.50"). Empty for cloud.
	Address string
}

// Reading pairs a device with one freshly received telemetry [model.Report].
type Reading struct {
	Device Device
	Report *model.Report
}

// Handler consumes readings as they arrive (poll tick or cloud message).
type Handler func(Reading)

// Source produces device readings. Local backends poll on an interval;
// cloud backends stream via MQTT subscriptions. Run blocks until ctx is
// cancelled.
type Source interface {
	Devices() []Device
	Run(ctx context.Context, onReading Handler) error
	// Read fetches a single fresh report for dev — used for immediate
	// feedback right after a write. Push-based backends (cloud) may have no
	// one-shot read and return (nil, error); callers treat that as "skip,
	// the periodic poll will catch up".
	Read(ctx context.Context, dev Device) (*model.Report, error)
}

// Controller writes properties back to a device.
type Controller interface {
	Write(ctx context.Context, dev Device, props map[string]any) error
}

// Backend is the combined role a transport implementation provides.
type Backend interface {
	Source
	Controller
}

// Observer is told what a backend knows about its upstream: whether the
// upstream as a whole is usable, and whether each device answers. The
// coordinator implements it and turns it into mqtt-smarthome's
// `<name>/connected` (1 or 2) and `<name>/status/<sn>/online`.
//
// Both methods are called on the backend's own goroutines, repeatedly and
// with unchanged values; an implementation must be cheap and idempotent.
type Observer interface {
	// UpstreamUsable reports whether the backend can currently reach the
	// thing it bridges: for the cloud, its MQTT session; for the local
	// transport, at least one reachable device.
	UpstreamUsable(usable bool)
	// DeviceReachable reports whether dev is reachable through the backend:
	// locally, it answers its HTTP API (unreachable only after consecutive
	// failed polls, not after one); through the cloud, it is listed by the
	// login, re-reported on every session restore — a session drop is the
	// upstream's business and does not make a device unreachable.
	DeviceReachable(dev Device, reachable bool)
}

// Observable is a backend that reports to an [Observer]. Observe is called
// once, before Run.
type Observable interface {
	Observe(o Observer)
}

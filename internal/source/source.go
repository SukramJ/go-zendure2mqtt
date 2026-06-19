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

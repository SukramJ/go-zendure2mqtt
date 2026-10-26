// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package state

import (
	"time"

	"github.com/SukramJ/go-bridgeui"
)

// The DTOs below are what the web UI's API serves. They live here, beside the
// cache, so the coordinator (which fills them) and the web package (which
// serves them) share them without importing each other — the same split as
// go-mtec2mqtt and go-keba2mqtt.

// Health is the operational status surface (GET /api/health).
type Health struct {
	// Status is "starting" until a device has reported, "ok" while the
	// broker, the backend and every known device are reachable, else
	// "degraded".
	Status        string    `json:"status"`
	Version       string    `json:"version"`
	Connection    string    `json:"connection"` // local | cloud
	Upstream      *bool     `json:"upstream"`   // backend reaches what it bridges; nil = not yet known
	MQTTServer    string    `json:"mqtt_server"`
	MQTTTopic     string    `json:"mqtt_topic"`
	MQTTConnected bool      `json:"mqtt_connected"`
	HASSEnabled   bool      `json:"hass_enabled"`
	EVCCEnabled   bool      `json:"evcc_enabled"`
	Language      string    `json:"language"`
	StartedAt     time.Time `json:"started_at"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	// Devices is the number of known devices; per-device detail is in the
	// snapshot.
	Devices  int       `json:"devices"`
	Problems []Problem `json:"problems"`
}

// Problem is one entry of the recent-problem log: a rejected or failed
// command, from MQTT or the web UI.
type Problem struct {
	At     time.Time `json:"at"`
	SN     string    `json:"sn,omitempty"`
	Event  string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
}

// ConfigView is the sanitised, read-only config projection
// (GET /api/config): no token, no password.
type ConfigView struct {
	Connection             string       `json:"connection"`
	Refresh                int          `json:"refresh"`
	LocalDevices           []DeviceView `json:"local_devices"`
	CloudTokenSet          bool         `json:"cloud_token_set"`
	CloudTLSVerify         bool         `json:"cloud_tls_verify"`
	MQTTServer             string       `json:"mqtt_server"`
	MQTTPort               int          `json:"mqtt_port"`
	MQTTTopic              string       `json:"mqtt_topic"`
	HASSEnable             bool         `json:"hass_enable"`
	HASSBaseTopic          string       `json:"hass_base_topic"`
	ChargeActiveW          int          `json:"charge_active_w"`
	DischargeActiveW       int          `json:"discharge_active_w"`
	EVCCEnable             bool         `json:"evcc_enable"`
	EVCCBatteryModeTimeout int          `json:"evcc_batterymode_timeout"`
	WebAuth                bool         `json:"web_auth"`
	Language               string       `json:"language"`
	Debug                  bool         `json:"debug"`
}

// DeviceView is one configured local device.
type DeviceView struct {
	SN   string `json:"sn"`
	Host string `json:"host"`
}

// ItemInfo describes one writable item for the UI's control list
// (GET /api/items).
type ItemInfo struct {
	// Key is what POST /api/write takes: the topic leaf for a catalog
	// property or a virtual switch, `evcc/<role>/<key>` for the evcc
	// profile.
	Key   string `json:"key"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Unit  string `json:"unit,omitempty"`
	// Component is the Home Assistant platform: number | select | switch.
	Component string   `json:"component"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
	Step      *float64 `json:"step,omitempty"`
	Options   []Option `json:"options,omitempty"`
	// Confirm asks the UI to confirm a write first: the item changes how
	// the unit operates, or what it persists to flash.
	Confirm bool `json:"confirm,omitempty"`
}

// Option is one select choice: Value is the wire token a write takes, Label
// its localised display.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// The errors a write from the UI can fail with are go-bridgeui's, so the
// web layer hands them through and the server maps them to HTTP status
// codes without knowing the coordinator.
var (
	// ErrUnknownDevice: no device with that serial.
	ErrUnknownDevice = bridgeui.ErrUnknownDevice
	// ErrNotWritable: the key names nothing writable.
	ErrNotWritable = bridgeui.ErrNotWritable
	// ErrInvalidValue: the value does not fit the item.
	ErrInvalidValue = bridgeui.ErrInvalidValue
	// ErrNotReady: the daemon or the device cannot take the write yet.
	ErrNotReady = bridgeui.ErrNotReady
)

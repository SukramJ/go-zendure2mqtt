// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package config holds the daemon's runtime settings.
//
// Values flow: YAML file → env overrides (ZENDURE_* prefix) → defaults →
// validation. The result is a single typed [Config] the rest of the
// daemon reads from.
//
// YAML keys are unprefixed (e.g. MQTT_SERVER, CONNECTION); the matching
// environment override prepends ZENDURE_ (ZENDURE_MQTT_SERVER). Keeping
// the YAML keys prefix-free avoids the awkward ZENDURE_ZENDURE_* doubling
// an in-YAML prefix would create.
package config

import "time"

// Daemon-wide constants.
const (
	// MQTTClientID is the client id the bridge connects to the broker with.
	MQTTClientID = "zendure2mqtt"
	// TopicRoot is the default MQTT topic root (overridable via MQTT_TOPIC).
	TopicRoot = "zendure2mqtt"
	// EnvPrefix is the environment-variable override prefix.
	EnvPrefix = "ZENDURE_"
	// AppDirName is the per-user config directory name under XDG.
	AppDirName = "zendure2mqtt"
	// ConfigFile is the default config file name searched by [Locate].
	ConfigFile = "config.yaml"

	// ConnectionLocal selects the local HTTP transport (zenSDK).
	ConnectionLocal = "local"
	// ConnectionCloud selects the Zendure cloud (REST login + cloud MQTT).
	ConnectionCloud = "cloud"
)

// LocalDevice is one statically configured local device. mDNS discovery
// (a later milestone) populates the same shape automatically.
type LocalDevice struct {
	SN    string `yaml:"SN"`
	Host  string `yaml:"HOST"`
	Model string `yaml:"MODEL"`
}

// Config is the validated daemon configuration. Fields are flat to match
// the YAML keys 1:1.
type Config struct {
	// --- Transport selection ---
	// Connection picks the backend: "local" (HTTP polling of devices on the
	// LAN) or "cloud" (Zendure cloud login + cloud MQTT stream).
	Connection string `yaml:"CONNECTION"`

	// --- Local transport ---
	// Refresh is the local HTTP poll interval in seconds.
	Refresh int `yaml:"REFRESH"`
	// LocalDevices lists the devices to poll (SN + HOST). YAML-only — the
	// env-override path handles scalars, not lists.
	LocalDevices []LocalDevice `yaml:"LOCAL_DEVICES"`

	// --- Cloud transport ---
	// CloudAppToken is the base64 "app token" from the Zendure app. It
	// decodes to "<api_url>.<appKey>" and is required for cloud access; the
	// daemon still starts without it (and stays idle) so a fresh install
	// does not crash-loop.
	CloudAppToken string `yaml:"CLOUD_APP_TOKEN"`
	// CloudTLSVerify enforces standard TLS certificate verification for the
	// cloud broker. It defaults to false because the Zendure cloud presents a
	// non-standard certificate that fails Go's verifier; the connection stays
	// TLS-encrypted regardless. Set true to require a valid certificate.
	CloudTLSVerify bool `yaml:"CLOUD_TLS_VERIFY"`

	// --- MQTT (output broker) ---
	MQTTServer   string `yaml:"MQTT_SERVER"`
	MQTTPort     int    `yaml:"MQTT_PORT"`
	MQTTLogin    string `yaml:"MQTT_LOGIN"`
	MQTTPassword string `yaml:"MQTT_PASSWORD"`
	MQTTTopic    string `yaml:"MQTT_TOPIC"`

	// --- Home Assistant ---
	HASSEnable    bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic string `yaml:"HASS_BASE_TOPIC"`

	// --- Virtual charge/discharge switches ---
	// ChargeActiveValue / DischargeActiveValue are the AC power limits (W)
	// written when the corresponding virtual switch is turned on.
	ChargeActiveValue    int `yaml:"CHARGE_ACTIVE_VALUE"`
	DischargeActiveValue int `yaml:"DISCHARGE_ACTIVE_VALUE"`

	// --- Diagnostic web UI (optional; milestone M2) ---
	WebEnable   bool   `yaml:"WEB_ENABLE"`
	WebBind     string `yaml:"WEB_BIND"`
	WebUser     string `yaml:"WEB_USER"`
	WebPassword string `yaml:"WEB_PASSWORD"`

	// --- Localisation ---
	// Language selects the HA display language: "en" (default) or "de". It
	// localises friendly names; topics and entity_ids stay language-neutral.
	Language string `yaml:"LANGUAGE"`

	// --- Misc ---
	Debug bool `yaml:"DEBUG"`
}

// IsCloud reports whether the cloud transport is selected.
func (c *Config) IsCloud() bool { return c.Connection == ConnectionCloud }

// CloudConfigured reports whether a cloud app token is present.
func (c *Config) CloudConfigured() bool { return c.CloudAppToken != "" }

// RefreshDuration returns the local poll interval as a duration.
func (c *Config) RefreshDuration() time.Duration {
	return time.Duration(c.Refresh) * time.Second
}

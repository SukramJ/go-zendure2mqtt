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
	// TopicRoot is the default instance name, the first level of every topic
	// (overridable via MQTT_TOPIC). mqtt-smarthome 2.0 §3 asks for a short
	// adapter word, openccu-loom ADR 0083 names it.
	TopicRoot = "zendure"
	// LegacyTopicRoot is the default topic root of every release before
	// 0.10.0. It is still the Home Assistant identity root of an instance that
	// leaves MQTT_TOPIC unset — see [Config.IdentityRoot] — and the root the
	// migration sweep clears the old layout under.
	LegacyTopicRoot = "zendure2mqtt"
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
	SN   string `yaml:"SN"`
	Host string `yaml:"HOST"`
	// DeviceName is an optional human-friendly device name. When set it
	// replaces the serial number in the HA device name and (via slugify)
	// seeds the entity_ids, so entities read "<DeviceName> …" instead of
	// "Zendure <SN> …".
	DeviceName string `yaml:"DEVICE_NAME"`
	Model      string `yaml:"MODEL"`
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
	// MQTTTopic is the instance name, `<name>` in `<name>/status/…`. It is
	// the only thing that keeps two instances on one broker apart.
	MQTTTopic string `yaml:"MQTT_TOPIC"`
	// MQTTMaintenance enables the mqtt-smarthome maintenance topics
	// (`<name>/maintenance/…`: log level, restart, stats). A pointer so an
	// omitted key takes the default (on) while an explicit false is kept.
	MQTTMaintenance *bool `yaml:"MQTT_MAINTENANCE"`
	// MQTTStatsInterval is the period of `<name>/maintenance/stats` in
	// seconds; 0 switches the topic off. A pointer for the same reason as
	// MQTTMaintenance: an explicit 0 must not become the default.
	MQTTStatsInterval *int `yaml:"MQTT_STATS_INTERVAL"`

	// identityRoot is the root every Home Assistant unique_id and device
	// identifier is namespaced with. Not a config key: see [Config.IdentityRoot].
	identityRoot string

	// --- Home Assistant ---
	HASSEnable    bool   `yaml:"HASS_ENABLE"`
	HASSBaseTopic string `yaml:"HASS_BASE_TOPIC"`

	// --- Virtual charge/discharge switches ---
	// ChargeActiveValue / DischargeActiveValue are the AC power limits (W)
	// written when the corresponding virtual switch is turned on. Pointers so
	// an explicit 0 (an invalid no-op limit) is distinguishable from "unset"
	// (which takes the 1200 W default) instead of both collapsing to zero.
	ChargeActiveValue    *int `yaml:"CHARGE_ACTIVE_VALUE"`
	DischargeActiveValue *int `yaml:"DISCHARGE_ACTIVE_VALUE"`

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

// IdentityRoot is the root the Home Assistant identities — every unique_id,
// device identifier and discovery node id — are built from.
//
// It is the topic root every release before 0.10.0 used, which is what those
// identities were minted with: the configured MQTT_TOPIC, or
// [LegacyTopicRoot] when the key is unset. 0.10.0 changed the default *name*
// to [TopicRoot] and that moves topics only; Home Assistant has no migration
// for a unique_id, so an identity built from the new default would orphan
// every entity of every installation that never set the key.
//
// A config that did not come through [Load] falls back to MQTTTopic, which is
// the pre-0.10.0 rule.
func (c *Config) IdentityRoot() string {
	if c.identityRoot != "" {
		return c.identityRoot
	}
	return c.MQTTTopic
}

// MaintenanceEnabled reports whether the maintenance topics are on (the
// default).
func (c *Config) MaintenanceEnabled() bool {
	return c.MQTTMaintenance == nil || *c.MQTTMaintenance
}

// StatsIntervalSeconds returns MQTT_STATS_INTERVAL, or the default when unset.
// 0 means off.
func (c *Config) StatsIntervalSeconds() int {
	if c.MQTTStatsInterval == nil {
		return DefaultMQTTStatsInterval
	}
	return *c.MQTTStatsInterval
}

// IsCloud reports whether the cloud transport is selected.
func (c *Config) IsCloud() bool { return c.Connection == ConnectionCloud }

// CloudConfigured reports whether a cloud app token is present.
func (c *Config) CloudConfigured() bool { return c.CloudAppToken != "" }

// RefreshDuration returns the local poll interval as a duration.
func (c *Config) RefreshDuration() time.Duration {
	return time.Duration(c.Refresh) * time.Second
}

// ChargeActiveW returns the charge power limit (W), or the default when unset.
func (c *Config) ChargeActiveW() int {
	if c.ChargeActiveValue == nil {
		return DefaultChargeActiveValue
	}
	return *c.ChargeActiveValue
}

// DischargeActiveW returns the discharge power limit (W), or the default when unset.
func (c *Config) DischargeActiveW() int {
	if c.DischargeActiveValue == nil {
		return DefaultDischargeActiveValue
	}
	return *c.DischargeActiveValue
}

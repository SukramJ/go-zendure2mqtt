// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

// Default values applied when the YAML omits a field. The MQTT server has
// no default and is caught by [Validate] when missing.
const (
	// DefaultConnection selects the local transport out of the box — it is
	// verifiable on the LAN without any cloud credentials.
	DefaultConnection = ConnectionLocal
	// DefaultRefresh is the local poll interval in seconds.
	DefaultRefresh = 15

	DefaultMQTTPort = 1883

	DefaultHASSBaseTopic = "homeassistant"

	// DefaultChargeActiveValue / DefaultDischargeActiveValue are the W limits
	// the virtual switches write when toggled on.
	DefaultChargeActiveValue    = 1200
	DefaultDischargeActiveValue = 1200

	// DefaultWebBind binds the optional UI to localhost only.
	DefaultWebBind = "127.0.0.1:8080"

	// DefaultLanguage is the fallback HA display language.
	DefaultLanguage = "en"
)

// applyDefaults fills in any field left at its zero value with the
// documented default. Connection parameters without a default are left at
// zero and caught by [Validate].
func applyDefaults(c *Config) {
	if c.Connection == "" {
		c.Connection = DefaultConnection
	}
	if c.Refresh == 0 {
		c.Refresh = DefaultRefresh
	}
	if c.MQTTPort == 0 {
		c.MQTTPort = DefaultMQTTPort
	}
	if c.MQTTTopic == "" {
		c.MQTTTopic = TopicRoot
	}
	if c.HASSBaseTopic == "" {
		c.HASSBaseTopic = DefaultHASSBaseTopic
	}
	if c.ChargeActiveValue == 0 {
		c.ChargeActiveValue = DefaultChargeActiveValue
	}
	if c.DischargeActiveValue == 0 {
		c.DischargeActiveValue = DefaultDischargeActiveValue
	}
	if c.WebBind == "" {
		c.WebBind = DefaultWebBind
	}
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
}

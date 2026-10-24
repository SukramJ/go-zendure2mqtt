// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import "path/filepath"

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

	// DefaultMQTTMaintenance switches the maintenance topics on, as
	// mqtt-smarthome 2.0 §7 recommends.
	DefaultMQTTMaintenance = true
	// DefaultMQTTStatsInterval is the `<name>/maintenance/stats` period in
	// seconds.
	DefaultMQTTStatsInterval = 60

	// DefaultChargeActiveValue / DefaultDischargeActiveValue are the W limits
	// the virtual switches write when toggled on.
	DefaultChargeActiveValue    = 1200
	DefaultDischargeActiveValue = 1200

	// DefaultWebBind binds the optional UI to localhost only.
	DefaultWebBind = "127.0.0.1:8080"

	// DefaultLanguage is the fallback HA display language.
	DefaultLanguage = "en"

	// DefaultMatterListen is the Matter operational port on every interface.
	DefaultMatterListen = ":5540"
	// DefaultMatterStoreFile is the node's SQLite file, placed next to the
	// config file (the daemon's only persistent state) when
	// MATTER_STORE_PATH is unset, and in the working directory when the
	// daemon runs from the environment alone.
	DefaultMatterStoreFile = "zendure2mqtt-fabric.db"
	// DefaultMatterPasscode and DefaultMatterDiscriminator are the values
	// go-fabric's reference bridge pairs with. They are public: change the
	// passcode before a bridge leaves a test network.
	DefaultMatterPasscode      = 20202021
	DefaultMatterDiscriminator = 3840
	// DefaultMatterVendorID / DefaultMatterProductID are the CSA test vendor
	// and the product id go-fabric's reference bridge uses with it.
	DefaultMatterVendorID  = 0xFFF1
	DefaultMatterProductID = 0x8001
	// DefaultMatterLabel is the bridge's NodeLabel: the daemon's name.
	DefaultMatterLabel = "zendure2mqtt"
)

// applyDefaults fills in any field left at its zero value with the
// documented default. Connection parameters without a default are left at
// zero and caught by [Validate]. dir is the config file's directory, empty
// for a config that did not come from a file; the Matter store defaults to
// a file there.
func applyDefaults(c *Config, dir string) {
	if c.Connection == "" {
		c.Connection = DefaultConnection
	}
	if c.Refresh == 0 {
		c.Refresh = DefaultRefresh
	}
	if c.MQTTPort == 0 {
		c.MQTTPort = DefaultMQTTPort
	}
	// The identity root is decided before the name is defaulted, because it
	// is the one place that has to know whether the key was set at all.
	if c.MQTTTopic == "" {
		c.identityRoot = LegacyTopicRoot
		c.MQTTTopic = TopicRoot
	} else {
		c.identityRoot = c.MQTTTopic
	}
	if c.MQTTMaintenance == nil {
		v := DefaultMQTTMaintenance
		c.MQTTMaintenance = &v
	}
	if c.MQTTStatsInterval == nil {
		v := DefaultMQTTStatsInterval
		c.MQTTStatsInterval = &v
	}
	if c.HASSBaseTopic == "" {
		c.HASSBaseTopic = DefaultHASSBaseTopic
	}
	if c.ChargeActiveValue == nil {
		v := DefaultChargeActiveValue
		c.ChargeActiveValue = &v
	}
	if c.DischargeActiveValue == nil {
		v := DefaultDischargeActiveValue
		c.DischargeActiveValue = &v
	}
	if c.WebBind == "" {
		c.WebBind = DefaultWebBind
	}
	if c.Language == "" {
		c.Language = DefaultLanguage
	}
	if c.MatterListen == "" {
		c.MatterListen = DefaultMatterListen
	}
	if c.MatterStorePath == "" {
		c.MatterStorePath = filepath.Join(dir, DefaultMatterStoreFile)
	}
	if c.MatterPasscode == 0 {
		c.MatterPasscode = DefaultMatterPasscode
	}
	if c.MatterDiscriminator == nil {
		v := DefaultMatterDiscriminator
		c.MatterDiscriminator = &v
	}
	if c.MatterVendorID == 0 {
		c.MatterVendorID = DefaultMatterVendorID
	}
	if c.MatterProductID == 0 {
		c.MatterProductID = DefaultMatterProductID
	}
	if c.MatterLabel == "" {
		c.MatterLabel = DefaultMatterLabel
	}
}

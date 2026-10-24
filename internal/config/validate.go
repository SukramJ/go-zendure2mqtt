// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SukramJ/go-hamqtt/topic"
)

// ValidationError aggregates every config problem found by [Validate].
type ValidationError struct {
	Issues []string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "config: " + e.Issues[0]
	}
	return fmt.Sprintf("config: %d validation issue(s):\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}

// allowedLanguages is the set of HA display languages with translations.
var allowedLanguages = map[string]bool{"en": true, "de": true}

// Validate checks the post-defaults config and returns a [*ValidationError]
// aggregating every problem.
//
// Cloud / local credentials are intentionally NOT required here: a fresh
// install (or HA add-on) may have them empty, and a fatal error would
// crash-loop before the operator can configure them. The daemon instead
// starts and stays idle (see CloudConfigured / the LOCAL_DEVICES warning).
func Validate(c *Config) error {
	var issues []string
	add := func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	}

	// --- Transport ---
	if c.Connection != ConnectionLocal && c.Connection != ConnectionCloud {
		add("CONNECTION must be %q or %q, got %q", ConnectionLocal, ConnectionCloud, c.Connection)
	}
	if c.Refresh < 5 || c.Refresh > 86400 {
		add("REFRESH must be 5..86400 seconds, got %d", c.Refresh)
	}
	for i, d := range c.LocalDevices {
		if d.SN == "" || d.Host == "" {
			add("LOCAL_DEVICES[%d] requires both SN and HOST", i)
		}
	}

	// --- Virtual switches ---
	// 1..2400: an explicit 0 is rejected (not silently rewritten to the default)
	// because a 0 W limit makes the switch a no-op that can never report ON.
	if v := c.ChargeActiveValue; v != nil && (*v < 1 || *v > 2400) {
		add("CHARGE_ACTIVE_VALUE must be 1..2400 W (omit the key for the %d default), got %d", DefaultChargeActiveValue, *v)
	}
	if v := c.DischargeActiveValue; v != nil && (*v < 1 || *v > 2400) {
		add("DISCHARGE_ACTIVE_VALUE must be 1..2400 W (omit the key for the %d default), got %d", DefaultDischargeActiveValue, *v)
	}

	// --- MQTT ---
	if c.MQTTServer == "" {
		add("MQTT_SERVER is required")
	}
	if c.MQTTPort < 1 || c.MQTTPort > 65535 {
		add("MQTT_PORT must be 1..65535, got %d", c.MQTTPort)
	}
	if c.MQTTTopic == "" {
		add("MQTT_TOPIC is required")
	} else if _, err := topic.NewSmartHomeMultiLevel(c.MQTTTopic); err != nil {
		add("MQTT_TOPIC %v", err)
	}
	if v := c.MQTTStatsInterval; v != nil && *v < 0 {
		add("MQTT_STATS_INTERVAL must be 0 (off) or a number of seconds, got %d", *v)
	}

	// --- Diagnostic web UI ---
	if c.WebEnable {
		if err := validateHostPort(c.WebBind); err != nil {
			add("WEB_BIND %v", err)
		}
		if (c.WebUser == "") != (c.WebPassword == "") {
			add("WEB_USER and WEB_PASSWORD must both be set or both be empty")
		}
	}

	// --- Matter controllers ---
	if c.MatterEnabled {
		validateMatter(c, add)
	}

	// --- Localisation ---
	if !allowedLanguages[c.Language] {
		add("LANGUAGE must be one of [de en], got %q", c.Language)
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

// matterLabelMax is BasicInformation's NodeLabel bound (max 32).
const matterLabelMax = 32

// validateMatter checks the MATTER_* block of an enabled bridge: an address
// the node can bind, a store file it can open, and the commissioning and
// identity values the Matter node refuses.
func validateMatter(c *Config, add func(format string, args ...any)) {
	if err := validateHostPort(c.MatterListen); err != nil {
		add("MATTER_LISTEN %v", err)
	}
	if err := validateStorePath(c.MatterStorePath); err != nil {
		add("MATTER_STORE_PATH %v", err)
	}
	if !validPasscode(c.MatterPasscode) {
		add("MATTER_PASSCODE must be 1..99999998 and none of the trivial passcodes (11111111, 12345678, …), got %d", c.MatterPasscode)
	}
	if d := c.MatterDiscriminatorValue(); d < 0 || d > 0xFFF {
		add("MATTER_DISCRIMINATOR must be 0..4095, got %d", d)
	}
	if c.MatterVendorID < 1 || c.MatterVendorID > 0xFFFF {
		add("MATTER_VENDOR_ID must be 1..65535, got %d", c.MatterVendorID)
	}
	if c.MatterProductID < 1 || c.MatterProductID > 0xFFFF {
		add("MATTER_PRODUCT_ID must be 1..65535, got %d", c.MatterProductID)
	}
	if l := strings.TrimSpace(c.MatterLabel); l == "" || len(c.MatterLabel) > matterLabelMax {
		add("MATTER_LABEL must be 1..%d bytes, got %q", matterLabelMax, c.MatterLabel)
	}
}

// validPasscode is the Matter setup-passcode rule go-fabric's node.Config.Validate
// applies (secure/setup.IsValidSetupPIN, its 99999998 bound): 1 to
// 99999998, and none of the trivial values.
func validPasscode(p int) bool {
	switch p {
	case 11111111, 22222222, 33333333, 44444444, 55555555, 66666666, 77777777, 88888888, 12345678, 87654321:
		return false
	}
	return p >= 1 && p <= 99999998
}

// validateStorePath checks that path names a file the node can create or
// open: not empty, not a directory, in a directory that exists.
func validateStorePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("must name a file")
	}
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return fmt.Errorf("%q is a directory, want a file", path)
	}
	dir := filepath.Dir(path)
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("directory %q of %q: %w", dir, path, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	return nil
}

// validateHostPort checks that s is a "host:port" with a port in 1..65535.
func validateHostPort(s string) error {
	_, port, err := net.SplitHostPort(s)
	if err != nil {
		return fmt.Errorf("must be host:port, got %q: %w", s, err)
	}
	p, perr := strconv.Atoi(port)
	if perr != nil || p < 1 || p > 65535 {
		return fmt.Errorf("port must be 1..65535, got %q", port)
	}
	return nil
}

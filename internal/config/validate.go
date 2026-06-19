// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
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
	if c.ChargeActiveValue < 0 || c.ChargeActiveValue > 2400 {
		add("CHARGE_ACTIVE_VALUE must be 0..2400 W, got %d", c.ChargeActiveValue)
	}
	if c.DischargeActiveValue < 0 || c.DischargeActiveValue > 2400 {
		add("DISCHARGE_ACTIVE_VALUE must be 0..2400 W, got %d", c.DischargeActiveValue)
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

	// --- Localisation ---
	if !allowedLanguages[c.Language] {
		add("LANGUAGE must be one of [de en], got %q", c.Language)
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
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

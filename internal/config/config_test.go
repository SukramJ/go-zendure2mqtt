// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
)

// fakeEnv is a hermetic [config.Env] for tests.
type fakeEnv struct{ vals map[string]string }

func (f fakeEnv) LookupEnv(k string) (string, bool) { v, ok := f.vals[k]; return v, ok }

func (f fakeEnv) Environ() []string {
	out := make([]string, 0, len(f.vals))
	for k, v := range f.vals {
		out = append(out, k+"="+v)
	}
	return out
}

func TestLoadDefaultsAndEnvOverride(t *testing.T) {
	env := fakeEnv{vals: map[string]string{
		"ZENDURE_MQTT_SERVER": "broker.local",
		"ZENDURE_DEBUG":       "true",
	}}
	cfg, err := config.Load(strings.NewReader("CONNECTION: local\nLANGUAGE: de\n"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTTServer != "broker.local" {
		t.Errorf("MQTTServer = %q, want broker.local (env override)", cfg.MQTTServer)
	}
	if !cfg.Debug {
		t.Errorf("Debug = false, want true (env override coercion)")
	}
	if cfg.MQTTPort != config.DefaultMQTTPort {
		t.Errorf("MQTTPort = %d, want default %d", cfg.MQTTPort, config.DefaultMQTTPort)
	}
	if cfg.Refresh != config.DefaultRefresh {
		t.Errorf("Refresh = %d, want default %d", cfg.Refresh, config.DefaultRefresh)
	}
	if cfg.MQTTTopic != config.TopicRoot {
		t.Errorf("MQTTTopic = %q, want default %q", cfg.MQTTTopic, config.TopicRoot)
	}
	if cfg.Language != "de" {
		t.Errorf("Language = %q, want de", cfg.Language)
	}
}

func TestValidateRequiresMQTTServer(t *testing.T) {
	_, err := config.Load(strings.NewReader("CONNECTION: local\n"), fakeEnv{})
	if _, ok := errors.AsType[*config.ValidationError](err); !ok {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
}

func TestValidateRejectsUnknownConnection(t *testing.T) {
	_, err := config.Load(strings.NewReader("CONNECTION: satellite\nMQTT_SERVER: x\n"), fakeEnv{})
	if err == nil {
		t.Fatal("expected validation error for unknown CONNECTION")
	}
}

// TestEnvCoercionPreservesStringCredentials guards against type-blind coercion
// rewriting numeric-looking passwords (0123456 → 123456, 1e5 → 100000).
func TestEnvCoercionPreservesStringCredentials(t *testing.T) {
	env := fakeEnv{vals: map[string]string{
		"ZENDURE_MQTT_SERVER":   "b",
		"ZENDURE_MQTT_PASSWORD": "0123456",
		"ZENDURE_WEB_PASSWORD":  "1e5",
	}}
	cfg, err := config.Load(strings.NewReader("CONNECTION: local\n"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTTPassword != "0123456" {
		t.Errorf("MQTTPassword = %q, want 0123456 (not numerically coerced)", cfg.MQTTPassword)
	}
	if cfg.WebPassword != "1e5" {
		t.Errorf("WebPassword = %q, want 1e5 (not float-coerced)", cfg.WebPassword)
	}
}

// TestExplicitZeroActiveValueRejected ensures an explicit 0 W limit is a
// validation error rather than being silently rewritten to the 1200 default.
func TestExplicitZeroActiveValueRejected(t *testing.T) {
	env := fakeEnv{vals: map[string]string{"ZENDURE_MQTT_SERVER": "b"}}
	_, err := config.Load(strings.NewReader("CONNECTION: local\nCHARGE_ACTIVE_VALUE: 0\n"), env)
	if err == nil {
		t.Fatal("expected validation error for CHARGE_ACTIVE_VALUE: 0")
	}

	// Omitting the key must still take the default.
	cfg, err := config.Load(strings.NewReader("CONNECTION: local\n"), env)
	if err != nil {
		t.Fatalf("Load with default: %v", err)
	}
	if cfg.ChargeActiveW() != config.DefaultChargeActiveValue {
		t.Errorf("ChargeActiveW() = %d, want default %d", cfg.ChargeActiveW(), config.DefaultChargeActiveValue)
	}
}

// TestIdentityRootIsPinnedToThePre010Root pins openccu-loom ADR 0083's guard
// for this bridge: 0.10.0 changed the default topic name to "zendure", and the
// Home Assistant identities must not follow it. Unset, the identity root is
// the old default "zendure2mqtt"; configured, it is the configured value, as
// it always was.
func TestIdentityRootIsPinnedToThePre010Root(t *testing.T) {
	env := fakeEnv{vals: map[string]string{"ZENDURE_MQTT_SERVER": "b"}}

	cfg, err := config.Load(strings.NewReader("CONNECTION: local\n"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MQTTTopic != "zendure" {
		t.Errorf("default name = %q, want zendure", cfg.MQTTTopic)
	}
	if got := cfg.IdentityRoot(); got != "zendure2mqtt" {
		t.Errorf("unset IdentityRoot = %q, want zendure2mqtt", got)
	}

	for _, name := range []string{"zendure2mqtt", "zendure", "garage"} {
		cfg, err := config.Load(strings.NewReader("MQTT_TOPIC: "+name+"\n"), env)
		if err != nil {
			t.Fatalf("Load(%s): %v", name, err)
		}
		if cfg.MQTTTopic != name || cfg.IdentityRoot() != name {
			t.Errorf("configured %q: name %q identity %q, want both %q", name, cfg.MQTTTopic, cfg.IdentityRoot(), name)
		}
	}

	// The env override counts as configured.
	env.vals["ZENDURE_MQTT_TOPIC"] = "keller"
	cfg, err = config.Load(strings.NewReader(""), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IdentityRoot() != "keller" {
		t.Errorf("env-configured IdentityRoot = %q, want keller", cfg.IdentityRoot())
	}
}

func TestMQTTTopicRefusesWildcards(t *testing.T) {
	env := fakeEnv{vals: map[string]string{"ZENDURE_MQTT_SERVER": "b"}}
	for _, name := range []string{"zen+dure", "zen#", "$SYS", "a//b"} {
		if _, err := config.Load(strings.NewReader("MQTT_TOPIC: \""+name+"\"\n"), env); err == nil {
			t.Errorf("MQTT_TOPIC %q accepted", name)
		}
	}
	// A multi-level name is kept, as it always was.
	if _, err := config.Load(strings.NewReader("MQTT_TOPIC: home/zendure\n"), env); err != nil {
		t.Errorf("MQTT_TOPIC home/zendure refused: %v", err)
	}
}

func TestMaintenanceDefaultsAndOverrides(t *testing.T) {
	env := fakeEnv{vals: map[string]string{"ZENDURE_MQTT_SERVER": "b"}}
	cfg, err := config.Load(strings.NewReader(""), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MaintenanceEnabled() || cfg.StatsIntervalSeconds() != config.DefaultMQTTStatsInterval {
		t.Errorf("defaults: maintenance %v interval %d, want true %d",
			cfg.MaintenanceEnabled(), cfg.StatsIntervalSeconds(), config.DefaultMQTTStatsInterval)
	}

	cfg, err = config.Load(strings.NewReader("MQTT_MAINTENANCE: false\nMQTT_STATS_INTERVAL: 0\n"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaintenanceEnabled() || cfg.StatsIntervalSeconds() != 0 {
		t.Errorf("file: maintenance %v interval %d, want false 0 (an explicit 0 is off, not the default)",
			cfg.MaintenanceEnabled(), cfg.StatsIntervalSeconds())
	}

	env.vals["ZENDURE_MQTT_MAINTENANCE"] = "false"
	env.vals["ZENDURE_MQTT_STATS_INTERVAL"] = "300"
	cfg, err = config.Load(strings.NewReader(""), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaintenanceEnabled() || cfg.StatsIntervalSeconds() != 300 {
		t.Errorf("env: maintenance %v interval %d, want false 300", cfg.MaintenanceEnabled(), cfg.StatsIntervalSeconds())
	}

	if _, err := config.Load(strings.NewReader("MQTT_STATS_INTERVAL: -5\n"), fakeEnv{vals: map[string]string{"ZENDURE_MQTT_SERVER": "b"}}); err == nil {
		t.Error("a negative MQTT_STATS_INTERVAL was accepted")
	}
}

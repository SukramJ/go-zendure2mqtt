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
	var verr *config.ValidationError
	if !errors.As(err, &verr) {
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

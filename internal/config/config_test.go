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

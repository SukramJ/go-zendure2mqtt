// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
)

func TestMQTTTLSFromYAML(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(
		"MQTT_SERVER: broker.lan\nMQTT_TLS: true\nMQTT_TLS_CA: /ssl/ca.crt\n"), fakeEnv{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MQTTTLS || cfg.MQTTTLSInsecure || cfg.MQTTTLSCA != "/ssl/ca.crt" {
		t.Errorf("TLS = %v, insecure = %v, CA = %q; want true, false, /ssl/ca.crt",
			cfg.MQTTTLS, cfg.MQTTTLSInsecure, cfg.MQTTTLSCA)
	}
	if cfg.MQTTPort != config.DefaultMQTTTLSPort {
		t.Errorf("MQTTPort = %d, want the TLS default %d", cfg.MQTTPort, config.DefaultMQTTTLSPort)
	}
	if got, want := cfg.MQTTBrokerURL(), "tls://broker.lan:8883"; got != want {
		t.Errorf("MQTTBrokerURL = %q, want %q", got, want)
	}
}

func TestMQTTTLSFromEnv(t *testing.T) {
	env := fakeEnv{vals: map[string]string{
		"ZENDURE_MQTT_SERVER":       "broker.lan",
		"ZENDURE_MQTT_TLS":          "true",
		"ZENDURE_MQTT_TLS_INSECURE": "true",
	}}
	cfg, err := config.Load(strings.NewReader(""), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MQTTTLS || !cfg.MQTTTLSInsecure || cfg.MQTTTLSCA != "" {
		t.Errorf("TLS = %v, insecure = %v, CA = %q; want true, true, empty",
			cfg.MQTTTLS, cfg.MQTTTLSInsecure, cfg.MQTTTLSCA)
	}
	if cfg.MQTTPort != config.DefaultMQTTTLSPort {
		t.Errorf("MQTTPort = %d, want the TLS default %d", cfg.MQTTPort, config.DefaultMQTTTLSPort)
	}
	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], "MQTT_TLS_INSECURE is on") {
		t.Errorf("Warnings = %q, want the insecure warning", w)
	}
}

func TestMQTTTLSExplicitPortWins(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		env        map[string]string
		want       int
	}{
		{"yaml port with TLS", "MQTT_SERVER: b\nMQTT_TLS: true\nMQTT_PORT: 1883\n", nil, 1883},
		{"env port with TLS", "MQTT_SERVER: b\nMQTT_TLS: true\n", map[string]string{"ZENDURE_MQTT_PORT": "18883"}, 18883},
		{"no TLS, no port", "MQTT_SERVER: b\n", nil, config.DefaultMQTTPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(strings.NewReader(tc.yaml), fakeEnv{vals: tc.env})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.MQTTPort != tc.want {
				t.Errorf("MQTTPort = %d, want %d", cfg.MQTTPort, tc.want)
			}
		})
	}
}

func TestMQTTPlainBrokerURL(t *testing.T) {
	cfg, err := config.Load(strings.NewReader("MQTT_SERVER: fd00::1\n"), fakeEnv{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.MQTTBrokerURL(), "tcp://[fd00::1]:1883"; got != want {
		t.Errorf("MQTTBrokerURL = %q, want %q", got, want)
	}
}

func TestMQTTTLSCAWithInsecureRejected(t *testing.T) {
	_, err := config.Load(strings.NewReader(
		"MQTT_SERVER: b\nMQTT_TLS: true\nMQTT_TLS_INSECURE: true\nMQTT_TLS_CA: /ssl/ca.crt\n"), fakeEnv{})
	verr, ok := errors.AsType[*config.ValidationError](err)
	if !ok {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	if !slices.ContainsFunc(verr.Issues, func(s string) bool {
		return strings.Contains(s, "MQTT_TLS_CA and MQTT_TLS_INSECURE")
	}) {
		t.Errorf("issues %q do not name the CA/insecure conflict", verr.Issues)
	}
}

func TestMQTTTLSOptionsWithoutTLSWarn(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"insecure", map[string]string{"ZENDURE_MQTT_TLS_INSECURE": "true"}, "MQTT_TLS_INSECURE has no effect while MQTT_TLS is false"},
		{"ca", map[string]string{"ZENDURE_MQTT_TLS_CA": "/ssl/ca.crt"}, "MQTT_TLS_CA has no effect while MQTT_TLS is false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Load(strings.NewReader("MQTT_SERVER: b\n"), fakeEnv{vals: tc.env})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if w := cfg.Warnings(); !slices.Equal(w, []string{tc.want}) {
				t.Errorf("Warnings = %q, want [%q]", w, tc.want)
			}
			if got := cfg.MQTTBrokerURL(); !strings.HasPrefix(got, "tcp://") {
				t.Errorf("MQTTBrokerURL = %q, want tcp:// while MQTT_TLS is false", got)
			}
		})
	}
}

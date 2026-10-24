// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config_test

import (
	"errors"
	"os"
	"path/filepath"
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

func TestMatterBlockIsOffByDefault(t *testing.T) {
	cfg, err := config.Load(strings.NewReader("MQTT_SERVER: broker\n"), fakeEnv{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MatterEnabled {
		t.Error("MatterEnabled = true, want off by default")
	}
	if cfg.MatterListen != config.DefaultMatterListen || cfg.MatterPasscode != config.DefaultMatterPasscode ||
		cfg.MatterDiscriminatorValue() != config.DefaultMatterDiscriminator ||
		cfg.MatterVendorID != config.DefaultMatterVendorID || cfg.MatterProductID != config.DefaultMatterProductID ||
		cfg.MatterLabel != config.DefaultMatterLabel || cfg.MatterStorePath != config.DefaultMatterStoreFile {
		t.Errorf("Matter defaults = %q %d %d %#x %#x %q %q", cfg.MatterListen, cfg.MatterPasscode,
			cfg.MatterDiscriminatorValue(), cfg.MatterVendorID, cfg.MatterProductID, cfg.MatterLabel, cfg.MatterStorePath)
	}
}

func TestMatterStoreDefaultsNextToTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("MQTT_SERVER: broker\nMATTER_ENABLED: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path, fakeEnv{})
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if want := filepath.Join(dir, config.DefaultMatterStoreFile); cfg.MatterStorePath != want {
		t.Errorf("MatterStorePath = %q, want %q", cfg.MatterStorePath, want)
	}
}

func TestMatterEnvOverrideKeepsAZeroDiscriminator(t *testing.T) {
	env := fakeEnv{vals: map[string]string{
		"ZENDURE_MQTT_SERVER":          "broker",
		"ZENDURE_MATTER_ENABLED":       "true",
		"ZENDURE_MATTER_DISCRIMINATOR": "0",
		"ZENDURE_MATTER_PASSCODE":      "34567890",
		"ZENDURE_MATTER_STORE_PATH":    filepath.Join(t.TempDir(), "fabric.db"),
	}}
	cfg, err := config.Load(strings.NewReader(""), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MatterEnabled || cfg.MatterDiscriminatorValue() != 0 || cfg.MatterPasscode != 34567890 {
		t.Errorf("enabled %v, discriminator %d, passcode %d", cfg.MatterEnabled, cfg.MatterDiscriminatorValue(), cfg.MatterPasscode)
	}
}

func TestMatterEnabledRefusesUnusableValues(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"MATTER_LISTEN":        "MATTER_LISTEN: \"5540\"\n",
		"MATTER_STORE_PATH":    "MATTER_STORE_PATH: " + filepath.Join(dir, "missing", "fabric.db") + "\n",
		"a directory":          "MATTER_STORE_PATH: " + dir + "\n",
		"MATTER_PASSCODE":      "MATTER_PASSCODE: 12345678\n",
		"MATTER_DISCRIMINATOR": "MATTER_DISCRIMINATOR: 4096\n",
		"MATTER_VENDOR_ID":     "MATTER_VENDOR_ID: 70000\n",
		"MATTER_PRODUCT_ID":    "MATTER_PRODUCT_ID: -1\n",
		"MATTER_LABEL":         "MATTER_LABEL: " + strings.Repeat("x", 33) + "\n",
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			doc := "MQTT_SERVER: broker\nMATTER_ENABLED: true\n" + block
			if !strings.Contains(block, "MATTER_STORE_PATH") {
				doc += "MATTER_STORE_PATH: " + filepath.Join(dir, "fabric.db") + "\n"
			}
			_, err := config.Load(strings.NewReader(doc), fakeEnv{})
			v, ok := errors.AsType[*config.ValidationError](err)
			if !ok {
				t.Fatalf("expected *ValidationError, got %v", err)
			}
			key := strings.TrimSuffix(strings.Fields(block)[0], ":")
			if len(v.Issues) != 1 || !strings.HasPrefix(v.Issues[0], key) {
				t.Errorf("issues = %v, want one about %s", v.Issues, key)
			}
		})
	}
	// Disabled, the same values are not judged: nothing changes for an
	// installation that does not use the block.
	if _, err := config.Load(strings.NewReader("MQTT_SERVER: broker\nMATTER_PASSCODE: 12345678\n"), fakeEnv{}); err != nil {
		t.Errorf("disabled block judged: %v", err)
	}
}

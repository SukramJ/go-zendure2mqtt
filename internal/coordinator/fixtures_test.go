// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"strings"
	"testing"

	"github.com/SukramJ/go-mqtt/protocol"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
)

// The instance every fixture in this package runs as: one that never set
// MQTT_TOPIC, which is the upgrade path openccu-loom ADR 0083 has to get
// right. Its topics moved to the 0.10.0 default name; its Home Assistant
// identities stay on the pre-0.10.0 root.
const (
	testName     = config.TopicRoot       // "zendure"
	testIdentity = config.LegacyTopicRoot // "zendure2mqtt"
)

// testConfig loads a config the way the daemon does — through config.Load,
// so the identity root is decided by the loader and not restated here.
func testConfig(tb testing.TB, lang string) *config.Config {
	tb.Helper()
	cfg, err := config.Load(strings.NewReader("MQTT_SERVER: broker.local\nLANGUAGE: "+lang+"\n"), nil)
	if err != nil {
		tb.Fatalf("config.Load: %v", err)
	}
	if cfg.MQTTTopic != testName || cfg.IdentityRoot() != testIdentity {
		tb.Fatalf("default instance = name %q identity %q, want %q / %q", cfg.MQTTTopic, cfg.IdentityRoot(), testName, testIdentity)
	}
	return cfg
}

// testRenderer is the renderer the daemon builds for [testConfig].
func testRenderer(lang string) harender.Renderer {
	return harender.Renderer{Topics: Layout(testName), IdentityRoot: testIdentity, Lang: lang}
}

// mqttMatch reports whether topic matches filter, by the MQTT client's own
// rule.
func mqttMatch(filter, topic string) bool { return protocol.MatchTopic(filter, topic) }

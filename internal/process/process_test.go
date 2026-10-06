// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package process_test

import (
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

const testCatalog = `
entries:
  - property: electricLevel
    topic: electric_level
    group: now
    platform: sensor
  - property: hyperTmp
    topic: temperature
    group: now
    platform: sensor
    offset: 2731
    scale: 10
  - property: acMode
    topic: ac_mode
    group: config
    platform: select
    writable: true
    value_map:
      "1": charge
      "2": discharge
    value_map_de:
      "1": Laden
      "2": Entladen
  - property: socLevel
    topic: soc_level
    platform: sensor
`

func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(strings.NewReader(testCatalog))
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return cat
}

func find(points []process.Point, group, topic string) (process.Point, bool) {
	for _, p := range points {
		if p.Group == group && p.Topic == topic {
			return p, true
		}
	}
	return process.Point{}, false
}

func TestResolveScalingAndValueMap(t *testing.T) {
	cat := loadCatalog(t)
	rep := &model.Report{
		SN: "SF1",
		Properties: map[string]any{
			"electricLevel": float64(75),
			"hyperTmp":      float64(2981), // (2981-2731)/10 = 25.0 °C
			"acMode":        float64(2),    // -> token "discharge"
			"unknownProp":   float64(7),    // no entry -> misc, raw
		},
		PackData: []map[string]any{
			{"sn": "PACK1", "socLevel": float64(80)},
		},
	}

	points := process.Resolve(rep, cat)

	if p, ok := find(points, "now", "temperature"); !ok {
		t.Error("temperature point missing")
	} else if f, _ := p.Value.(float64); f != 25.0 {
		t.Errorf("temperature = %v, want 25.0", p.Value)
	}

	if p, ok := find(points, "config", "ac_mode"); !ok {
		t.Error("ac_mode point missing")
	} else if p.Value != "discharge" {
		t.Errorf("ac_mode = %v, want the token discharge (value_map)", p.Value)
	}

	if p, ok := find(points, process.GroupMisc, "unknownProp"); !ok {
		t.Error("unknown property should fall through to misc group")
	} else if f, _ := p.Value.(float64); f != 7 {
		t.Errorf("unknownProp = %v, want raw 7", p.Value)
	}

	if p, ok := find(points, process.GroupBattery, "soc_level"); !ok {
		t.Error("packData soc_level point missing")
	} else if p.PackSN != "PACK1" {
		t.Errorf("pack point PackSN = %q, want PACK1", p.PackSN)
	}
}

// TestResolvePublishesTokensNotLabels pins openccu-loom ADR 0083's enum rule:
// the wire carries the stable token — the English value_map entry — whatever
// LANGUAGE says, and the German label lives in discovery only. Before 0.10.0
// a German instance published "Laden" here.
func TestResolvePublishesTokensNotLabels(t *testing.T) {
	cat := loadCatalog(t)
	rep := &model.Report{SN: "SF1", Properties: map[string]any{"acMode": float64(1), "hyperTmp": float64(9)}}

	points := process.Resolve(rep, cat)
	p, ok := find(points, "config", "ac_mode")
	if !ok {
		t.Fatal("ac_mode point missing")
	}
	if p.Value != "charge" {
		t.Errorf("ac_mode = %v, want the token charge", p.Value)
	}

	// The token maps back to the raw code for writes, in any case, and the
	// labels stay available for discovery.
	entry, _ := cat.ByTopic("ac_mode")
	if code, ok := entry.CodeForToken("CHARGE"); !ok || code != "1" {
		t.Errorf("CodeForToken(CHARGE) = %q,%v, want 1,true", code, ok)
	}
	if _, ok := entry.CodeForToken("Laden"); ok {
		t.Error("CodeForToken accepted a label; labels are matched by the set path, not as tokens")
	}
	if got := entry.Options("de"); len(got) != 2 || got[0] != "Laden" || got[1] != "Entladen" {
		t.Errorf("Options(de) = %v, want [Laden Entladen]", got)
	}

	// A code the value map does not know keeps its number, as before.
	rep.Properties["acMode"] = float64(9)
	if p, _ := find(process.Resolve(rep, cat), "config", "ac_mode"); p.Value != float64(9) {
		t.Errorf("unmapped code = %v, want the raw 9", p.Value)
	}
}

func TestItemPath(t *testing.T) {
	p := process.Point{Group: "config", Topic: "ac_mode"}
	if got := strings.Join(process.Item("SF1", p), "/"); got != "SF1/config/ac_mode" {
		t.Errorf("Item = %q", got)
	}
	pack := process.Point{Group: process.GroupBattery, PackSN: "PACK1", Topic: "soc_level"}
	if got := strings.Join(process.Item("SF1", pack), "/"); got != "SF1/battery/PACK1/soc_level" {
		t.Errorf("pack Item = %q", got)
	}
}

// TestResolveSanitizesHostileTopicSegments checks that a device-supplied
// unmapped property key with MQTT wildcards/levels is neutralized, and that a
// battery pack with an implausible serial is dropped entirely.
func TestResolveSanitizesHostileTopicSegments(t *testing.T) {
	cat := loadCatalog(t)
	rep := &model.Report{
		Properties: map[string]any{"evil/#key": float64(1)},
		PackData: []map[string]any{
			{"sn": "PACK1", "socLevel": float64(80)},
			{"sn": "bad/sn+#", "socLevel": float64(50)},
			{"sn": "", "socLevel": float64(50)},
		},
	}
	points := process.Resolve(rep, cat)

	for _, p := range points {
		if strings.ContainsAny(p.Topic, "/+#") {
			t.Errorf("point topic %q still contains an MQTT wildcard/level", p.Topic)
		}
		if strings.ContainsAny(p.PackSN, "/+#") {
			t.Errorf("pack SN %q still contains an MQTT wildcard/level", p.PackSN)
		}
	}
	// The valid pack survives; the two malformed ones are dropped.
	packs := 0
	for _, p := range points {
		if p.PackSN != "" {
			packs++
		}
	}
	if packs != 1 {
		t.Errorf("battery points = %d, want 1 (malformed pack serials dropped)", packs)
	}
}

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
			"acMode":        float64(2),    // -> "output"
			"unknownProp":   float64(7),    // no entry -> misc, raw
		},
		PackData: []map[string]any{
			{"sn": "PACK1", "socLevel": float64(80)},
		},
	}

	points := process.Resolve(rep, cat, "en")

	if p, ok := find(points, "now", "temperature"); !ok {
		t.Error("temperature point missing")
	} else if f, _ := p.Value.(float64); f != 25.0 {
		t.Errorf("temperature = %v, want 25.0", p.Value)
	}

	if p, ok := find(points, "config", "ac_mode"); !ok {
		t.Error("ac_mode point missing")
	} else if p.Value != "discharge" {
		t.Errorf("ac_mode = %v, want discharge (value_map)", p.Value)
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

func TestResolveGermanSelectLabel(t *testing.T) {
	cat := loadCatalog(t)
	rep := &model.Report{SN: "SF1", Properties: map[string]any{"acMode": float64(1)}}

	points := process.Resolve(rep, cat, "de")
	p, ok := find(points, "config", "ac_mode")
	if !ok {
		t.Fatal("ac_mode point missing")
	}
	if p.Value != "Laden" {
		t.Errorf("ac_mode (de) = %v, want Laden", p.Value)
	}

	// The German label must reverse-map back to the raw code for writes.
	entry, _ := cat.ByTopic("ac_mode")
	if code, ok := entry.CodeForLabel("Laden"); !ok || code != "1" {
		t.Errorf("CodeForLabel(Laden) = %q,%v, want 1,true", code, ok)
	}
	if got := entry.Options("de"); len(got) != 2 || got[0] != "Laden" || got[1] != "Entladen" {
		t.Errorf("Options(de) = %v, want [Laden Entladen]", got)
	}
}

func TestStateAndCommandTopics(t *testing.T) {
	p := process.Point{Group: "config", Topic: "ac_mode"}
	if got := process.StateTopic("zendure", "SF1", p); got != "zendure/SF1/config/ac_mode/state" {
		t.Errorf("StateTopic = %q", got)
	}
	if got := process.CommandTopic("zendure", "SF1", p); got != "zendure/SF1/config/ac_mode/set" {
		t.Errorf("CommandTopic = %q", got)
	}
	pack := process.Point{Group: process.GroupBattery, PackSN: "PACK1", Topic: "soc_level"}
	if got := process.StateTopic("zendure", "SF1", pack); got != "zendure/SF1/battery/PACK1/soc_level/state" {
		t.Errorf("pack StateTopic = %q", got)
	}
}

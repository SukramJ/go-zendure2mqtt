// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package evcc_test

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/evcc"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
)

// TestDocsSnippetMatchesTheProfile is the contract the shared profile asks
// for: every topic the evcc.yaml in docs/evcc.md names is one this bridge
// publishes (status) or routes (set), every getter reads `.val`, every setter
// writes the command of its own name — also inside a watchdog — and every
// command is documented.
func TestDocsSnippetMatchesTheProfile(t *testing.T) {
	t.Parallel()
	md, err := os.ReadFile("../../docs/evcc.md")
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout may carry CRLF line endings.
	md = bytes.ReplaceAll(md, []byte("\r\n"), []byte("\n"))
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllSubmatch(md, -1)
	if len(blocks) != 1 {
		t.Fatalf("docs/evcc.md has %d yaml blocks, want 1", len(blocks))
	}
	var doc struct {
		Meters []map[string]any `yaml:"meters"`
	}
	if err := yaml.Unmarshal(blocks[0][1], &doc); err != nil {
		t.Fatalf("snippet is not YAML: %v", err)
	}
	if len(doc.Meters) == 0 {
		t.Fatal("snippet defines no meters")
	}

	layout, err := harender.NewLayout(config.TopicRoot)
	if err != nil {
		t.Fatal(err)
	}
	const sn = "HOA1NAH9KT2N0J"
	status := map[string]bool{}
	for _, it := range evcc.StatusItems() {
		status[layout.EVCCStatus(sn, it.Role, it.Key)] = true
	}
	set := map[string]string{}
	for _, it := range evcc.Commands() {
		set[layout.EVCCSet(sn, it.Role, it.Key)] = it.Key
	}

	var setters []string
	var walk func(field string, v any)
	check := func(field string, src map[string]any) {
		topic, _ := src["topic"].(string)
		switch {
		case status[topic]:
			if src["jq"] != ".val" {
				t.Errorf("%s reads %s without jq: .val", field, topic)
			}
			if _, ok := src["timeout"]; ok {
				t.Errorf("%s has a timeout; the bridge publishes on change only", field)
			}
		case set[topic] != "":
			if _, ok := src["jq"]; ok {
				t.Errorf("%s writes %s with a jq filter", field, topic)
			}
			if set[topic] != field {
				t.Errorf("setter %s writes %s, want the command of the same name", field, topic)
			}
			setters = append(setters, field)
		default:
			t.Errorf("%s: %q is neither a published status nor a routed command topic", field, topic)
		}
	}
	walk = func(field string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if x["source"] == "mqtt" {
				check(field, x)
				return
			}
			for _, e := range x {
				walk(field, e)
			}
		case []any:
			for _, e := range x {
				walk(field, e)
			}
		}
	}
	for _, m := range doc.Meters {
		if m["type"] != "custom" {
			t.Errorf("meter %v: type %v, want custom", m["name"], m["type"])
		}
		for field, v := range m {
			walk(field, v)
		}
	}
	slices.Sort(setters)
	want := make([]string, 0, len(evcc.Commands()))
	for _, it := range evcc.Commands() {
		want = append(want, it.Key)
	}
	slices.Sort(want)
	if !slices.Equal(setters, want) {
		t.Errorf("snippet setters %v, want every command %v", setters, want)
	}
	if !strings.Contains(string(md), "EVCC_BATTERYMODE_TIMEOUT") {
		t.Error("docs/evcc.md does not explain EVCC_BATTERYMODE_TIMEOUT")
	}
}

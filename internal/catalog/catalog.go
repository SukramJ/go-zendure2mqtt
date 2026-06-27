// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package catalog is the declarative property map: it turns raw Zendure
// property keys (electricLevel, acMode, inputLimit, …) into MQTT topics
// and Home Assistant entities, with units, scaling and value mappings.
//
// The catalog is loaded from zendure.yaml so adding a property or a new
// product is a data change, not a code change — the same pattern as
// go-mtec2mqtt's register catalog.
package catalog

import (
	"sort"
	"strconv"
)

// Entry describes how a single Zendure property is surfaced.
type Entry struct {
	// Property is the raw key as it appears in the device report
	// (camelCase, e.g. "electricLevel").
	Property string `yaml:"property"`
	// Topic is the MQTT topic leaf (snake_case, e.g. "electric_level").
	// Defaults to Property when empty.
	Topic string `yaml:"topic"`
	// Group buckets the property into a topic sub-path: now | battery |
	// config | static | misc.
	Group string `yaml:"group"`
	// Platform is the Home Assistant platform: sensor | binary_sensor |
	// number | select | switch. Empty disables HA discovery for the entry.
	Platform string `yaml:"platform"`
	// DeviceClass / Unit annotate the HA sensor.
	DeviceClass string `yaml:"device_class"`
	Unit        string `yaml:"unit"`
	// Scale divides the raw value before publishing (e.g. 100 for 0.01V).
	// Zero means "no scaling".
	Scale float64 `yaml:"scale"`
	// Offset is subtracted from the raw value before Scale is applied
	// (e.g. 2731 for the 0.1 K → °C temperature conversion).
	Offset float64 `yaml:"offset"`
	// Writable marks the property as settable (HA gets a command topic).
	Writable bool `yaml:"writable"`
	// Min / Max / Step bound a writable number entity.
	Min  *float64 `yaml:"min"`
	Max  *float64 `yaml:"max"`
	Step *float64 `yaml:"step"`
	// ValueMap maps a raw integer code to an English label, used for select
	// entities (e.g. {1: charge, 2: discharge} for acMode). Reversed on write.
	ValueMap map[string]string `yaml:"value_map"`
	// ValueMapDE is the German label set for the same codes. Falls back to
	// ValueMap when a code is missing. Like daikin2mqtt, only the select
	// option *labels* are localised; the raw code sent to the device, the
	// topics and the entity ids stay language-independent.
	ValueMapDE map[string]string `yaml:"value_map_de"`
	// Name / NameDE are the friendly names (English / German).
	Name   string `yaml:"name"`
	NameDE string `yaml:"name_de"`
}

// TopicLeaf returns Topic, falling back to Property.
func (e Entry) TopicLeaf() string {
	if e.Topic != "" {
		return e.Topic
	}
	return e.Property
}

// FriendlyName returns the localised name for lang ("de" or anything else
// → English), falling back across name_de → name → property.
func (e Entry) FriendlyName(lang string) string {
	if lang == "de" && e.NameDE != "" {
		return e.NameDE
	}
	if e.Name != "" {
		return e.Name
	}
	return e.Property
}

// Label returns the localised select label for a raw value-map code:
// German when lang=="de" and a translation exists, otherwise English.
func (e Entry) Label(code, lang string) (string, bool) {
	if lang == "de" {
		if l, ok := e.ValueMapDE[code]; ok {
			return l, true
		}
	}
	l, ok := e.ValueMap[code]
	return l, ok
}

// Options returns the select option labels in stable (ascending code)
// order, localised to lang.
func (e Entry) Options(lang string) []string {
	out := make([]string, 0, len(e.ValueMap))
	for _, c := range e.sortedCodes() {
		if l, ok := e.Label(c, lang); ok {
			out = append(out, l)
		}
	}
	return out
}

// CodeForLabel reverse-maps a select label (in either language) back to its
// raw code, so an inbound command resolves regardless of LANGUAGE.
func (e Entry) CodeForLabel(label string) (string, bool) {
	for c, l := range e.ValueMap {
		if l == label {
			return c, true
		}
	}
	for c, l := range e.ValueMapDE {
		if l == label {
			return c, true
		}
	}
	return "", false
}

// sortedCodes returns the value-map codes in ascending numeric order
// (lexical fallback for non-numeric codes) for deterministic option lists.
func (e Entry) sortedCodes() []string {
	codes := make([]string, 0, len(e.ValueMap))
	for c := range e.ValueMap {
		codes = append(codes, c)
	}
	sort.Slice(codes, func(i, j int) bool {
		ai, aerr := strconv.Atoi(codes[i])
		bi, berr := strconv.Atoi(codes[j])
		if aerr == nil && berr == nil {
			return ai < bi
		}
		return codes[i] < codes[j]
	})
	return codes
}

// Catalog is the loaded, indexed property map.
type Catalog struct {
	entries []Entry
	byProp  map[string]Entry
	byTopic map[string]Entry
}

// newCatalog indexes entries by property and topic leaf.
func newCatalog(entries []Entry) *Catalog {
	c := &Catalog{
		entries: entries,
		byProp:  make(map[string]Entry, len(entries)),
		byTopic: make(map[string]Entry, len(entries)),
	}
	for i := range entries {
		e := &entries[i]
		c.byProp[e.Property] = *e
		c.byTopic[e.TopicLeaf()] = *e
	}
	return c
}

// Entries returns all catalog entries in declaration order.
func (c *Catalog) Entries() []Entry { return c.entries }

// ByProperty looks up an entry by its raw property key.
func (c *Catalog) ByProperty(property string) (Entry, bool) {
	e, ok := c.byProp[property]
	return e, ok
}

// ByTopic looks up an entry by its MQTT topic leaf (used on the write path
// to map an incoming .../set topic back to a property).
func (c *Catalog) ByTopic(topic string) (Entry, bool) {
	e, ok := c.byTopic[topic]
	return e, ok
}

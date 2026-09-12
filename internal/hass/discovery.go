// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass builds Home Assistant MQTT auto-discovery payloads.
//
// For every catalogued point it publishes a retained config message under
// <base>/<platform>/zendure_<sn>_<topic>/config so Home Assistant creates
// the matching entity (sensor/number/select/switch) and wires it to the
// bridge's state and command topics.
package hass

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Discovery publishes Home Assistant discovery configs (idempotently: each
// unique_id is sent once per process lifetime).
type Discovery struct {
	base   string // HA discovery root, e.g. "homeassistant"
	root   string // bridge MQTT topic root, e.g. "zendure"
	lang   string
	pub    mqtt.Publisher
	logger *slog.Logger

	mu   sync.Mutex
	sent map[string]bool
}

// New constructs a Discovery publisher.
func New(base, root, lang string, pub mqtt.Publisher, logger *slog.Logger) *Discovery {
	if logger == nil {
		logger = slog.Default()
	}
	return &Discovery{base: base, root: root, lang: lang, pub: pub, logger: logger, sent: map[string]bool{}}
}

// Publish emits discovery configs for every catalogued, HA-eligible point and
// returns the set of config topics that make up the device's current entity set
// — whether freshly published this call or already sent earlier in the process
// lifetime. The caller reconciles this set against the broker's retained
// configs to clear orphans (see the coordinator's reconcileOrphans). Points
// without a catalog entry or platform are skipped.
func (d *Discovery) Publish(ctx context.Context, dev source.Device, report *model.Report, points []process.Point) (published map[string]bool) {
	published = make(map[string]bool, len(points))
	for _, p := range points {
		if p.Entry == nil || p.Entry.Platform == "" {
			continue
		}
		uniqueID := d.uniqueID(dev.SN, p)
		// The config topic belongs to the device's current set whether or not we
		// (re)send it below, so record it before the already-sent guard: a
		// steady-state publish (everything already sent) must still report the
		// full set so reconciliation does not treat live entities as orphans.
		published[d.configTopic(p.Entry.Platform, uniqueID)] = true
		d.mu.Lock()
		already := d.sent[uniqueID]
		d.mu.Unlock()
		if already {
			continue
		}
		topic, payload, err := d.config(dev, report, p, uniqueID)
		if err != nil {
			d.logger.Warn("hass.config_failed", slog.String("id", uniqueID), slog.String("err", err.Error()))
			continue
		}
		if err := d.pub.Publish(ctx, topic, payload, mqtt.QoS0, true); err != nil {
			d.logger.Warn("hass.publish_failed", slog.String("topic", topic), slog.String("err", err.Error()))
			continue
		}
		d.mu.Lock()
		d.sent[uniqueID] = true
		d.mu.Unlock()
	}
	return published
}

// Forget drops the given discovery config topics from the already-sent set so
// their entities are republished on the next matching point. The coordinator
// calls this right after clearing retained orphan configs: if a "orphan" was in
// fact still live (e.g. a transiently shrunken report), the next report restores
// it instead of leaving it deleted for the rest of the process lifetime.
func (d *Discovery) Forget(configTopics []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, topic := range configTopics {
		// configTopic() is <base>/<platform>/<uniqueID>/config; recover the id.
		parts := strings.Split(topic, "/")
		if len(parts) < 2 {
			continue
		}
		delete(d.sent, parts[len(parts)-2])
	}
}

// uniqueID derives a stable, broker-wide-unique entity id.
func (d *Discovery) uniqueID(sn string, p process.Point) string {
	return UniqueID(d.root, sn, p.PackSN, p.Topic)
}

// UniqueID is the formula behind every entity's unique_id, exported so a
// second renderer can be proved to produce the same string rather than a
// second copy of the same formula.
//
// Home Assistant keys its entity registry on this and has no migration path
// for it, so the string is frozen: ADR 0070 sanctions a re-key for the
// bridges and this one declines it (ADR 0070 phase 5, step 2). A parallel
// rendering path that re-derived the formula instead of calling this could
// drift from it without any pin noticing, because the pins compare the two
// against each other and would simply agree on a wrong answer.
//
// Note that root is config.MQTTTopic and therefore operator-configurable,
// which is F2 of the phase-5 measurement: changing it re-keys every entity.
// That defect is preserved here deliberately — this function is a record of
// what is published today, not of what should be.
func UniqueID(root, sn, packSN, topicLeaf string) string {
	if packSN != "" {
		return fmt.Sprintf("%s_%s_pack_%s_%s", root, sn, packSN, topicLeaf)
	}
	return fmt.Sprintf("%s_%s_%s", root, sn, topicLeaf)
}

// config builds the (topic, payload) for one entity.
func (d *Discovery) config(dev source.Device, report *model.Report, p process.Point, uniqueID string) (topic string, payload []byte, err error) {
	e := p.Entry
	// default_entity_id seeds an English, language-independent entity_id (device
	// name + English topic) so entity_ids stay stable while the localized display
	// name changes. The former object_id key is NOT published: HA's MQTT discovery
	// schemas are extra=REMOVE_EXTRA and no MQTT platform declares object_id any
	// more (0 of 32 as of HA 2026.9), so it was silently dropped on arrival.
	// unique_id is deliberately independent of the seed, so the entity identity
	// never changes with the name.
	seed := EntityObjectID(d.deviceName(dev, p), p.Topic)
	cfg := map[string]any{
		"name":              e.FriendlyName(d.lang),
		"unique_id":         uniqueID,
		"default_entity_id": e.Platform + "." + seed,
		"state_topic":       process.StateTopic(d.root, dev.SN, p),
		// Availability ties every entity to the bridge status (LWT) topic so HA
		// shows them unavailable when the bridge is down.
		"availability_topic":    d.root + "/bridge/status",
		"payload_available":     "online",
		"payload_not_available": "offline",
		"device":                d.deviceBlock(dev, report, p),
	}
	if e.DeviceClass != "" {
		cfg["device_class"] = e.DeviceClass
	}
	if e.Unit != "" {
		cfg["unit_of_measurement"] = e.Unit
	}
	if e.Writable {
		cfg["command_topic"] = process.CommandTopic(d.root, dev.SN, p)
	}
	switch e.Platform {
	case "sensor":
		if e.DeviceClass == "energy" {
			cfg["state_class"] = "total_increasing"
		} else if e.Unit != "" {
			cfg["state_class"] = "measurement"
		}
	case "number":
		if e.Min != nil {
			cfg["min"] = *e.Min
		}
		if e.Max != nil {
			cfg["max"] = *e.Max
		}
		if e.Step != nil {
			cfg["step"] = *e.Step
		}
	case "select":
		cfg["options"] = e.Options(d.lang)
	case "switch":
		cfg["payload_on"] = "1"
		cfg["payload_off"] = "0"
	}

	payload, err = json.Marshal(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("hass: marshal config: %w", err)
	}
	return d.configTopic(e.Platform, uniqueID), payload, nil
}

// configTopic is the retained HA discovery config topic for an entity:
// <base>/<platform>/<uniqueID>/config.
func (d *Discovery) configTopic(platform, uniqueID string) string {
	return fmt.Sprintf("%s/%s/%s/config", d.base, platform, uniqueID)
}

// deviceBlock is the HA "device" registry block for a point. The main unit
// is one device; each battery pack is split out into its own sub-device,
// linked back to the main unit via `via_device` so Home Assistant nests
// them under the SolarFlow instead of flattening every pack value onto it.
func (d *Discovery) deviceBlock(dev source.Device, report *model.Report, p process.Point) map[string]any {
	mainID := d.root + "_" + dev.SN
	if p.PackSN == "" {
		blk := map[string]any{
			"identifiers":   []string{mainID},
			"name":          d.deviceName(dev, p),
			"manufacturer":  "Zendure",
			"model":         dev.Model,
			"serial_number": dev.SN,
		}
		if report != nil && report.Product != "" {
			blk["model_id"] = report.Product // e.g. "solarFlow2400AC"
		}
		if dev.Address != "" {
			blk["configuration_url"] = "http://" + dev.Address // device's local HTTP API host
		}
		return blk
	}
	blk := map[string]any{
		"identifiers":   []string{mainID + "_pack_" + p.PackSN},
		"name":          d.deviceName(dev, p),
		"manufacturer":  "Zendure",
		"model":         "Battery Pack",
		"serial_number": p.PackSN,
		"via_device":    mainID,
	}
	if sw := PackSoftVersion(report, p.PackSN); sw != "" {
		blk["sw_version"] = sw
	}
	return blk
}

// PackSoftVersion returns a battery pack's firmware version
// (packData.softVersion) as a string, or "" if the report does not carry it.
// It is the pack sub-device's only source of sw_version, and it is exported
// for the parallel rendering path of ADR 0070 phase 5, step 2.
func PackSoftVersion(report *model.Report, packSN string) string {
	if report == nil {
		return ""
	}
	for _, pd := range report.PackData {
		if sn, _ := pd["sn"].(string); sn == packSN {
			if v, ok := pd["softVersion"]; ok {
				return numString(v)
			}
			return ""
		}
	}
	return ""
}

// numString renders a JSON-decoded number without scientific notation
// (float64 4109 → "4109"), leaving non-numeric values to default formatting.
func numString(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// deviceName is the HA device friendly name: the unit, or a per-pack
// sub-device name. Language-independent (used to seed the entity_id).
// A configured DeviceName replaces the "Zendure <SN>" default; when unset
// the serial-number default applies.
func (d *Discovery) deviceName(dev source.Device, p process.Point) string {
	return DeviceName(dev, p.PackSN)
}

// DeviceName is the HA device friendly name for a unit or one of its battery
// packs, exported for the same reason as [UniqueID]: it seeds
// default_entity_id through [EntityObjectID], so a second renderer has to
// call it rather than restate it.
func DeviceName(dev source.Device, packSN string) string {
	base := "Zendure " + dev.SN
	if dev.DeviceName != "" {
		base = dev.DeviceName
	}
	if packSN == "" {
		return base
	}
	return base + " Pack " + packSN
}

// EntityObjectID builds an English, language-independent entity object id
// from the device name and the English topic, e.g.
// "zendure_hoa1_electric_level". It seeds default_entity_id so entity_ids
// stay stable while the display name is localized.
//
// Exported for the parallel rendering path of ADR 0070 phase 5, step 2, and
// exported rather than reimplemented there on purpose: two of the strings
// this produces are pinned *defects* — a pack-serial hyphen-vs-underscore
// collision that gives two entities one default_entity_id (F8), and a
// dropped "é" where the shared library transliterates — and a defect is only
// pinned if the thing under test is the function that has it. A copy would
// let one of the two paths be fixed while the other kept the pin green.
func EntityObjectID(deviceName, topic string) string {
	return collapseTokens(slugify(deviceName + "_" + topic))
}

// umlautReplacer transliterates German umlauts to match HA's slugify.
var umlautReplacer = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss")

// slugify lowercases, transliterates umlauts and reduces any run of
// non-alphanumeric characters to a single underscore.
func slugify(s string) string {
	s = umlautReplacer.Replace(strings.ToLower(s))
	var b strings.Builder
	pendingSep := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		} else {
			pendingSep = true
		}
	}
	return b.String()
}

// collapseTokens drops adjacent duplicate underscore-separated tokens.
func collapseTokens(s string) string {
	parts := strings.Split(s, "_")
	out := parts[:0]
	for _, p := range parts {
		if p == "" || (len(out) > 0 && out[len(out)-1] == p) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "_")
}

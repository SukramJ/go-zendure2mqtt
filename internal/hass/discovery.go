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

// uniqueID derives a stable, broker-wide-unique entity id.
func (d *Discovery) uniqueID(sn string, p process.Point) string {
	if p.PackSN != "" {
		return fmt.Sprintf("%s_%s_pack_%s_%s", d.root, sn, p.PackSN, p.Topic)
	}
	return fmt.Sprintf("%s_%s_%s", d.root, sn, p.Topic)
}

// config builds the (topic, payload) for one entity.
func (d *Discovery) config(dev source.Device, report *model.Report, p process.Point, uniqueID string) (topic string, payload []byte, err error) {
	e := p.Entry
	// object_id and default_entity_id both seed an English, language-independent
	// entity_id (device name + English topic) so entity_ids stay stable while the
	// localized display name changes. HA deprecated object_id in favour of
	// default_entity_id, but current releases still honour object_id reliably
	// whereas default_entity_id is not yet consistently applied (a localized name
	// otherwise leaks into the entity_id) — so we publish BOTH, matching the
	// go-mtec2mqtt twin: object_id keeps today's HA correct, default_entity_id
	// keeps future HA correct. unique_id is deliberately independent of the seed,
	// so the entity identity never changes with the name.
	seed := entityObjectID(d.deviceName(dev, p), p.Topic)
	cfg := map[string]any{
		"name":              e.FriendlyName(d.lang),
		"unique_id":         uniqueID,
		"object_id":         seed,
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
	if sw := packSoftVersion(report, p.PackSN); sw != "" {
		blk["sw_version"] = sw
	}
	return blk
}

// packSoftVersion returns a battery pack's firmware version (packData.softVersion)
// as a string, or "" if the report does not carry it.
func packSoftVersion(report *model.Report, packSN string) string {
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
	base := "Zendure " + dev.SN
	if dev.DeviceName != "" {
		base = dev.DeviceName
	}
	if p.PackSN == "" {
		return base
	}
	return base + " Pack " + p.PackSN
}

// entityObjectID builds an English, language-independent entity object id
// from the device name and the English topic, e.g.
// "zendure_hoa1_electric_level". It seeds default_entity_id so entity_ids
// stay stable while the display name is localized.
func entityObjectID(deviceName, topic string) string {
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

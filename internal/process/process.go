// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package process turns a raw [model.Report] into a flat list of
// publishable [Point]s: catalog lookup, scaling, value mapping and
// packData expansion all happen here, so the coordinator only has to
// publish what it is handed.
package process

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// maxPackSNLen bounds a device-supplied battery pack serial. A pack SN seeds
// its own MQTT topic level and HA sub-device, so an implausibly long or
// malformed one is dropped rather than published.
const maxPackSNLen = 64

// Group names used when the catalog does not classify a property.
const (
	// GroupMisc collects device properties without a catalog entry.
	GroupMisc = "misc"
	// GroupBattery collects per-pack values expanded from packData.
	GroupBattery = "battery"
)

// Point is one resolved value ready to be published.
type Point struct {
	// Group is the topic sub-path (now | config | static | battery | misc).
	Group string
	// Topic is the topic leaf (catalog topic or the raw property name).
	Topic string
	// PackSN, when set, scopes the point to a battery pack sub-device.
	PackSN string
	// Value is the processed value (scaled / mapped) to publish.
	Value any
	// Entry is the catalog entry, or nil for unmapped raw values.
	Entry *catalog.Entry
}

// Resolve flattens rep into points using cat. Select labels are emitted in
// lang ("de" → German option labels, matching HA discovery). Properties
// without a catalog entry are still published (group "misc", raw value) so
// nothing is lost while the catalog is filled in.
func Resolve(rep *model.Report, cat *catalog.Catalog, lang string) []Point {
	points := make([]Point, 0, len(rep.Properties))

	for key, raw := range rep.Properties {
		if entry, ok := cat.ByProperty(key); ok {
			e := entry
			points = append(points, Point{
				Group: groupOrDefault(e.Group, GroupMisc),
				Topic: e.TopicLeaf(),
				Value: applyEntry(e, raw, lang),
				Entry: &e,
			})
			continue
		}
		points = append(points, Point{Group: GroupMisc, Topic: sanitizeSegment(key), Value: raw})
	}

	// packData[] → per-pack sub-entities under the battery group.
	for _, pack := range rep.PackData {
		packSN, ok := pack["sn"].(string)
		if !ok || !validPackSN(packSN) {
			continue // implausible/malformed serial — would corrupt topics and leak sub-devices
		}
		for key, raw := range pack {
			if key == "sn" {
				continue
			}
			value := raw
			var entryPtr *catalog.Entry
			topic := sanitizeSegment(key)
			if entry, ok := cat.ByProperty(key); ok {
				e := entry
				value = applyEntry(e, raw, lang)
				entryPtr = &e
				topic = e.TopicLeaf()
			}
			points = append(points, Point{
				Group:  GroupBattery,
				Topic:  topic,
				PackSN: packSN,
				Value:  value,
				Entry:  entryPtr,
			})
		}
	}

	return points
}

// sanitizeSegment makes a device-supplied string safe as an MQTT topic level:
// characters that would inject a level ('/') or a wildcard ('+', '#'), plus
// NUL and invalid UTF-8, are replaced with '_'. An MQTT PUBLISH to a wildcard
// or non-UTF-8 topic is a protocol violation the broker (and go-mqtt's own
// validation) rejects, and a rejected publish would count against the output
// circuit breaker — one hostile report could otherwise mute the whole bridge.
func sanitizeSegment(s string) string {
	if s == "" {
		return "_"
	}
	if !strings.ContainsAny(s, "/+#\x00") && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '+' || r == '#' || r == 0 || r == utf8.RuneError:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validPackSN reports whether a device-supplied battery pack serial is
// plausible: non-empty, within a sane length, and limited to characters that
// are safe in an MQTT topic level and an HA unique_id. Rejecting outliers
// bounds the otherwise-unbounded set of pack sub-devices a rogue device could
// mint (each new serial permanently grows discovery state and retained topics).
func validPackSN(sn string) bool {
	if sn == "" || len(sn) > maxPackSNLen {
		return false
	}
	for _, r := range sn {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// groupOrDefault returns g, or fallback when g is empty.
func groupOrDefault(g, fallback string) string {
	if g == "" {
		return fallback
	}
	return g
}

// applyEntry applies offset/scale and value-map translation to a raw value.
func applyEntry(e catalog.Entry, raw any, lang string) any {
	if len(e.ValueMap) > 0 {
		if label, ok := e.Label(strconv.Itoa(toInt(raw)), lang); ok {
			return label
		}
	}
	if e.Scale != 0 || e.Offset != 0 {
		if f, ok := toFloat(raw); ok {
			if e.Offset != 0 {
				f -= e.Offset
			}
			if e.Scale != 0 {
				f /= e.Scale
			}
			return f
		}
	}
	return raw
}

// toFloat coerces a JSON-decoded numeric value to float64.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// toInt coerces a JSON-decoded numeric value to int (for value-map keys).
func toInt(v any) int {
	if f, ok := toFloat(v); ok {
		return int(f)
	}
	return 0
}

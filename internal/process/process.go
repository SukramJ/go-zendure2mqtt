// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package process turns a raw [model.Report] into a flat list of
// publishable [Point]s: catalog lookup, scaling, value mapping and
// packData expansion all happen here, so the coordinator only has to
// publish what it is handed.
package process

import (
	"math"
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
	// Value is the processed value to publish: a float64 scaled to display
	// units, an enum's stable token (the English value_map entry, never a
	// localised label), a bool for the virtual switches, or the raw JSON
	// value for an unmapped property.
	Value any
	// Entry is the catalog entry, or nil for unmapped raw values.
	Entry *catalog.Entry
	// Unmapped is set when Entry has a value map and the raw value is not one
	// of its codes — an unknown code, or a value that is no integer code at
	// all. Value then carries the raw value unchanged (never a token it is
	// not), and the entity's discovery template renders it as unknown.
	Unmapped bool
}

// Resolve flattens rep into points using cat. An enum code is emitted as its
// token ([catalog.Entry.Token]) whatever LANGUAGE says — the label is Home
// Assistant's business, mapped in discovery (openccu-loom ADR 0083).
// Properties without a catalog entry are still published (group "misc", raw
// value) so nothing is lost while the catalog is filled in.
func Resolve(rep *model.Report, cat *catalog.Catalog) []Point {
	points := make([]Point, 0, len(rep.Properties))

	for key, raw := range rep.Properties {
		if entry, ok := cat.ByProperty(key); ok {
			e := entry
			value, unmapped := applyEntry(e, raw)
			points = append(points, Point{
				Group:    groupOrDefault(e.Group, GroupMisc),
				Topic:    e.TopicLeaf(),
				Value:    value,
				Entry:    &e,
				Unmapped: unmapped,
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
			unmapped := false
			var entryPtr *catalog.Entry
			topic := sanitizeSegment(key)
			if entry, ok := cat.ByProperty(key); ok {
				e := entry
				value, unmapped = applyEntry(e, raw)
				entryPtr = &e
				topic = e.TopicLeaf()
			}
			points = append(points, Point{
				Group:    GroupBattery,
				Topic:    topic,
				PackSN:   packSN,
				Value:    value,
				Entry:    entryPtr,
				Unmapped: unmapped,
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
		switch r {
		case '/', '+', '#', 0, utf8.RuneError:
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

// applyEntry applies offset/scale and value-map translation to a raw value,
// and reports whether a value-mapped entry got a value its map does not know.
//
// A value-mapped entry yields its token only for an integer code the map
// carries. Anything else — an unknown code, a fractional number, a string
// that is no integer — keeps its raw value and is reported unmapped, so it is
// never published as a member of an option set it is not: before 0.10.1 a
// non-numeric value was coerced to code 0, which smartMode reads as
// "persist". No value at all (nil or "") is passed through and is not
// unmapped; the coordinator clears the item.
func applyEntry(e catalog.Entry, raw any) (any, bool) {
	if len(e.ValueMap) > 0 {
		if raw == nil || raw == "" {
			return raw, false // no value: the item is cleared, not mapped
		}
		if code, ok := codeOf(raw); ok {
			if token, ok := e.Token(code); ok {
				return token, false
			}
		}
		return raw, true
	}
	if e.Scale != 0 || e.Offset != 0 {
		if f, ok := toFloat(raw); ok {
			if e.Offset != 0 {
				f -= e.Offset
			}
			if e.Scale != 0 {
				f /= e.Scale
			}
			return f, false
		}
	}
	return raw, false
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

// codeOf renders a raw value as a value-map key: an integral number, or a
// string holding a decimal integer. Anything else has no code.
func codeOf(v any) (string, bool) {
	if s, ok := v.(string); ok {
		i, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return "", false
		}
		return strconv.Itoa(i), true
	}
	f, ok := toFloat(v)
	if !ok || f != math.Trunc(f) || math.IsInf(f, 0) || f > math.MaxInt32 || f < math.MinInt32 {
		return "", false
	}
	return strconv.Itoa(int(f)), true
}

// Owners lists the distinct Home Assistant device owners in a point slice —
// the empty string for the main unit, then one battery-pack serial per pack —
// in the order they first appear, skipping points that mint no entity.
//
// One owner is one Home Assistant device and therefore one retained device
// document, so this is what the discovery plane iterates. Arrival order
// rather than sorted, because it decides the order the documents are
// published in and the main unit's points come first in every report this
// bridge resolves: Home Assistant sees the parent before the sub-devices
// that name it in `via_device`.
func Owners(points []Point) []string {
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	for _, p := range points {
		if p.Entry == nil || p.Entry.Platform == "" || seen[p.PackSN] {
			continue
		}
		seen[p.PackSN] = true
		out = append(out, p.PackSN)
	}
	return out
}

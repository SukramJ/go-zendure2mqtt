// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package hass publishes this bridge's Home Assistant MQTT auto-discovery
// documents, and owns the identity strings they carry.
//
// Since ADR 0070 phase 5 step 5 the form is Home Assistant's device-based
// discovery: one retained document per device at
// <base>/device/<node_id>/config, carrying the device block, the origin block
// and every one of that device's components. It replaced 29 retained
// per-entity configs at <base>/<platform>/<unique_id>/config — four segments,
// no node-id level, which Home Assistant permits and which is why the
// migration needed [publisher.LegacyTopicByUniqueID] to retract them.
//
// The payloads themselves are rendered by internal/harender through the
// shared go-hamqtt model; this package decides what to publish and when, and
// keeps the identity formulas ([UniqueID], [DeviceName], [EntityObjectID])
// that the migration froze.
package hass

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/SukramJ/go-hamqtt/discovery"

	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// BundleWriter is the narrow slice of [publisher.Runtime] this package
// publishes retained device documents through.
//
// One method, and it must be [publisher.Runtime.PublishBundle] rather than a
// plain retained publish, because the ordering inside it is the whole
// migration: it retracts the per-entity configs the document supersedes
// *first* and aborts before writing the document if a retraction fails.
// Publishing a device document while a per-entity config for the same
// unique_id is still retained is refused by Home Assistant — symmetrically,
// measured on HA 2026.9 on 2026-09-10/11 — with one
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line
// and nothing else: the entities simply do not appear. A partial retraction
// followed by a publish is the same failure for the entities whose old config
// survived, which is why the abort matters as much as the order.
//
// The boolean is part of the contract for the same reason it was before: the
// runtime claims the topic before it writes and records it afterwards, and
// that claim is what keeps the orphan sweep from retracting a document this
// process is publishing right now.
type BundleWriter interface {
	// PublishBundle retracts the superseded per-entity configs, then writes
	// one retained device document, and reports whether it reached the
	// broker.
	PublishBundle(ctx context.Context, b *discovery.Bundle) (bool, error)
}

// BundleRenderer renders one device's retained discovery document.
//
// An interface here, satisfied by harender.Renderer, purely to keep the
// dependency pointing one way: internal/harender calls [UniqueID],
// [DeviceName] and [EntityObjectID] out of this package — the production
// identity formulas, called rather than copied, so a second rendering path
// cannot drift from them — so this package cannot import that one.
type BundleRenderer interface {
	// Bundle renders the device document for one owner: the main unit when
	// packSN is empty, that unit's battery sub-device otherwise. It returns
	// nil, nil for an owner that mints no entity.
	Bundle(dev source.Device, report *model.Report, packSN string, points []process.Point) (*discovery.Bundle, error)
}

// Discovery publishes Home Assistant device documents (idempotently: each
// unique_id triggers one publish of its device's document per process
// lifetime).
type Discovery struct {
	base   string // HA discovery root, e.g. "homeassistant"
	root   string // bridge MQTT topic root, e.g. "zendure2mqtt"
	render BundleRenderer
	pub    BundleWriter
	logger *slog.Logger

	mu sync.Mutex
	// sent records the unique_ids whose device document has been published
	// in this process. Keyed on the unique_id and not on the document topic
	// deliberately: the guard has to fire for an entity that appears later
	// in a process — a pack learned on the third poll, a catalog point whose
	// value was absent from the first report — and a document-level guard
	// would leave it out until the next restart.
	sent map[string]bool
	// byTopic maps a document topic to the unique_ids it carried, so
	// [Discovery.Forget] can undo the guard for a document the sweep
	// cleared. The topic no longer names a unique_id, which is what the
	// per-entity form gave for free.
	byTopic map[string][]string
}

// New constructs a Discovery publisher.
func New(base, root string, render BundleRenderer, pub BundleWriter, logger *slog.Logger) *Discovery {
	if logger == nil {
		logger = slog.Default()
	}
	return &Discovery{
		base: base, root: root, render: render, pub: pub, logger: logger,
		sent: map[string]bool{}, byTopic: map[string][]string{},
	}
}

// Publish emits the retained device document for every Home Assistant device
// in a report — the main unit and one per battery pack — and returns the set
// of document topics that make up this device's current entity set, whether
// freshly published this call or already sent earlier in the process
// lifetime. The caller reconciles this set against the broker's retained
// configs to clear orphans (see the coordinator's reconcileOrphans). Points
// without a catalog entry or platform are skipped, and an owner that mints no
// entity at all publishes nothing: an empty `components` map is not a device
// with no entities, it is Home Assistant's instruction to remove every entity
// of that device.
//
// The guard is payload-blind, and stays so. It fires on a unique_id never
// published in this process, so the first report of a process decides the
// retained document for the rest of it and a catalog edit does not reach the
// broker until a restart. That is F1 of the ADR 0070 phase-5 measurement,
// preserved rather than fixed here because a defect fixed inside the step
// that also moves every payload is a defect nobody can measure. One
// consequence of the document form is unavoidable and is a narrowing rather
// than a fix: when a *new* entity appears mid-process its device's whole
// document is rewritten, so that one publish also carries the current bodies
// of its siblings. There is one document per device; it cannot be written
// per entity.
func (d *Discovery) Publish(
	ctx context.Context,
	dev source.Device,
	report *model.Report,
	points []process.Point,
) (published map[string]bool) {
	owners := process.Owners(points)
	published = make(map[string]bool, len(owners))
	for _, packSN := range owners {
		bundle, err := d.render.Bundle(dev, report, packSN, points)
		if err != nil {
			d.logger.Warn("hass.bundle_failed",
				slog.String("sn", dev.SN), slog.String("pack", packSN), slog.String("err", err.Error()))
			continue
		}
		if bundle == nil {
			continue
		}
		topic := bundle.Topic(d.base)
		// The document topic belongs to the device's current set whether or
		// not we (re)send it below, so record it before the already-sent
		// guard: a steady-state publish must still report the full set so
		// reconciliation does not treat a live document as an orphan.
		published[topic] = true

		ids := componentIDs(bundle)
		d.mu.Lock()
		fresh := false
		for _, id := range ids {
			if !d.sent[id] {
				fresh = true
				break
			}
		}
		d.mu.Unlock()
		if !fresh {
			continue
		}

		if _, err := d.pub.PublishBundle(ctx, bundle); err != nil {
			// Nothing is marked sent, so the next report retries. That
			// matters more here than it did for a per-entity config: a
			// failure inside PublishBundle can be a failed *retraction*,
			// which means a legacy config is still retained and Home
			// Assistant would refuse the document even if it arrived.
			d.logger.Warn("hass.publish_failed",
				slog.String("topic", topic), slog.String("err", err.Error()))
			continue
		}
		d.mu.Lock()
		for _, id := range ids {
			d.sent[id] = true
		}
		d.byTopic[topic] = ids
		d.mu.Unlock()
	}
	return published
}

// componentIDs lists the unique_ids a document carries, in the document's own
// sorted key order.
func componentIDs(b *discovery.Bundle) []string {
	out := make([]string, 0, len(b.Components))
	for _, key := range b.Keys() {
		if uid := b.Components[key].UniqueID; uid != "" {
			out = append(out, uid)
		}
	}
	return out
}

// Forget drops the given device documents from the already-sent set so they
// are republished on the next matching report. The coordinator calls this
// right after clearing retained orphan configs: if an "orphan" was in fact
// still live (e.g. a transiently shrunken report), the next report restores
// it instead of leaving it deleted for the rest of the process lifetime.
//
// A topic this process never published is ignored rather than parsed. The
// document topic carries a node id and no unique_id, so the mapping back to
// the guard's keys is the one this process recorded when it wrote the
// document — there is nothing in the string to recover it from, which is the
// one thing the per-entity form gave for free.
func (d *Discovery) Forget(configTopics []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, topic := range configTopics {
		for _, id := range d.byTopic[topic] {
			delete(d.sent, id)
		}
		delete(d.byTopic, topic)
	}
}

// UniqueID is the formula behind every entity's unique_id, exported so a
// second renderer can be proved to produce the same string rather than a
// second copy of the same formula.
//
// Home Assistant keys its entity registry on this and has no migration path
// for it, so the string is frozen: ADR 0070 sanctions a re-key for the
// bridges and this one declines it. Declining it is why this bridge was
// chosen as the pilot — and it is what makes the device-document migration
// survivable, because an entity whose unique_id is unchanged keeps its
// history, its name override, its icon, its area and its automations when its
// config moves from a per-entity topic into a device document.
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

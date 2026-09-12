// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"strings"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// ConfigFilter is the MQTT filter matching this daemon's discovery config
// topics (<base>/+/+/config). Zendure embeds the device serial inside the
// unique_id topic level (zendure_<sn>_<topic>) rather than as its own MQTT
// level, so a per-device filter is not expressible as an MQTT wildcard: the
// reconcile subscribes to the whole discovery prefix and scopes the orphan
// decision to one device in code (see [Discovery.OrphanConfigs]).
func (d *Discovery) ConfigFilter() string { return d.base + "/+/+/config" }

// IsOwnConfig reports whether a retained HA discovery config payload was
// published by this daemon — its unique_id is in our `<root>_` namespace and
// its state topic (when present) is under our MQTT root — so orphan cleanup
// never touches configs owned by another integration or bridge instance.
func (d *Discovery) IsOwnConfig(payload []byte) bool {
	uid, state, ok := parseOwnership(payload)
	if !ok {
		return false
	}
	return strings.HasPrefix(uid, d.root+"_") &&
		(state == "" || strings.HasPrefix(state, d.root+"/"))
}

// OwnsDeviceConfigTopic reports whether a parsed retained discovery config
// topic is one this daemon publishes for the given device serial.
//
// It is the topic-level twin of [Discovery.IsOwnConfig], and it exists
// because that is the only question [publisher.SweepRequest.Owns] can be
// asked: the predicate runs on the transport's read loop with the parsed
// topic and nothing else, before the payload is offered to Inspect. Scoping
// the sweep to one device matters and is not a refinement — a fleet-wide
// predicate would judge a second unit's configs unclaimed during the poll in
// which only the first unit has published, and retract them.
//
// The form checked is this bridge's own and only form,
// `<base>/<platform>/<unique_id>/config`: four segments, no node-id level,
// which is what [publisher.ParseConfigTopic] reports as a non-bundle topic
// with an empty NodeID. A five-segment config or a device document under the
// same prefix belongs to somebody else — or to a later migration step — and
// is declined here rather than assumed.
//
// Ownership of the *payload* is still checked separately, through
// [Discovery.IsOwnConfig] on the body the sweep inspects: the topic namespace
// is specific but it is still a namespace, and a retained config this daemon
// did not write must never be cleared on the strength of its topic alone.
func OwnsDeviceConfigTopic(root, sn string, t publisher.ConfigTopic) bool {
	if t.Bundle || t.Platform == "" || t.NodeID != "" || t.ObjectID == "" {
		return false
	}
	return strings.HasPrefix(t.ObjectID, root+"_"+sn+"_")
}

// OrphanConfigs returns the retained config topics that this daemon owns for
// the given device serial (unique_id prefixed `<root>_<sn>_`) and that are no
// longer in the published set — entities removed, renamed or re-platformed
// across versions. A foreign integration's config, another Zendure device's
// config, and an already-cleared (empty) payload are never returned, so the
// caller can safely publish an empty retained payload to each returned topic to
// remove it.
func (d *Discovery) OrphanConfigs(retained map[string][]byte, published map[string]bool, sn string) []string {
	prefix := d.root + "_" + sn + "_"
	out := make([]string, 0, len(retained))
	for topic, payload := range retained {
		if len(payload) == 0 || published[topic] {
			continue // already cleared, or still a current entity
		}
		if !d.IsOwnConfig(payload) {
			continue // belongs to another integration — never touch it
		}
		uid, _, _ := parseOwnership(payload)
		if !strings.HasPrefix(uid, prefix) {
			continue // another Zendure device's config — leave it for that device
		}
		out = append(out, topic)
	}
	return out
}

// parseOwnership extracts the unique_id and state_topic from a discovery config
// payload. ok is false when the payload is not valid JSON.
func parseOwnership(payload []byte) (uniqueID, stateTopic string, ok bool) {
	var cfg struct {
		UniqueID   string `json:"unique_id"`
		StateTopic string `json:"state_topic"`
	}
	if json.Unmarshal(payload, &cfg) != nil {
		return "", "", false
	}
	return cfg.UniqueID, cfg.StateTopic, true
}

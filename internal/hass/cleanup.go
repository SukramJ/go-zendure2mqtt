// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"strings"
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

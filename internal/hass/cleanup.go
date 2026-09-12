// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"encoding/json"
	"strings"

	"github.com/SukramJ/go-hamqtt/publisher"
)

// IsOwnConfig reports whether a retained Home Assistant discovery payload was
// published by this daemon, so orphan cleanup never touches a config owned by
// another integration or another bridge instance.
//
// Both discovery forms are recognised, and both have to be. The device
// documents this daemon publishes today are one; the per-entity configs every
// release before ADR 0070 phase 5 step 5 published are the other, and those
// are still retained on every installed broker until this daemon clears them.
// A sweep blind to the old form would leave the whole legacy fleet standing
// as duplicate ghost entities — which is precisely what the ADR requires the
// first start to clear.
//
// The test is the same in both: the unique_id must be in this bridge's
// `<root>_` namespace and the state topic, where there is one, must be under
// this bridge's MQTT root. In a document that is asked of every component,
// because a document is owned as a whole — it is retracted as a whole — and
// one foreign component in it would mean the topic is not ours to clear.
func (d *Discovery) IsOwnConfig(payload []byte) bool {
	var body struct {
		UniqueID   string `json:"unique_id"`
		StateTopic string `json:"state_topic"`
		Components map[string]struct {
			UniqueID   string `json:"unique_id"`
			StateTopic string `json:"state_topic"`
		} `json:"components"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return false
	}
	if len(body.Components) > 0 {
		for _, comp := range body.Components {
			if !d.ownsIdentity(comp.UniqueID, comp.StateTopic) {
				return false
			}
		}
		return true
	}
	return d.ownsIdentity(body.UniqueID, body.StateTopic)
}

// ownsIdentity is the ownership test for one entity's identity pair.
func (d *Discovery) ownsIdentity(uniqueID, stateTopic string) bool {
	return strings.HasPrefix(uniqueID, d.root+"_") &&
		(stateTopic == "" || strings.HasPrefix(stateTopic, d.root+"/"))
}

// OwnsDeviceConfigTopic reports whether a parsed retained discovery config
// topic is one this daemon publishes, or used to publish, for the given
// device serial.
//
// It is the topic-level twin of [Discovery.IsOwnConfig], and it exists
// because that is the only question [publisher.SweepRequest.Owns] can be
// asked: the predicate runs on the transport's read loop with the parsed
// topic and nothing else, before the payload is offered to Inspect. Scoping
// the sweep to one device matters and is not a refinement — a fleet-wide
// predicate would judge a second unit's configs unclaimed during the poll in
// which only the first unit has published, and retract them.
//
// Two forms are owned, and the second is a migration obligation rather than a
// convenience:
//
//   - `<base>/device/<node_id>/config`, this daemon's current form, whose
//     node id is the device identifier: `<root>_<sn>` for a unit and
//     `<root>_<sn>_pack_<packSN>` for one of its battery packs. Matched
//     exactly for the unit and by the `_pack_` prefix for the packs, so a
//     second unit whose serial merely begins with this one's is not claimed.
//   - `<base>/<platform>/<unique_id>/config`, the four-segment per-entity
//     form with no node-id level — what [publisher.ParseConfigTopic] reports
//     as a non-bundle topic with an empty NodeID — which is what every
//     release before ADR 0070 phase 5 step 5 published. Its retained configs
//     outlive the upgrade. [publisher.Runtime.PublishBundle] retracts the ones
//     that correspond to a component of the new document; the ones that do
//     not — an entity removed or renamed in some earlier release — can only
//     be found by a sweep, and this is what lets the sweep find them.
//
// A five-segment config under the same prefix belongs to somebody else and is
// declined here rather than assumed.
//
// Ownership of the *payload* is still checked separately, through
// [Discovery.IsOwnConfig] on the body the sweep inspects: the topic namespace
// is specific but it is still a namespace, and a retained config this daemon
// did not write must never be cleared on the strength of its topic alone.
func OwnsDeviceConfigTopic(root, sn string, t publisher.ConfigTopic) bool {
	device := root + "_" + sn
	if t.Bundle {
		return t.NodeID == device || strings.HasPrefix(t.NodeID, device+"_pack_")
	}
	if t.Platform == "" || t.NodeID != "" || t.ObjectID == "" {
		return false
	}
	return strings.HasPrefix(t.ObjectID, device+"_")
}

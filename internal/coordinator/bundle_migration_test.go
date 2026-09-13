// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// TestDeviceDocumentsArePinned pins the retained device documents verbatim:
// the exact bytes that reach the broker, keyed by the topic they are retained
// on.
//
// The per-entity pins beside it store an assembled view — a component body
// plus the document's device and origin blocks — because that is the shape
// that can be compared across the form change. This one stores the document
// itself, because that is what a broker holds and what Home Assistant parses,
// and because the assembled view cannot see the two mistakes the document
// form makes possible: a component landing in the wrong device's document,
// and a document whose `components` map is empty, which is Home Assistant's
// instruction to delete every entity of that device.
//
// Refresh with:
//
//	go test ./internal/coordinator/ -run ArePinned -update-discovery-golden
func TestDeviceDocumentsArePinned(t *testing.T) {
	captured := capturePublish(t, goldenUnit(), goldenReport())
	docs := capturedDocuments(t, captured)

	if *updateDiscoveryGolden {
		writeGoldenJSON(t, goldenBundlePath, docs)
		t.Logf("rewrote %s with %d documents", goldenBundlePath, len(docs))
		return
	}

	var want map[string]map[string]any
	readGoldenJSON(t, goldenBundlePath, &want)

	gotKeys, wantKeys := sortedKeys(docs), sortedKeys(want)
	if strings.Join(gotKeys, "\n") != strings.Join(wantKeys, "\n") {
		t.Fatalf("document topics changed\n got %v\nwant %v", gotKeys, wantKeys)
	}
	for _, topic := range wantKeys {
		if got, exp := mustCanonical(docs[topic]), mustCanonical(want[topic]); got != exp {
			t.Errorf("%s: document changed\n got %s\nwant %s", topic, got, exp)
		}
	}
	if len(docs) != 2 {
		t.Errorf("published %d documents, want 2 — one per Home Assistant device", len(docs))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestTheMoveChangesOnlyTheTopicAndTheOrigin is this step's central claim,
// checked field by field rather than read off a diff.
//
// ADR 0070 phase 5 chose this bridge as the pilot precisely because the
// sanctioned re-key could be declined here: every identity string is
// reproducible byte-exactly, so the runtime can be proved on an installed
// base without the confounding of a changed unique_id. That makes the
// permitted difference between the frozen pre-migration pin and what this
// bridge publishes now a closed, short list:
//
//   - the topic, from <base>/<platform>/<unique_id>/config to
//     <base>/device/<node_id>/config;
//   - `origin`, added, because discovery.Validate requires origin.name on a
//     device document where Home Assistant treats it as optional on a
//     per-entity config;
//   - `platform`, added, because a component inside a document has no topic
//     to say what it is — and its value must be the platform the old topic's
//     second segment carried, or the entity changes domain.
//
// Everything else must be byte-identical: unique_id, default_entity_id, the
// whole device block, state_topic, command_topic, the three flat availability
// keys, and every platform projection. Anything else this reports is a defect
// in the change, not a pin to refresh.
//
// Mutation check (each verified to fail this test): dropping the
// Context.UniqueID override so the library case-folds the serial; dropping
// the Context.ObjectID override so default_entity_id is seeded from the topic
// root; returning topic.Default's state topic instead of process.StateTopic;
// omitting the flat availability triple from Entity.BuildDiscovery; slugging
// the node id.
func TestTheMoveChangesOnlyTheTopicAndTheOrigin(t *testing.T) {
	legacy := legacyAll(t)
	now := discoveryEntries(t, capturePublish(t, goldenUnit(), goldenReport()))

	if len(now) != len(legacy) {
		t.Fatalf("the bridge now publishes %d entities, the frozen pin has %d — an entity appeared or vanished",
			len(now), len(legacy))
	}

	addedKeys := map[string]int{}
	for _, uid := range sortedKeys(legacy) {
		was, is := legacy[uid], now[uid]
		if is.Topic == "" {
			t.Errorf("%s: in the frozen pin but no longer published — the entity would disappear", uid)
			continue
		}

		// The topic moves, and only in the one way it is allowed to.
		wantPlatform := strings.Split(was.Topic, "/")[1]
		node := nodeIDOf(t, is.Payload)
		if got, exp := is.Topic, "homeassistant/device/"+node+"/config"; got != exp {
			t.Errorf("%s: topic %s, want %s", uid, got, exp)
		}

		for _, key := range sortedKeys(is.Payload) {
			old, had := was.Payload[key]
			if !had {
				addedKeys[key]++
				continue
			}
			if mustCanonicalValue(is.Payload[key]) != mustCanonicalValue(old) {
				t.Errorf("%s: %s moved\n now %s\n was %s", uid, key,
					mustCanonicalValue(is.Payload[key]), mustCanonicalValue(old))
			}
		}
		for _, key := range sortedKeys(was.Payload) {
			if _, still := is.Payload[key]; !still {
				t.Errorf("%s: %s was published before this release and is now absent", uid, key)
			}
		}

		if got, _ := is.Payload["platform"].(string); got != wantPlatform {
			t.Errorf("%s: platform %q, want %q — the old topic's second segment", uid, got, wantPlatform)
		}
		origin, _ := is.Payload["origin"].(map[string]any)
		if name, _ := origin["name"].(string); name != harender.OriginName {
			t.Errorf("%s: origin.name %q, want %q", uid, name, harender.OriginName)
		}
	}

	// And the added keys are exactly those two, on every single row. A key
	// added to one entity and not the others is the shape of an accident.
	want := map[string]int{"platform": len(legacy), "origin": len(legacy)}
	if len(addedKeys) != len(want) {
		t.Errorf("keys added by this release: %v, want only platform and origin on all %d rows",
			addedKeys, len(legacy))
	}
	for key, n := range want {
		if addedKeys[key] != n {
			t.Errorf("%s appeared on %d of %d rows, want all of them", key, addedKeys[key], n)
		}
	}
}

// nodeIDOf reads the node id this bridge would publish an entity's device
// document under, out of the entity's own device block: the first identifier.
func nodeIDOf(t *testing.T, payload map[string]any) string {
	t.Helper()
	dev, _ := payload["device"].(map[string]any)
	ids, _ := dev["identifiers"].([]any)
	if len(ids) == 0 {
		t.Fatalf("payload carries no device identifiers: %v", payload)
	}
	id, _ := ids[0].(string)
	return id
}

func mustCanonicalValue(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(raw)
}

// TestTheIdentityHazardsSurviveTheMove checks the four slug divergences the
// measurement singled out, and in particular that the two *pinned defects*
// are still there.
//
// A defect that disappears inside a migration step is not a win, it is a lost
// measurement: the bundle form cannot be credited with a fix nobody applied,
// and if it changed one of these seeds it changed identity, which is the one
// thing this release promises it did not.
//
//   - F8: packs "AB-12" and "AB_12" keep two distinct unique_ids and ONE
//     shared default_entity_id, because slugify folds both separators. Home
//     Assistant resolves the collision by suffixing, silently. The library's
//     topic.Slug preserves the hyphen and would fix it — which is exactly why
//     this release does not adopt it.
//   - The "é" of "Café Nord" is still dropped rather than transliterated to
//     "e", so the seed reads "caf_nord".
//
// Mutation check: replacing hass.EntityObjectID with the library's topic.Slug
// in harender.Renderer.Entity fails both assertions below (the collision
// splits, and "caf_nord" becomes "cafe_nord").
func TestTheIdentityHazardsSurviveTheMove(t *testing.T) {
	got := map[string]goldenEntry{}
	for _, c := range identityCases() {
		for uid, e := range discoveryEntries(t, capturePublish(t, c.dev, c.report)) {
			got[c.name+"/"+uid] = e
		}
	}

	if *updateDiscoveryGolden {
		writeGolden(t, goldenIdentityPath, got)
		t.Logf("rewrote %s with %d payloads", goldenIdentityPath, len(got))
		return
	}
	compareGolden(t, goldenIdentityPath, got)

	// F8, asserted rather than only pinned: the two pack entities must still
	// carry one entity-id seed between them.
	const (
		hyphen     = "pack-serial-hyphen-vs-underscore/zendure2mqtt_SF2400AC0012345_pack_AB-12_soc_level"
		underscore = "pack-serial-hyphen-vs-underscore/zendure2mqtt_SF2400AC0012345_pack_AB_12_soc_level"
	)
	a, b := got[hyphen], got[underscore]
	if a.Topic == "" || b.Topic == "" {
		t.Fatalf("the F8 collision rows are gone: %q and %q", a.Topic, b.Topic)
	}
	idA, _ := a.Payload["default_entity_id"].(string)
	idB, _ := b.Payload["default_entity_id"].(string)
	if idA != idB {
		t.Errorf("F8 disappeared: the two packs now seed %q and %q. That is a fix nobody applied in this release — report it rather than refreshing the pin.",
			idA, idB)
	}
	// The two devices are still distinct, which is what makes the collision a
	// collision rather than one entity: distinct unique_ids, distinct
	// documents.
	if a.Topic == b.Topic {
		t.Errorf("the two packs share a device document topic %s; they are separate Home Assistant devices", a.Topic)
	}

	// The dropped accent.
	accent := got["non-german-accent/zendure2mqtt_SF2400AC0012345_electric_level"]
	if id, _ := accent.Payload["default_entity_id"].(string); id != "sensor.caf_nord_electric_level" {
		t.Errorf("default_entity_id = %q, want sensor.caf_nord_electric_level — the dropped \"é\" is a pinned defect and must not be fixed here",
			id)
	}
}

// TestLegacyConfigsAreRetractedBeforeTheDocument is the pin for the one
// ordering in this whole migration that fails silently in one direction.
//
// Home Assistant refuses a device document while a per-entity config for the
// same unique_id is still retained — symmetrically, measured on HA 2026.9 on
// 2026-09-10/11 — and says so with one
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line
// and nothing else. The entities simply do not appear.
//
// The guarantee is per *document*, and that is the guarantee the conflict
// needs: Home Assistant keys the refusal on the unique_id, so what must
// precede a document is the retraction of the legacy configs of that
// document's own components. This bridge publishes one document per Home
// Assistant device — the unit and each battery pack — through
// Runtime.PublishBundle, which retracts that document's superseded topics
// before writing it and aborts if one fails. So the pack's seven retractions
// legitimately land *after* the unit's document: the two documents share no
// unique_id, and holding the whole fleet back until every retraction has
// landed would buy nothing while making one device's broker failure the
// fleet's. The assertion below is therefore the exact one — no retraction may
// follow the publish of the document that owns it — rather than the cruder
// "all retractions before all documents", which this bridge deliberately does
// not do.
//
// The list is asserted against the frozen pre-migration pin rather than
// against anything this build renders: what has to be retracted is what the
// *previous release* published, and deriving both sides from the current code
// would let them agree on a wrong answer.
//
// Mutation check, each verified to fail this test: dropping
// Config.LegacyEntityTopics, so SupersededTopics renders the five-segment
// form — 29 retractions, none of them on a topic this fleet holds, which is
// the measured failure and is indistinguishable from success on the wire;
// stating LegacyTopicByObjectID instead, likewise 29 wrong topics;
// case-folding the node id, which moves the documents and orphans their
// components; publishing the document through Runtime.Publish instead of
// PublishBundle, which retracts nothing at all.
func TestLegacyConfigsAreRetractedBeforeTheDocument(t *testing.T) {
	captured, wire := capturePublishWire(t, goldenUnit(), goldenReport())

	// Which document owns which unique_id, read off the documents that were
	// actually published.
	owner := map[string]string{}
	for topic, doc := range capturedDocuments(t, captured) {
		components, _ := doc["components"].(map[string]any)
		for _, raw := range components {
			comp, _ := raw.(map[string]any)
			if uid, _ := comp["unique_id"].(string); uid != "" {
				owner[uid] = topic
			}
		}
	}
	if len(owner) != 29 {
		t.Fatalf("the published documents carry %d components in total, want 29", len(owner))
	}

	var retracted []string
	done := map[string]bool{}
	documents := 0
	for _, rec := range wire {
		if !strings.HasPrefix(rec.Topic, "homeassistant/") {
			continue
		}
		if len(rec.Payload) == 0 {
			retracted = append(retracted, rec.Topic)
			uid := uniqueIDOfLegacyTopic(t, rec.Topic)
			if doc := owner[uid]; doc != "" && done[doc] {
				t.Errorf("%s was retracted after %s — the document that owns its unique_id — had already been published; Home Assistant would have refused that component",
					rec.Topic, doc)
			}
			continue
		}
		done[rec.Topic] = true
		documents++
	}

	wantTopics := make([]string, 0, 29)
	for _, e := range legacyAll(t) {
		wantTopics = append(wantTopics, e.Topic)
	}
	sort.Strings(wantTopics)
	sort.Strings(retracted)
	if strings.Join(retracted, "\n") != strings.Join(wantTopics, "\n") {
		t.Errorf("the retraction list is not this fleet's 29 configs\n retracted %v\n pinned    %v",
			retracted, wantTopics)
	}
	if documents != 2 {
		t.Errorf("published %d device documents, want 2", documents)
	}
}

// uniqueIDOfLegacyTopic reads the unique_id out of a four-segment per-entity
// config topic, <prefix>/<platform>/<unique_id>/config, through the library's
// own parser rather than by counting slashes here.
func uniqueIDOfLegacyTopic(t *testing.T, topic string) string {
	t.Helper()
	parsed, ok := publisher.ParseConfigTopic(discoveryPrefix, topic)
	if !ok || parsed.Bundle || parsed.NodeID != "" {
		t.Fatalf("%s is not the four-segment per-entity form this fleet is on", topic)
	}
	return parsed.ObjectID
}

// TestSupersededTopicsMatchTheFrozenFleet is the same verdict one level down,
// on the library call rather than on the wire: the list
// Runtime.PublishBundle derives from the rendered documents is exactly the 29
// topics the previous release retained, no more and no fewer.
//
// "No more" matters as much as "no fewer". A retraction list wider than the
// fleet clears a retained config belonging to another writer in a shared
// discovery tree, which is the failure mode the library refuses to risk by
// guessing — and why stating a form *replaces* the default rather than
// adding to it.
func TestSupersededTopicsMatchTheFrozenFleet(t *testing.T) {
	wantTopics := make([]string, 0, 29)
	for _, e := range legacyAll(t) {
		wantTopics = append(wantTopics, e.Topic)
	}
	sort.Strings(wantTopics)

	dev, report := goldenUnit(), goldenReport()
	points := resolvePoints(t, dev, report)
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}

	got := make([]string, 0, len(wantTopics))
	for _, packSN := range processOwners(points) {
		bundle, err := r.Bundle(dev, report, packSN, points)
		if err != nil {
			t.Fatalf("pack %q: Bundle: %v", packSN, err)
		}
		got = append(got, publisher.SupersededTopics(discoveryPrefix, bundle,
			publisher.LegacyTopicByUniqueID)...)

		// The forms the measurement ruled out, re-checked here rather than
		// trusted: either of them in place of the stated one retracts 0 of
		// this fleet's 29 configs.
		for name, form := range map[string]publisher.LegacyTopicFunc{
			"LegacyTopicByObjectID": publisher.LegacyTopicByObjectID,
			"LegacyTopicWithNodeID": publisher.LegacyTopicWithNodeID,
		} {
			for _, topic := range publisher.SupersededTopics(discoveryPrefix, bundle, form) {
				for _, pinned := range wantTopics {
					if topic == pinned {
						t.Errorf("%s rendered the pinned topic %s; the premise of stating LegacyTopicByUniqueID no longer holds and the verdict needs re-taking",
							name, topic)
					}
				}
			}
		}
	}
	sort.Strings(got)

	if strings.Join(got, "\n") != strings.Join(wantTopics, "\n") {
		t.Errorf("SupersededTopics would not retract this bridge's fleet\n would retract %v\n pinned        %v\n(a config left retained makes Home Assistant refuse the document for that entity, with one WARNING line as the only evidence)",
			got, wantTopics)
	}
}

// processOwners is process.Owners, reached through the resolved points so the
// test iterates the same owner list the publish path does.
func processOwners(points []process.Point) []string { return process.Owners(points) }

// failingClient is a capturingClient that refuses one topic.
type failingClient struct {
	capturingClient

	mu     sync.Mutex
	refuse string
}

func (f *failingClient) Publish(
	ctx context.Context,
	topic string,
	payload []byte,
	qos mqtt.QoS,
	retain bool,
	opts ...mqtt.PublishOption,
) error {
	f.mu.Lock()
	refuse := f.refuse
	f.mu.Unlock()
	if topic == refuse {
		return errors.New("broker refused the retraction")
	}
	return f.capturingClient.Publish(ctx, topic, payload, qos, retain, opts...)
}

// TestAFailedRetractionAbortsTheDocument is the other half of the ordering
// rule, and the one a correct order does not give for free.
//
// A partial retraction followed by a published document is the same silent
// failure as no retraction at all, for every entity whose old config
// survived: Home Assistant refuses that component and logs one line. So the
// publish has to abort, and the daemon has to retry rather than mark the
// document sent — which it does, because nothing is recorded until
// PublishBundle returns without an error.
//
// The containment is per device, deliberately, and the assertions say so: the
// broker refuses one of the *unit's* 22 retractions, so the unit's document
// is not written, while the pack's document — whose own seven retractions
// landed, and which shares no unique_id with the unit — is. Taking the fleet
// down with one device would turn a transient broker refusal on one topic
// into every entity of every device missing.
//
// Mutation check (verified): marking the document sent in
// hass.Discovery.Publish before checking PublishBundle's error makes the
// second poll publish nothing and fails the retry assertion. Removing the
// `continue` on the error path publishes the unit's document anyway and fails
// the first.
func TestAFailedRetractionAbortsTheDocument(t *testing.T) {
	// One of the unit's superseded topics, taken from the frozen pin so the
	// test refuses a topic the migration really does retract.
	var refuse string
	for _, e := range legacyAll(t) {
		if strings.Contains(e.Topic, "_electric_level/") {
			refuse = e.Topic
		}
	}
	if refuse == "" {
		t.Fatal("no pinned legacy topic to refuse")
	}

	pub := &failingClient{refuse: refuse}
	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	rt := newHARuntime(pub, cfg.MQTTTopic)
	t.Cleanup(rt.Close)
	disc := hass.New("homeassistant", cfg.MQTTTopic,
		harender.Renderer{Root: cfg.MQTTTopic, Lang: cfg.Language}, rt, discardLogger())

	dev, report := goldenUnit(), goldenReport()
	points := resolvePoints(t, dev, report)
	const (
		unitDoc = "homeassistant/device/zendure2mqtt_SF2400AC0012345/config"
		packDoc = "homeassistant/device/zendure2mqtt_SF2400AC0012345_pack_AO4H2301X01/config"
	)

	published := disc.Publish(context.Background(), dev, report, points)
	if len(published) != 2 {
		t.Fatalf("published set has %d topics, want 2", len(published))
	}
	written := documentsOnTheWire(pub)
	if written[unitDoc] {
		t.Error("the unit's document was published although one of its retractions failed; Home Assistant would have refused the conflicting component")
	}
	if !written[packDoc] {
		t.Error("the pack's document was withheld although its own retractions all landed; one device's broker failure must not take the fleet down")
	}
	for _, topic := range rt.Declared() {
		if topic == unitDoc {
			t.Error("the runtime declared the aborted document")
		}
	}

	// The failure is transient, and the next poll must retry: the unit is
	// otherwise stuck in the one state where none of its entities appears in
	// Home Assistant at all.
	pub.mu.Lock()
	pub.refuse = ""
	pub.mu.Unlock()
	disc.Publish(context.Background(), dev, report, points)

	if written = documentsOnTheWire(pub); !written[unitDoc] {
		t.Error("the retry did not publish the unit's document")
	}
}

// documentsOnTheWire is the set of device documents a client has actually been
// handed a non-empty payload for.
func documentsOnTheWire(pub *failingClient) map[string]bool {
	out := map[string]bool{}
	for _, rec := range pub.wire() {
		if strings.HasPrefix(rec.Topic, "homeassistant/device/") && len(rec.Payload) > 0 {
			out[rec.Topic] = true
		}
	}
	return out
}

// TestNodeIDIsStableDistinctAndUnfolded pins the one free decision this step
// had to make.
//
// The node id is a topic segment this fleet has never had — the per-entity
// configs it replaces carry no node-id level at all — so there is no
// installed spelling to preserve, and Home Assistant keys its *device*
// registry on `identifiers` rather than on the node id, so the choice is
// free. It is still load-bearing: a re-keying later would leave the old
// document retained on the broker, announcing the same device from a second
// topic, so it is picked once and frozen with the rest of the identity.
//
// The choice is the device identifier verbatim. The three properties that
// makes it right are asserted below; the fourth reason is that it is the
// spelling of the serial this bridge already uses everywhere else, which the
// library's slugged default is not.
//
// Mutation check: lowercasing the node id in harender.Context.NodeID fails
// the case assertion and TestTheMoveChangesOnlyTheTopicAndTheOrigin;
// returning a constant fails the distinctness assertion; returning "" fails
// the Validate assertion in TestRenderedBundleValidates.
func TestNodeIDIsStableDistinctAndUnfolded(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	ctx := r.Context()
	dev, report := goldenUnit(), goldenReport()

	unit := ctx.NodeID(r.Device(dev, report, ""))
	pack := ctx.NodeID(r.Device(dev, report, "AO4H2301X01"))

	if unit != "zendure2mqtt_"+dev.SN {
		t.Errorf("unit node id %q, want %q — the device identifier verbatim", unit, "zendure2mqtt_"+dev.SN)
	}
	if !strings.Contains(unit, dev.SN) {
		t.Errorf("the node id %q case-folded the serial %q; every other appearance of the serial in this bridge's tree is verbatim",
			unit, dev.SN)
	}
	if pack == unit {
		t.Errorf("the unit and its pack share the node id %q; they are two Home Assistant devices and would share one document", unit)
	}

	// Stable: derived from the serial and the root and from nothing else, so
	// two renderers built independently agree.
	other := harender.Renderer{Root: "zendure2mqtt", Lang: "de"}
	if got := other.Context().NodeID(other.Device(dev, report, "")); got != unit {
		t.Errorf("the node id is not stable across renderers: %q then %q", unit, got)
	}

	// A legal single topic segment. discovery.Validate refuses one that is
	// not, but only for the payloads a test happens to render.
	for _, node := range []string{unit, pack} {
		if strings.ContainsAny(node, "/+#") || node == "" {
			t.Errorf("node id %q is not a legal MQTT topic segment", node)
		}
	}

	// And it is the same string the device block's identifiers carry, which
	// is what makes the document findable from the registry entry.
	if got := r.Device(dev, report, "").Identity.UID(); got != unit {
		t.Errorf("node id %q disagrees with the device identifier %q", unit, got)
	}
}

// TestEveryDocumentAndRetractionIsAtMostOnce extends the QoS pin to the two
// wire acts this step added.
//
// The state plane's pin reads the level off the transport call because no
// golden file can see it. The same is true of a retraction and of a device
// document, and the document is the larger risk of the two: it is written
// through publisher.Runtime rather than the state publisher, so it takes its
// QoS from a different config field, and a mismatch would move the delivery
// guarantee of the discovery plane alone.
//
// Mutation check: dropping QoS: publisher.QoSAtMostOnce from HARuntimeConfig
// makes every retraction and both documents go out at QoS 1 and fails this
// test on all 31 records.
func TestEveryDocumentAndRetractionIsAtMostOnce(t *testing.T) {
	_, wire := capturePublishWire(t, goldenUnit(), goldenReport())

	retractions, documents := 0, 0
	for _, rec := range wire {
		if !strings.HasPrefix(rec.Topic, "homeassistant/") {
			continue
		}
		if rec.QoS != mqtt.QoS0 {
			t.Errorf("%s published at QoS %v, want QoS0 — the installed base's delivery guarantee moved", rec.Topic, rec.QoS)
		}
		if !rec.Retain {
			t.Errorf("%s published without the retain flag; a discovery config and its retraction are both retained acts", rec.Topic)
		}
		if len(rec.Payload) == 0 {
			retractions++
			continue
		}
		documents++
	}
	if retractions != 29 {
		t.Errorf("counted %d legacy retractions, want 29", retractions)
	}
	if documents != 2 {
		t.Errorf("counted %d device documents, want 2", documents)
	}
}

// TestEveryRenderedDocumentValidates keeps the hard constraint PR #41
// established: discovery.Validate accepts every payload this bridge
// publishes, and both documents. This bridge had never validated its own
// output before that PR, and Home Assistant's discovery schemas are
// extra=REMOVE_EXTRA — a key it does not declare vanishes on arrival with no
// error on the wire and no line in any log.
func TestEveryRenderedDocumentValidates(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}

	cases := append([]identityCase{{"fleet", goldenUnit(), goldenReport()}}, identityCases()...)
	for _, c := range cases {
		points := resolvePoints(t, c.dev, c.report)
		owners := process.Owners(points)
		if len(owners) == 0 {
			t.Errorf("%s: resolved no device owners", c.name)
		}
		for _, packSN := range owners {
			bundle, err := r.Bundle(c.dev, c.report, packSN, points)
			if err != nil {
				t.Fatalf("%s/%s: Bundle: %v", c.name, packSN, err)
			}
			if bundle == nil {
				t.Errorf("%s/%s: rendered no document for an owner that mints entities", c.name, packSN)
				continue
			}
			if err := discovery.Validate(bundle); err != nil {
				t.Errorf("%s/%s: the document this bridge publishes does not validate: %v", c.name, packSN, err)
			}
			for key, comp := range bundle.Components {
				if err := discovery.ValidateBody(comp.Platform, mustBody(t, comp)); err != nil {
					t.Errorf("%s/%s: component %q does not validate: %v", c.name, packSN, key, err)
				}
			}
		}
	}
}

// mustBody re-decodes a component into the generic map ValidateBody takes.
func mustBody(t *testing.T, comp discovery.Component) map[string]any {
	t.Helper()
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshal component: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal component: %v", err)
	}
	return body
}

// TestASubDeviceGetsItsOwnDocument states the hierarchy rule as a behaviour.
//
// A battery pack is not a component of its parent's document — it is a
// device, with its own node id, its own `identifiers` and its own retained
// document naming the parent in `via_device`. Rendering it as components of
// the unit would flatten every pack value onto the SolarFlow, which is the
// shape the per-entity form's via_device already avoided.
func TestASubDeviceGetsItsOwnDocument(t *testing.T) {
	docs := capturedDocuments(t, capturePublish(t, goldenUnit(), goldenReport()))

	var parents, children int
	for topic, doc := range docs {
		dev, _ := doc["device"].(map[string]any)
		if via, _ := dev["via_device"].(string); via != "" {
			children++
			if via != "zendure2mqtt_"+goldenUnit().SN {
				t.Errorf("%s: via_device %q does not name the unit", topic, via)
			}
			continue
		}
		parents++
	}
	if parents != 1 || children != 1 {
		t.Errorf("documents: %d parents and %d sub-devices, want 1 and 1", parents, children)
	}
}

// TestBundleIsNotPublishedForAnOwnerWithoutEntities guards the one payload
// this bridge must never write. An empty `components` map is not "a device
// with no entities" to Home Assistant, it is the instruction to remove every
// entity of that device — and the harender renderer answers nil rather than
// an empty document so the mistake is not reachable from the publish path.
func TestBundleIsNotPublishedForAnOwnerWithoutEntities(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	dev := goldenUnit()
	got, err := r.Bundle(dev, &model.Report{SN: dev.SN}, "NOSUCHPACK", resolvePoints(t, dev, goldenReport()))
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if got != nil {
		t.Errorf("rendered a document with %d components for an owner with no entities", len(got.Components))
	}
}

// compile-time reminders that the two collaborators this step introduced are
// the library's own types and not shapes this repository restates.
var (
	_ hass.BundleWriter   = (*publisher.Runtime)(nil)
	_ hass.BundleRenderer = harender.Renderer{}
	_ hamodel.Entity      = (*harender.Entity)(nil)
	_ publisher.Transport = hagomqtt.Transport((*capturingClient)(nil))
	_ source.Backend      = (*goldenBackend)(nil)
)

// TestEveryEntityReferencesATopicThisBridgePublishes states, as a check
// rather than as a reading of the payloads, the one availability fact whose
// failure is total and silent.
//
// Home Assistant's availability_mode defaults to "all": every source an
// entity names must say online, so one referenced topic that nobody ever
// publishes leaves that entity permanently unavailable — with no error
// anywhere, because there is nothing wrong with the config. The shared
// library's own default availability shape is {LevelBridge, LevelDevice},
// and the per-device level of it names a topic two sibling bridges never
// publish; adopting that default here would have greyed out the whole fleet.
//
// This bridge does not have that shape and the assertion below is what says
// so rather than assumes it: harender states hamodel.NoAvailability(), so no
// `availability` array is rendered at all, and every entity's only
// availability source is the flat availability_topic — one string,
// coordinator.BridgeStatusTopic, which is simultaneously the Last Will
// publisher.Runtime.Will() returns and the topic AnnounceOnline and
// AnnounceOffline write. discovery.BundleAvailabilityTopics reads both
// spellings, so a later move to the array form is checked by this test
// without a change to it.
//
// The predicate is the set of topics this daemon actually publishes to,
// stated here as the one topic it is. Nothing is checked at run time: a
// production guard would add a failure path to the publish of a fleet whose
// answer cannot change between builds. This is a pin, and it moves no byte.
//
// Mutation check, each run five times and caught five times: making
// harender.Context.Availability return a per-device entry rather than nil,
// which is the library's own {LevelBridge, LevelDevice} default in the shape
// that greyed out two sibling fleets; and pointing Entity's availTopic at a
// per-device topic instead of Layout().Bridge(). A third mutation —
// returning the bridge topic from harender.Layout.Availability rather than
// "" — survives, and correctly: Context.Availability returns nil before that
// method is ever consulted, so it renders nothing. That is a fact about the
// rendering path rather than a hole in this pin, and it is recorded here so
// the next reader does not have to re-derive it.
func TestEveryEntityReferencesATopicThisBridgePublishes(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	published := func(topic string) bool {
		return topic == BridgeStatusTopic("zendure2mqtt")
	}

	cases := append([]identityCase{{"fleet", goldenUnit(), goldenReport()}}, identityCases()...)
	for _, c := range cases {
		points := resolvePoints(t, c.dev, c.report)
		for _, packSN := range process.Owners(points) {
			bundle, err := r.Bundle(c.dev, c.report, packSN, points)
			if err != nil {
				t.Fatalf("%s/%s: Bundle: %v", c.name, packSN, err)
			}
			if bundle == nil {
				continue
			}
			topics := discovery.BundleAvailabilityTopics(bundle)
			if len(topics) != 1 || topics[0] != BridgeStatusTopic("zendure2mqtt") {
				t.Errorf("%s/%s: the document's entities reference %v, want the one bridge status topic %q",
					c.name, packSN, topics, BridgeStatusTopic("zendure2mqtt"))
			}
			if err := discovery.CheckBundleAvailability(bundle, published); err != nil {
				t.Errorf("%s/%s: %v — every entity naming that topic is permanently unavailable, with nothing in any log saying so",
					c.name, packSN, err)
			}
		}
	}
}

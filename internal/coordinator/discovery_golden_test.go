// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

var updateDiscoveryGolden = flag.Bool("update-discovery-golden", false,
	"rewrite the pinned discovery payloads and state-topic lists")

var (
	goldenUnitPath       = filepath.Join("testdata", "discovery_unit.json")
	goldenPackPath       = filepath.Join("testdata", "discovery_pack.json")
	goldenIdentityPath   = filepath.Join("testdata", "discovery_identity.json")
	goldenStateTopicPath = filepath.Join("testdata", "state_topics.json")
)

// goldenEntry is one retained discovery publish, addressed the way the wire
// addresses it.
//
// The topic is pinned alongside the body deliberately. The two ways this
// migration can break most quietly are invisible in the body: a changed
// unique_id moves the config to a new topic, and Home Assistant then orphans
// the old entity and creates a new one beside it with nothing in any log.
//
// The payload is stored decoded rather than as raw bytes so the pin stays
// reviewable — a human has to be able to read the diff and decide whether a
// change is wanted. Comparison is on the canonical re-encoding of both sides,
// which is exact for every key and value; only whitespace, which no consumer
// sees, is out of scope. Storing it raw would compare nothing at all:
// json.MarshalIndent reformats an embedded json.RawMessage, so the pin and
// the capture would differ on every write.
type goldenEntry struct {
	Topic   string         `json:"topic"`
	Payload map[string]any `json:"payload"`
}

// discardLogger keeps the pins quiet: the publish path logs a debug line per
// report and a warning per skipped point, and neither is what is being pinned.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// canonicalJSON is the form both sides of a payload comparison are reduced to.
func canonicalJSON(t *testing.T, body map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// wireRecord is one publish as the wire saw it: not just the bytes, but the
// two flags no pin in this repository covered before the state plane moved
// onto the shared library.
//
// The QoS is recorded because it is the one thing the migration could change
// without changing a payload. publisher.StateConfig's zero value means
// "unset" and resolves to QoS 1, and this bridge has always published at
// QoS 0, so "nothing moved" has to include the level — and a byte pin cannot
// see it.
type wireRecord struct {
	Topic   string
	Payload []byte
	QoS     mqtt.QoS
	Retain  bool
}

// capturingClient records every publish so a pin can read what the bridge put
// on the wire. Subscribe/Unsubscribe are inert: the orphan reconcile is not
// what these tests pin, and it is short-circuited before it subscribes (see
// capturePublish).
type capturingClient struct {
	mu       sync.Mutex
	payloads map[string][]byte
	records  []wireRecord
}

func (c *capturingClient) Publish(_ context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.payloads == nil {
		c.payloads = map[string][]byte{}
	}
	c.payloads[topic] = append([]byte(nil), payload...)
	c.records = append(c.records, wireRecord{
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
		QoS:     qos,
		Retain:  retain,
	})
	return nil
}

// wire returns every publish the client saw, in order.
func (c *capturingClient) wire() []wireRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]wireRecord(nil), c.records...)
}

func (c *capturingClient) Subscribe(context.Context, string, mqtt.QoS, mqtt.MessageHandler, ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	return mqtt.SubscribeResult{}, nil
}

func (c *capturingClient) Unsubscribe(context.Context, string) error { return nil }

// goldenBackend is the collaborator [New] needs to learn the device list. It
// is a stub of the transport, not of anything this pin measures: every byte
// the pin records comes from process.Resolve, Coordinator.switchPoints and
// hass.Discovery.
type goldenBackend struct{ devices []source.Device }

func (b *goldenBackend) Devices() []source.Device { return b.devices }

func (b *goldenBackend) Run(ctx context.Context, _ source.Handler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (b *goldenBackend) Read(context.Context, source.Device) (*model.Report, error) {
	return nil, context.Canceled
}

func (b *goldenBackend) Write(context.Context, source.Device, map[string]any) error { return nil }

// goldenCatalog loads the catalog the daemon ships, not a test catalog.
//
// This is load-bearing. zendure.yaml is the documented extension point, so a
// catalog edit is the single most likely source of an accidental payload
// change — and the inline testCatalog used elsewhere in these tests cannot
// see one.
func goldenCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadFile(filepath.Join("..", "..", "zendure.yaml"))
	if err != nil {
		t.Fatalf("load zendure.yaml: %v", err)
	}
	return cat
}

// capturePublish runs the daemon's real publish path — process.Resolve over
// the shipped catalog, Coordinator.switchPoints, hass.Discovery.Publish and
// the state fan-out — for one device and one report, and returns everything
// it put on the wire.
//
// runCtx is handed an already-cancelled context so reconcileOrphans returns at
// its first guard instead of subscribing and collecting for two seconds. The
// sweep is a later step's concern (ADR 0070 phase 5, step 4) and pinning it
// would need a broker, not a capture.
func capturePublish(t *testing.T, dev source.Device, report *model.Report) map[string][]byte {
	t.Helper()
	captured, _ := capturePublishWire(t, dev, report)
	return captured
}

// newStatePlane builds the state publisher the daemon builds, over the
// capturing client: QoS 0 stated with publisher.QoSAtMostOnce, the raw
// encoding, and this bridge's own command filter as the collision guard.
//
// It restates the composition root's configuration rather than importing it,
// because cmd/zendure2mqtt is a main package and nothing can. What keeps the
// two in step is TestEveryPublishIsAtMostOnce reading the level off the wire
// rather than off the config: a daemon configured differently from this rig
// would publish at a different QoS, which that pin fails on.
func newStatePlane(pub *capturingClient, root string) *publisher.StatePublisher {
	return publisher.NewStatePublisher(hagomqtt.Transport(pub), publisher.StateConfig{
		QoS:            publisher.QoSAtMostOnce,
		Encoding:       discovery.RawEncoding,
		CommandFilters: []string{CommandFilter(root)},
		Logger:         discardLogger(),
	})
}

// newHARuntime builds the Home Assistant runtime the daemon builds, over the
// capturing client: the discovery prefix, this bridge's own status topic and
// QoS 0 stated with publisher.QoSAtMostOnce.
func newHARuntime(pub *capturingClient, root string) *publisher.Runtime {
	return publisher.New(hagomqtt.Transport(pub), publisher.Config{
		Prefix:      "homeassistant",
		StatusTopic: BridgeStatusTopic(root),
		QoS:         publisher.QoSAtMostOnce,
		Logger:      discardLogger(),
	})
}

// capturePublishWire is [capturePublish] plus the ordered wire log, for the
// pins that assert on the QoS and the retain flag rather than on the bytes.
func capturePublishWire(t *testing.T, dev source.Device, report *model.Report) (map[string][]byte, []wireRecord) {
	t.Helper()

	pub := &capturingClient{}
	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	rt := newHARuntime(pub, cfg.MQTTTopic)
	c := New(Deps{
		Cfg:        cfg,
		Backend:    &goldenBackend{devices: []source.Device{dev}},
		MQTT:       pub,
		Catalog:    goldenCatalog(t),
		HASS:       hass.New("homeassistant", cfg.MQTTTopic, cfg.Language, rt, discardLogger()),
		Logger:     discardLogger(),
		HARuntime:  rt,
		StatePlane: newStatePlane(pub, cfg.MQTTTopic),
	})
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	c.runCtx = dead

	c.publish(context.Background(), dev, report)

	pub.mu.Lock()
	out := make(map[string][]byte, len(pub.payloads))
	for k, v := range pub.payloads {
		out[k] = v
	}
	pub.mu.Unlock()
	return out, pub.wire()
}

// discoveryEntries decodes the captured discovery configs, keyed by the
// unique_id they carry.
//
// unique_id is the key on purpose: it is what Home Assistant keys its entity
// registry on, and it has no migration path. Keying the pin on it means a
// re-keyed entity shows up as one row removed and one row added — the loudest
// diff the format can produce — rather than as a quiet field change buried in
// a payload.
func discoveryEntries(t *testing.T, captured map[string][]byte) map[string]goldenEntry {
	t.Helper()
	out := map[string]goldenEntry{}
	for topic, raw := range captured {
		if !strings.HasPrefix(topic, "homeassistant/") {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: unmarshal config: %v", topic, err)
		}
		uid, _ := body["unique_id"].(string)
		if uid == "" {
			t.Fatalf("%s: config carries no unique_id", topic)
		}
		if prev, dup := out[uid]; dup {
			t.Fatalf("unique_id %q published on two topics: %s and %s", uid, prev.Topic, topic)
		}
		out[uid] = goldenEntry{Topic: topic, Payload: body}
	}
	return out
}

// splitByPack partitions discovery entries into the main unit's and the
// battery packs', so the two HA devices get one golden file each — the shapes
// diverge (via_device, sw_version, a six-level state topic) and a reviewer
// reading a diff should not have to work out which device a row belongs to.
func splitByPack(entries map[string]goldenEntry) (unit, pack map[string]goldenEntry) {
	unit, pack = map[string]goldenEntry{}, map[string]goldenEntry{}
	for uid, e := range entries {
		dev, _ := e.Payload["device"].(map[string]any)
		if _, viaPack := dev["via_device"]; viaPack {
			pack[uid] = e
			continue
		}
		unit[uid] = e
	}
	return unit, pack
}

// goldenUnit is the device the fleet pin is taken from: a SolarFlow 2400 AC
// reached locally, so the device block carries configuration_url, with one
// battery pack attached.
func goldenUnit() source.Device {
	return source.Device{
		SN:       "SF2400AC0012345",
		DeviceID: "SF2400AC0012345",
		Model:    "SolarFlow 2400 AC",
		Address:  "192.168.1.50",
	}
}

// goldenReport is one plausible telemetry snapshot carrying every one of the
// 20 main-unit properties the shipped catalog maps, plus one packData entry
// carrying all seven pack properties.
//
// Every value is a raw device value, not a published one: hyperTmp is
// deci-kelvin, BatVolt is centivolt, socSet/minSoc are deci-percent, acMode
// and smartMode are value-map codes. The scaling and mapping in the pinned
// payloads and states therefore comes from catalog.Entry + process.applyEntry,
// which is the point.
//
// softVersion is deliberately present in the pack. It has no catalog entry, so
// it mints no entity — but it does become a raw "battery" point with a state
// topic, and it is the only source of the pack device block's sw_version. Both
// of those are pinned by nothing today.
func goldenReport() *model.Report {
	return &model.Report{
		SN:      "SF2400AC0012345",
		Product: "solarFlow2400AC",
		Properties: map[string]any{
			// now
			"electricLevel":   float64(55),
			"solarInputPower": float64(812),
			"packInputPower":  float64(0),
			"outputPackPower": float64(640),
			"gridInputPower":  float64(1200),
			"outputHomePower": float64(350),
			"gridOffPower":    float64(0),
			"remainOutTime":   float64(214),
			"hyperTmp":        float64(3011),
			"BatVolt":         float64(5039),
			// static
			"rssi":           float64(-58),
			"chargeMaxLimit": float64(2400),
			"packNum":        float64(1),
			// config
			"acMode":          float64(1),
			"inputLimit":      float64(1200),
			"outputLimit":     float64(800),
			"socSet":          float64(950),
			"minSoc":          float64(150),
			"inverseMaxPower": float64(2400),
			"smartMode":       float64(1),
		},
		PackData: []map[string]any{{
			"sn":          "AO4H2301X01",
			"socLevel":    float64(57),
			"power":       float64(-640),
			"maxTemp":     float64(2981),
			"totalVol":    float64(5039),
			"maxVol":      float64(337),
			"minVol":      float64(334),
			"batcur":      float64(208),
			"softVersion": float64(4109),
		}},
	}
}

// TestDiscoveryPayloadsArePinned pins the topic and the full payload of every
// Home Assistant discovery config this bridge publishes for one SolarFlow
// 2400 AC with one battery pack: 29 entities across 2 HA devices — 13 unit
// sensors, 5 numbers, 2 selects, 2 virtual switches and 7 pack sensors, from
// the 27 entries of the shipped zendure.yaml plus the two switches minted in
// internal/virtual.
//
// It exists because of what this repository did not have. Before this test
// there was no testdata directory anywhere in the tree and no golden file of
// any kind; the whole guard against a changed discovery payload was four
// asserted keys on one sensor plus three topic strings, and the tests built
// their Discovery with root "zendure" while the shipped default is
// "zendure2mqtt" — so the one pinned unique_id literal had the shape of no
// string any deployment ever publishes. It pinned the formula, not the output.
//
// That is not survivable for the work this is a prerequisite of (ADR 0070
// phase 5: moving this bridge onto the shared go-hamqtt discovery model).
// Home Assistant's MQTT discovery schemas are extra=REMOVE_EXTRA: an
// undeclared key is dropped on arrival with no error on the wire and no log
// line. And HA keys its entity registry on unique_id and its device registry
// on identifiers, neither of which has a migration path. The reference
// consumer migrated twelve discovery planes onto the same library, byte-pinned
// every one of them first, and the pins caught four real defects that code
// review had not — four planes that would have silently grown a stray
// "platform" key among them.
//
// So this pins the whole payload, not a chosen set of keys. Everything on the
// following list was pinned by nothing before: state_topic and command_topic
// as they appear in a config (only the topic builder was covered, and
// discovery calls it from a second site), availability_topic,
// payload_available, payload_not_available, device.identifiers, via_device,
// model_id, serial_number, configuration_url, sw_version, device_class,
// state_class, unit_of_measurement, min/max/step, options, payload_on and
// payload_off — and 28 of the 29 entities.
//
// The inputs are real: the shipped zendure.yaml through catalog.LoadFile, a
// raw report through process.Resolve, the virtual switches through
// Coordinator.switchPoints, and hass.Discovery.Publish itself. Nothing here
// re-implements a payload.
//
// Scope. This is one unit with one pack, which is what CI can reproduce. A
// second pack adds seven entities and one sub-device and changes no shape, so
// it is not pinned. The orphan sweep is not pinned (it needs a broker, and it
// is step 4's concern). The German label set is not pinned; LANGUAGE=de
// changes "name" and a select's "options" and nothing else about the shape.
//
// A diff here is a regression until someone shows otherwise. Refresh with:
//
//	go test ./internal/coordinator/ -run ArePinned -update-discovery-golden
func TestDiscoveryPayloadsArePinned(t *testing.T) {
	captured := capturePublish(t, goldenUnit(), goldenReport())
	unit, pack := splitByPack(discoveryEntries(t, captured))

	if *updateDiscoveryGolden {
		writeGolden(t, goldenUnitPath, unit)
		writeGolden(t, goldenPackPath, pack)
		t.Logf("rewrote %s (%d) and %s (%d)", goldenUnitPath, len(unit), goldenPackPath, len(pack))
		return
	}

	compareGolden(t, goldenUnitPath, unit)
	compareGolden(t, goldenPackPath, pack)

	// The counts are asserted as well as the contents: a pin refreshed
	// carelessly could otherwise shrink to whatever the code still emits.
	if len(unit)+len(pack) != 29 {
		t.Errorf("published %d entities (%d unit + %d pack), want 29 — an entity appeared or vanished",
			len(unit)+len(pack), len(unit), len(pack))
	}
}

// identityCase is one device/report pair chosen because it lands on an input
// class no other test in this repository exercises.
type identityCase struct {
	name   string
	dev    source.Device
	report *model.Report
}

// identityCases are the four slug and identity divergences the phase-5
// measurement singled out, none of which any test covered before: every device
// name in every existing test is ASCII with no hyphen, and no test has ever
// had two battery packs.
//
// They are chosen from the diff between this bridge's slugify and the shared
// library's topic.Slug, not from intuition — the two disagree on "ü" (u vs
// ue), on non-German accents ("é" dropped vs e), on the hyphen (folded to "_"
// vs preserved), and on adjacent duplicate tokens (collapsed vs kept). Each
// row below changes default_entity_id and nothing else, which is exactly why
// swapping the function would ship green: default_entity_id is inert for an
// entity Home Assistant already has, and decisive for every new install.
func identityCases() []identityCase {
	oneSensor := func() *model.Report {
		return &model.Report{
			SN:         "SF2400AC0012345",
			Product:    "solarFlow2400AC",
			Properties: map[string]any{"electricLevel": float64(55)},
		}
	}
	named := func(name string) source.Device {
		d := goldenUnit()
		d.DeviceName = name
		return d
	}

	// A German operator's device name. slugify transliterates "ü" to "u";
	// topic.Slug expands it to "ue".
	umlaut := identityCase{"umlaut-u", named("Balkon Süd"), oneSensor()}

	// A non-German accent. slugify has no transliteration for "é" and folds
	// it to a separator, so "Café Nord" loses a character and gains an
	// underscore; topic.Slug renders it "e".
	accent := identityCase{"non-german-accent", named("Café Nord"), oneSensor()}

	// A hyphen in the device name. slugify folds it to "_"; topic.Slug
	// preserves it.
	hyphen := identityCase{"hyphen-in-name", named("Haus-Nord"), oneSensor()}

	// F8, and the only row here that is a live defect rather than a
	// divergence: validPackSN admits both "-" and "_", slugify folds both to
	// "_", so two packs whose serials differ only in that character produce
	// two distinct unique_ids and ONE default_entity_id. Home Assistant
	// resolves the collision by suffixing, silently. Both rows are pinned as
	// they are published today.
	packCollision := identityCase{"pack-serial-hyphen-vs-underscore", goldenUnit(), &model.Report{
		SN:         "SF2400AC0012345",
		Product:    "solarFlow2400AC",
		Properties: map[string]any{"packNum": float64(2)},
		PackData: []map[string]any{
			{"sn": "AB-12", "socLevel": float64(57)},
			{"sn": "AB_12", "socLevel": float64(58)},
		},
	}}

	return []identityCase{umlaut, accent, hyphen, packCollision}
}

// TestIdentityHazardPayloadsArePinned pins the discovery payloads for the four
// inputs whose identity strings sit on a divergence between this bridge's
// slugify and the slug of the library it is about to move onto, plus the one
// of those that is a defect today.
//
// Which pinned rows are defects, named so a later step can be seen to fix
// them rather than to have quietly changed them:
//
//   - "pack-serial-hyphen-vs-underscore" is F8 of the phase-5 measurement,
//     and it is pinned broken. Packs "AB-12" and "AB_12" both slug to
//     "ab_12", so the two rows carry distinct unique_ids —
//     zendure2mqtt_SF2400AC0012345_pack_AB-12_soc_level and
//     ..._pack_AB_12_soc_level — and the identical default_entity_id
//     sensor.zendure_sf2400ac0012345_pack_ab_12_soc_level. Two entities
//     compete for one entity_id and Home Assistant suffixes one of them
//     without saying so. The shared library's topic.Slug preserves the
//     hyphen specifically to avoid this collision class, so this is one row
//     that is expected to change — deliberately, in its own commit, with
//     this pin refreshed as the record of the fix.
//
//   - "non-german-accent" is the same class as the defect the library's own
//     slug documents ("Größe → gr_e"): slugify has no mapping for "é", so
//     the character is dropped and replaced by a separator. It is pinned as
//     the wrong-but-shipped answer.
//
// Each case also carries the two virtual switches, which take their
// default_entity_id seed from the same device name, so every divergence is
// pinned on three platforms rather than one.
//
// The other two rows — "umlaut-u" and "hyphen-in-name" — are not defects.
// They are correct, deliberate output that the shared library spells
// differently, and pinning them is what makes the difference visible when the
// swap is eventually taken.
//
// Refresh with:
//
//	go test ./internal/coordinator/ -run ArePinned -update-discovery-golden
func TestIdentityHazardPayloadsArePinned(t *testing.T) {
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
}

// TestStateTopicsArePinned pins the sorted list of the 30 state topics the
// bridge publishes for one unit with one pack.
//
// It is the half of the wire Home Assistant's registry does not protect. A
// changed unique_id orphans an entity visibly — it disappears and a new one
// appears. A changed state_topic leaves the entity exactly where it is and
// makes it permanently unknown, with nothing anywhere to say why.
//
// 30, not 29: every resolved point gets a state topic, including the ones with
// no catalog entry and therefore no entity. Here that is the pack's
// softVersion, published raw under the battery group — which is documented
// behaviour ("nothing is lost while the catalog is filled in") and was pinned
// by nothing.
//
// The bridge status topic is not in the list: it is announced by PublishOnline
// and PublishOffline, not by the publish path this captures. It is pinned
// indirectly, as every entity's availability_topic, by the payload pins.
//
// Refresh with:
//
//	go test ./internal/coordinator/ -run ArePinned -update-discovery-golden
func TestStateTopicsArePinned(t *testing.T) {
	captured := capturePublish(t, goldenUnit(), goldenReport())

	topics := make([]string, 0, len(captured))
	for topic := range captured {
		if strings.HasPrefix(topic, "homeassistant/") {
			continue // pinned with its payload by TestDiscoveryPayloadsArePinned
		}
		topics = append(topics, topic)
	}
	sort.Strings(topics)

	if *updateDiscoveryGolden {
		writeGoldenJSON(t, goldenStateTopicPath, topics)
		t.Logf("rewrote %s with %d topics", goldenStateTopicPath, len(topics))
		return
	}

	var want []string
	readGoldenJSON(t, goldenStateTopicPath, &want)
	if strings.Join(topics, "\n") != strings.Join(want, "\n") {
		t.Errorf("state topics changed\n got %v\nwant %v\n(a changed state topic leaves the entity in place and makes it permanently unknown)",
			topics, want)
	}
}

// compareGolden checks a captured entry set against its pin, reporting a
// missing row, an extra row and a changed row differently — the three mean
// different things on a Home Assistant registry.
func compareGolden(t *testing.T, path string, got map[string]goldenEntry) {
	t.Helper()
	var want map[string]goldenEntry
	readGoldenJSON(t, path, &want)

	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		w, pinned := want[k]
		if !pinned {
			t.Errorf("%s: %s is not in the pin — a new entity appeared, or an existing one was re-keyed; refresh the pin deliberately", path, k)
			continue
		}
		if got[k].Topic != w.Topic {
			t.Errorf("%s: %s: topic\n got %s\nwant %s\n(a changed config topic orphans the entity under it, silently)",
				path, k, got[k].Topic, w.Topic)
		}
		if g, wb := canonicalJSON(t, got[k].Payload), canonicalJSON(t, w.Payload); g != wb {
			t.Errorf("%s: %s: payload\n got %s\nwant %s", path, k, g, wb)
		}
	}
	for k := range want {
		if _, still := got[k]; !still {
			t.Errorf("%s: %s is in the pin but no longer published — an entity disappeared", path, k)
		}
	}
}

func writeGolden(t *testing.T, path string, entries map[string]goldenEntry) {
	t.Helper()
	writeGoldenJSON(t, path, entries)
}

func writeGoldenJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readGoldenJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // fixed test-fixture path
	if err != nil {
		t.Fatalf("read %s: %v (create it with -update-discovery-golden)", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

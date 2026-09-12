// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// statePlaneRig is one coordinator wired to a capturing client, for the pins
// that call publish more than once — which is the only way a dedup gate can
// be observed at all.
type statePlaneRig struct {
	coord *Coordinator
	pub   *capturingClient
	dev   source.Device
}

// newStatePlaneRig builds the daemon's real publish path over a capturing
// client: the shipped zendure.yaml, process.Resolve, the virtual switches,
// hass.Discovery and the state plane the composition root configures.
func newStatePlaneRig(t *testing.T) *statePlaneRig {
	t.Helper()
	pub := &capturingClient{}
	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	dev := goldenUnit()
	c := New(Deps{
		Cfg:        cfg,
		Backend:    &goldenBackend{devices: []source.Device{dev}},
		MQTT:       pub,
		Catalog:    goldenCatalog(t),
		HASS:       hass.New("homeassistant", cfg.MQTTTopic, cfg.Language, pub, discardLogger()),
		Logger:     discardLogger(),
		StatePlane: newStatePlane(pub, cfg.MQTTTopic),
	})
	// An already-cancelled runCtx keeps the orphan reconcile at its first
	// guard instead of subscribing for two seconds; the sweep has its own
	// pins.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	c.runCtx = dead
	return &statePlaneRig{coord: c, pub: pub, dev: dev}
}

// stateWrites counts the publishes the rig saw on state topics — everything
// outside the Home Assistant discovery prefix — and resets the log, so a
// caller can count one poll at a time.
func (r *statePlaneRig) stateWrites() int {
	n := 0
	for _, rec := range r.pub.wire() {
		if !strings.HasPrefix(rec.Topic, "homeassistant/") {
			n++
		}
	}
	r.pub.mu.Lock()
	r.pub.records = nil
	r.pub.mu.Unlock()
	return n
}

// TestEveryPublishIsAtMostOnce is the wire-level half of "nothing moved".
//
// Every release of this bridge has published every message at QoS 0, and
// publisher.StateConfig's zero value means *unset* and resolves to QoS 1 —
// so the one change this migration could have made without touching a
// payload byte is invisible to every golden file in this directory. This pin
// reads the level off the transport call itself, for the discovery configs
// and the state values alike, and asserts the retain flag in the same pass
// because a non-retained state leaves every entity at "unknown" after a Home
// Assistant restart.
//
// It is stated as an inherited choice being preserved rather than as an
// endorsement. QoS 0 is a real cost — the library's own
// publisher.availability.at_most_once warning names it: a marker lost at
// QoS 0 leaves an entity wrongly available until the next flip, which for a
// crash is never. Changing it is its own release.
func TestEveryPublishIsAtMostOnce(t *testing.T) {
	_, wire := capturePublishWire(t, goldenUnit(), goldenReport())
	if len(wire) == 0 {
		t.Fatal("no publishes captured")
	}
	for _, rec := range wire {
		if rec.QoS != mqtt.QoS0 {
			t.Errorf("%s published at QoS %v, want QoS0 — the installed base's delivery guarantee moved", rec.Topic, rec.QoS)
		}
		if !rec.Retain {
			t.Errorf("%s published without the retain flag — Home Assistant would read it as unknown after a restart", rec.Topic)
		}
	}
}

// TestStatePlaneSuppressesUnchangedValues is the migration's measurable win.
//
// Before this step, coordinator.publish re-published every resolved point
// retained on every poll — 30 writes per device every 15 s by default,
// roughly 7 000 an hour, nearly all byte-identical to what the broker already
// held. The dedup gate inside publisher.StatePublisher turns the unchanged
// ones into nothing.
//
// Mutation check: reverting Coordinator.publishState to a direct
// deps.MQTT.Publish makes the second poll write 30 again and this test fails
// on the second assertion. Narrowing the gate to a length comparison instead
// of bytes.Equal fails TestStatePlaneWritesAChangedValue below.
func TestStatePlaneSuppressesUnchangedValues(t *testing.T) {
	rig := newStatePlaneRig(t)
	ctx := context.Background()

	rig.coord.publish(ctx, rig.dev, goldenReport())
	first := rig.stateWrites()
	if first != 30 {
		t.Fatalf("first poll wrote %d state topics, want 30", first)
	}

	rig.coord.publish(ctx, rig.dev, goldenReport())
	if second := rig.stateWrites(); second != 0 {
		t.Errorf("second poll of an unchanged report wrote %d state topics, want 0", second)
	}
}

// TestStatePlaneWritesAChangedValue is the other half: the gate must not
// swallow a real change.
//
// One property moves and exactly one topic is written. A gate comparing
// anything coarser than the payload bytes — a length, a digest of the point
// list, the "already sent" set shape internal/hass uses for configs — passes
// the suppression test above and fails here.
func TestStatePlaneWritesAChangedValue(t *testing.T) {
	rig := newStatePlaneRig(t)
	ctx := context.Background()

	rig.coord.publish(ctx, rig.dev, goldenReport())
	rig.stateWrites()

	changed := goldenReport()
	changed.Properties["electricLevel"] = float64(56)
	rig.coord.publish(ctx, rig.dev, changed)

	wire := rig.pub.wire()
	var written []string
	for _, rec := range wire {
		if !strings.HasPrefix(rec.Topic, "homeassistant/") {
			written = append(written, rec.Topic)
		}
	}
	want := []string{"zendure2mqtt/SF2400AC0012345/now/electric_level/state"}
	if len(written) != 1 || written[0] != want[0] {
		t.Errorf("changed poll wrote %v, want %v", written, want)
	}
	for _, rec := range wire {
		if rec.Topic == want[0] && string(rec.Payload) != "56" {
			t.Errorf("payload = %q, want \"56\"", rec.Payload)
		}
	}
}

// TestReconnectReopensTheDedupGate pins the one case the gate must not win.
//
// A broker restarted without persistence drops every retained state while
// this process reconnects underneath. The cache would then answer "already
// published" for values the broker no longer holds and every entity would sit
// blank until its datapoint next changed — which on packNum or
// chargeMaxLimit is never. PublishOnline, which is wired to the lifecycle's
// OnConnect, resets the gate so the next poll writes the fleet once.
//
// Mutation check: deleting the StatePlane.Reset() call from PublishOnline
// leaves the post-reconnect poll at 0 writes and fails the last assertion.
func TestReconnectReopensTheDedupGate(t *testing.T) {
	rig := newStatePlaneRig(t)
	ctx := context.Background()

	rig.coord.publish(ctx, rig.dev, goldenReport())
	rig.stateWrites()
	rig.coord.publish(ctx, rig.dev, goldenReport())
	if n := rig.stateWrites(); n != 0 {
		t.Fatalf("steady state wrote %d, want 0", n)
	}

	rig.coord.PublishOnline(ctx)
	rig.stateWrites() // the bridge status announcement is not a state topic

	rig.coord.publish(ctx, rig.dev, goldenReport())
	if n := rig.stateWrites(); n != 30 {
		t.Errorf("post-reconnect poll wrote %d state topics, want 30 — a broker that lost its retained store would leave every entity blank", n)
	}
}

// TestEmptyStateValueStillRetracts pins the accident this bridge has always
// had, preserved deliberately.
//
// formatValue renders an empty string value as zero bytes, and publishing
// zero bytes retained is MQTT's retraction rather than a state — so such a
// point has always deleted the broker's retained value instead of writing
// one. publisher.StatePublisher.Publish refuses that with
// ErrEmptyStatePayload precisely because it is usually an accident, which
// would have turned a silent retraction into a warning log and no write at
// all. Routing it to Evict keeps the three wire values identical (empty
// payload, retained, QoS 0) and says it on purpose.
//
// Mutation check: dropping the empty-payload branch from publishState makes
// this test see no publish at all.
func TestEmptyStateValueStillRetracts(t *testing.T) {
	rig := newStatePlaneRig(t)
	topic := "zendure2mqtt/SF2400AC0012345/config/ac_mode/state"

	if !rig.coord.publishState(context.Background(), topic, formatValue("")) {
		t.Fatal("publishState reported no write for an empty value")
	}
	wire := rig.pub.wire()
	if len(wire) != 1 {
		t.Fatalf("wire = %d publishes, want 1", len(wire))
	}
	got := wire[0]
	if got.Topic != topic || len(got.Payload) != 0 || !got.Retain || got.QoS != mqtt.QoS0 {
		t.Errorf("retraction = %+v, want empty retained QoS0 payload on %s", got, topic)
	}
}

// TestStatePlaneRefusesAStateWriteIntoItsOwnCommandFilter pins the guard this
// bridge had no equivalent of.
//
// A state topic that fell inside `<root>/+/+/+/set` would be echoed straight
// back into Coordinator.handleSet — the process commanding itself, with
// nothing in any log saying so. The library refuses it with
// ErrStateCommandCollision, and the filter it checks against is
// CommandFilter, the same string Run subscribes.
//
// Mutation check: removing CommandFilters from the state config lets the
// publish through and fails the wire assertion.
func TestStatePlaneRefusesAStateWriteIntoItsOwnCommandFilter(t *testing.T) {
	rig := newStatePlaneRig(t)

	if rig.coord.publishState(context.Background(), "zendure2mqtt/SF1/config/inputLimit/set", []byte("1200")) {
		t.Error("publishState reported a write into this process's own command filter")
	}
	if wire := rig.pub.wire(); len(wire) != 0 {
		t.Errorf("a command topic reached the wire as state: %+v", wire)
	}
}

// TestPinnedStateTopicsClearTheCommandGuard checks the guard the other way
// round: none of the 30 topics this bridge actually publishes may collide
// with its own command filter, or the migration would have silenced the
// entire state plane.
//
// It reads the pinned topic list rather than re-deriving it, so a future
// topic-schema change is checked against the guard by the same pin that
// records the schema.
func TestPinnedStateTopicsClearTheCommandGuard(t *testing.T) {
	var pinned []string
	readGoldenJSON(t, goldenStateTopicPath, &pinned)
	if len(pinned) == 0 {
		t.Fatal("state-topic pin is empty")
	}
	filter := CommandFilter("zendure2mqtt")
	for _, topic := range pinned {
		if publisher.MatchFilter(filter, topic) {
			t.Errorf("state topic %s matches this bridge's own command filter %s", topic, filter)
		}
	}
	sorted := append([]string(nil), pinned...)
	sort.Strings(sorted)
	if strings.Join(sorted, "\n") != strings.Join(pinned, "\n") {
		t.Error("state-topic pin is not sorted; the guard check above assumes the pin is the published set")
	}
}

// TestFormatValueIsStillTheRenderer records why the state plane is handed
// bytes rather than values.
//
// publisher.StatePublisher.PublishValue would render the payload itself
// through publisher.RenderRawValue, which is the better function — it
// round-trips floats instead of truncating them — but it renders a Go bool as
// "true"/"false" and an int through strconv, where formatValue publishes
// "1"/"0" for a bool. Handing the library the bytes keeps the payload a
// decision of this repository, which is what the step is for; swapping the
// renderer is a payload change and belongs with the ones that move bytes.
//
// Mutation check: switching publishState to PublishValue makes the bool row
// below disagree.
func TestFormatValueIsStillTheRenderer(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{float64(55), "55"},
		{float64(50.39), "50.39"},
		{"charge", "charge"},
		{true, "1"},
		{false, "0"},
	}
	for _, tc := range cases {
		if got := string(formatValue(tc.value)); got != tc.want {
			t.Errorf("formatValue(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
	raw, err := publisher.RenderRawValue(true)
	if err != nil {
		t.Fatalf("RenderRawValue: %v", err)
	}
	if string(raw) == "1" {
		t.Error("publisher.RenderRawValue now renders a bool as \"1\"; the divergence this test records is gone and publishState could use PublishValue")
	}
}

// TestStateTopicMatchesTheComponentTheConfigDeclares closes the trap the
// library documents: layout.State(slot) is not the config's state_topic.
//
// The renderer projects state_topic only on the 22 platforms that accept the
// key, while a topic.Layout answers for all 32 — so on climate, button and
// the eight others the derived topic is one no config references and the
// entity stays unknown forever, with nothing logged. This bridge publishes
// sensor, number, select and switch, so the two answers agree today; the pin
// is that they agree, proved through publisher.ComponentStateTopic, which is
// the only string provably equal to the published config's.
//
// Mutation check: pointing the assertion at harender.Layout.State instead of
// ComponentStateTopic still passes today and would keep passing when a
// binary_sensor or a button is added (F7) — so the check is deliberately on
// the component, and TestLibraryRenderReproducesTheStateTopics in
// library_render_test.go is what proves the two are currently equal.
func TestStateTopicMatchesTheComponentTheConfigDeclares(t *testing.T) {
	captured := capturePublish(t, goldenUnit(), goldenReport())
	configs := discoveryEntries(t, captured)
	if len(configs) != 29 {
		t.Fatalf("captured %d configs, want 29", len(configs))
	}
	for uid, entry := range configs {
		declared, _ := entry.Payload["state_topic"].(string)
		if declared == "" {
			t.Errorf("%s: config declares no state_topic", uid)
			continue
		}
		if _, published := captured[declared]; !published {
			t.Errorf("%s: config points Home Assistant at %s, which nothing published", uid, declared)
		}
	}
}

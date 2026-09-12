// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// fakeBroker is a retained-message store with subscriptions: enough of a
// broker for the two things the birth plane and the orphan sweep are made of,
// and neither is expressible against a client that only records.
//
// Retained delivery happens inline in Subscribe, which is what a real broker
// does (it flushes the matching retained tree to a fresh subscriber straight
// after the acknowledgement) and what publisher.Runtime's snapshot window
// depends on — a window that opens before the subscription exists sees none
// of the messages it was opened for.
type fakeBroker struct {
	mu sync.Mutex
	// retained is the broker's retained tree, seeded by a test and updated
	// by every retained publish.
	retained map[string][]byte
	// subs maps a filter to its handler.
	subs map[string]mqtt.MessageHandler
	// wire is every publish, in order.
	wire []wireRecord
}

func newFakeBroker(retained map[string][]byte) *fakeBroker {
	seeded := make(map[string][]byte, len(retained))
	for k, v := range retained {
		seeded[k] = v
	}
	return &fakeBroker{retained: seeded, subs: map[string]mqtt.MessageHandler{}}
}

func (b *fakeBroker) Publish(_ context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	b.mu.Lock()
	b.wire = append(b.wire, wireRecord{
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
		QoS:     qos,
		Retain:  retain,
	})
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = append([]byte(nil), payload...)
		}
	}
	b.mu.Unlock()
	return nil
}

func (b *fakeBroker) Subscribe(_ context.Context, filter string, _ mqtt.QoS, h mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	b.mu.Lock()
	b.subs[filter] = h
	matching := make([]string, 0, len(b.retained))
	for topic := range b.retained {
		if publisher.MatchFilter(filter, topic) {
			matching = append(matching, topic)
		}
	}
	sort.Strings(matching)
	payloads := make([][]byte, len(matching))
	for i, topic := range matching {
		payloads[i] = append([]byte(nil), b.retained[topic]...)
	}
	b.mu.Unlock()

	for i, topic := range matching {
		h(&mqtt.Message{Topic: topic, Payload: payloads[i], Retain: true})
	}
	return mqtt.SubscribeResult{}, nil
}

func (b *fakeBroker) Unsubscribe(_ context.Context, filter string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, filter)
	return nil
}

// deliver injects one message to whichever subscription matches, the way a
// broker fans a live publish out.
func (b *fakeBroker) deliver(t *testing.T, topic string, payload []byte) {
	t.Helper()
	b.mu.Lock()
	var handlers []mqtt.MessageHandler
	for filter, h := range b.subs {
		if publisher.MatchFilter(filter, topic) {
			handlers = append(handlers, h)
		}
	}
	b.mu.Unlock()
	if len(handlers) == 0 {
		t.Fatalf("nothing is subscribed to %s", topic)
	}
	for _, h := range handlers {
		h(&mqtt.Message{Topic: topic, Payload: payload})
	}
}

// filters returns the currently installed subscription filters, sorted.
func (b *fakeBroker) filters() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.subs))
	for f := range b.subs {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// publishes returns every publish the broker saw, in order.
func (b *fakeBroker) publishes() []wireRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]wireRecord(nil), b.wire...)
}

// brokerRig is one coordinator over a fakeBroker, with a live runCtx — the
// orphan sweep and the birth subscription both need one.
type brokerRig struct {
	coord  *Coordinator
	broker *fakeBroker
	dev    source.Device
	root   string
}

func newBrokerRig(t *testing.T, retained map[string][]byte) *brokerRig {
	t.Helper()
	broker := newFakeBroker(retained)
	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	dev := goldenUnit()
	rt := publisher.New(hagomqtt.Transport(broker), publisher.Config{
		Prefix:      "homeassistant",
		StatusTopic: BridgeStatusTopic(cfg.MQTTTopic),
		QoS:         publisher.QoSAtMostOnce,
		Logger:      discardLogger(),
	})
	c := New(Deps{
		Cfg:       cfg,
		Backend:   &goldenBackend{devices: []source.Device{dev}},
		MQTT:      broker,
		Catalog:   goldenCatalog(t),
		HASS:      hass.New("homeassistant", cfg.MQTTTopic, cfg.Language, rt, discardLogger()),
		Logger:    discardLogger(),
		HARuntime: rt,
		StatePlane: publisher.NewStatePublisher(hagomqtt.Transport(broker), publisher.StateConfig{
			QoS:            publisher.QoSAtMostOnce,
			CommandFilters: []string{CommandFilter(cfg.MQTTTopic)},
			Logger:         discardLogger(),
		}),
	})
	c.runCtx = t.Context()
	t.Cleanup(rt.Close)
	return &brokerRig{coord: c, broker: broker, dev: dev, root: cfg.MQTTTopic}
}

// seedFleet publishes one poll's worth of configs and state without letting
// the automatic orphan reconcile run.
//
// The suppression is the point, not a convenience: publish schedules a
// reconcile of its own on a goroutine, and publisher.Runtime serialises
// snapshot windows, so a test that then opens its own window either waits the
// background one out or wins the race and runs with a different claim set.
// Both outcomes are timing-dependent, which is how a sweep pin becomes a
// flake. A dead runCtx stops the scheduled reconcile at its first guard; the
// live one is restored for the pass the test drives itself.
func (r *brokerRig) seedFleet(t *testing.T) map[string]bool {
	t.Helper()
	live := r.coord.runCtx
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	r.coord.runCtx = dead
	r.coord.publish(context.Background(), r.dev, goldenReport())
	r.coord.runCtx = live

	published := map[string]bool{}
	for _, topic := range r.coord.deps.HARuntime.Declared() {
		published[topic] = true
	}
	return published
}

// TestBridgeStatusTopicIsOneString is the cross-check the library refuses a
// disagreement on, run here because this bridge does not hand the runtime a
// topic.Layout in production.
//
// One string is read by four parties: the Last Will configured at CONNECT,
// publisher.Runtime's availability announcements, every published entity's
// flat availability_topic, and harender's Layout.Bridge. Under the default
// availability_mode "all" a single typo greys out the entire fleet with
// nothing on the wire naming the cause, which is why publisher.New panics on
// a StatusTopic that disagrees with its Layout.
//
// Mutation check: changing BridgeStatusTopic's formula fails the harender
// comparison, the pinned availability_topic in the golden files, and the
// publisher.New construction below — which panics rather than returning.
func TestBridgeStatusTopicIsOneString(t *testing.T) {
	const root = "zendure2mqtt"
	want := harender.Layout{Root: root}.Bridge()
	if got := BridgeStatusTopic(root); got != want {
		t.Fatalf("BridgeStatusTopic = %q, want harender.Layout.Bridge() = %q", got, want)
	}

	broker := newFakeBroker(nil)
	rt := publisher.New(hagomqtt.Transport(broker), publisher.Config{
		Prefix: "homeassistant",
		// Both stated: with a Layout set, the library panics on a
		// StatusTopic that disagrees with it. Passing both is the assertion.
		StatusTopic: BridgeStatusTopic(root),
		Layout:      harender.Layout{Root: root},
		QoS:         publisher.QoSAtMostOnce,
		Logger:      discardLogger(),
	})
	if got := rt.BridgeTopic(); got != want {
		t.Errorf("Runtime.BridgeTopic = %q, want %q", got, want)
	}

	// And the string the pinned configs actually carry.
	captured := capturePublish(t, goldenUnit(), goldenReport())
	for uid, entry := range discoveryEntries(t, captured) {
		if got, _ := entry.Payload["availability_topic"].(string); got != want {
			t.Errorf("%s: availability_topic = %q, want %q", uid, got, want)
		}
	}
}

// TestWillIsTheRuntimesStatement pins the Last Will as data the runtime
// produces rather than a literal in main.go.
//
// The measured defect in two sibling bridges is a will whose topic no
// published entity references: the broker dutifully writes "offline" on a
// hard crash and every entity in Home Assistant stays available forever,
// showing the last value it ever saw. A will nobody reads is
// indistinguishable from no will at all. Reading it off the runtime is what
// makes that unreachable — the same topic and the same payload
// AnnounceOffline writes.
//
// The QoS is asserted too: the will is the one place a publisher.QoS crosses
// back out to the MQTT client, and it must still be the wire's 0.
//
// Mutation check: configuring the runtime with publisher.QoSUnset makes
// Will().QoS 1 and fails here; a StatusTopic other than BridgeStatusTopic
// fails the topic assertion and TestBridgeStatusTopicIsOneString.
func TestWillIsTheRuntimesStatement(t *testing.T) {
	rig := newBrokerRig(t, nil)
	will, err := rig.coord.deps.HARuntime.Will()
	if err != nil {
		t.Fatalf("Will: %v", err)
	}
	if will.Topic != BridgeStatusTopic(rig.root) {
		t.Errorf("will topic = %q, want %q", will.Topic, BridgeStatusTopic(rig.root))
	}
	if string(will.Payload) != "offline" {
		t.Errorf("will payload = %q, want \"offline\"", will.Payload)
	}
	if will.QoS != 0 {
		t.Errorf("will QoS = %d, want 0", will.QoS)
	}
	if !will.Retain {
		t.Error("will is not retained; a Home Assistant subscribing after the crash would never see it")
	}
}

// TestAvailabilityAnnouncementsAreUnchanged pins the two bridge-status
// publishes byte for byte, because they moved from a hand-written
// client.Publish to publisher.Runtime.
//
// Both are the same three wire values as before: the retained payload
// "online" or "offline" on <root>/bridge/status at QoS 0. The offline one is
// published deliberately on a graceful stop, since the Last Will only fires
// on an ungraceful one — which this bridge got right before the migration and
// must not lose in it.
func TestAvailabilityAnnouncementsAreUnchanged(t *testing.T) {
	rig := newBrokerRig(t, nil)
	ctx := t.Context()

	rig.coord.PublishOnline(ctx)
	rig.coord.PublishOffline(ctx)

	got := rig.broker.publishes()
	want := []wireRecord{
		{Topic: "zendure2mqtt/bridge/status", Payload: []byte("online"), QoS: mqtt.QoS0, Retain: true},
		{Topic: "zendure2mqtt/bridge/status", Payload: []byte("offline"), QoS: mqtt.QoS0, Retain: true},
	}
	if len(got) != len(want) {
		t.Fatalf("wire = %+v, want 2 publishes", got)
	}
	for i := range want {
		if got[i].Topic != want[i].Topic || !bytes.Equal(got[i].Payload, want[i].Payload) ||
			got[i].QoS != want[i].QoS || got[i].Retain != want[i].Retain {
			t.Errorf("publish %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestBirthMessageReplaysTheDeclaredConfigs is F6 of the ADR 0070 phase-5
// measurement, fixed.
//
// This bridge had no Home Assistant birth subscription at all: all four of
// its Subscribe sites were accounted for and none was <hass_base>/status.
// Discovery is retained, so an HA restart was survived — but a broker
// restarted without persistence, or one whose retained store was cleared,
// left Home Assistant with no entities until the daemon was restarted, and
// nothing in the logs said so.
//
// Adopting publisher.Runtime.WatchBirth adds a subscription, not a publish.
// The replay it performs on the rising edge does publish — the same 29
// retained configs, byte-identical to what this process already declared —
// and that is the fix rather than a side effect.
//
// Mutation check: removing the WatchBirth call from Run leaves nothing
// subscribed to homeassistant/status and fakeBroker.deliver fails the test
// outright; treating "offline" as a birth (dropping the payload check in the
// library) would make the second half of this test see a replay it must not.
func TestBirthMessageReplaysTheDeclaredConfigs(t *testing.T) {
	rig := newBrokerRig(t, nil)
	ctx := t.Context()

	// One poll, so there is something declared to replay.
	rig.seedFleet(t)
	if err := rig.coord.watchBirth(ctx); err != nil {
		t.Fatalf("watchBirth: %v", err)
	}
	declared := rig.coord.deps.HARuntime.Declared()
	if len(declared) != 29 {
		t.Fatalf("runtime declared %d configs, want 29 — the sweep and the resync both read this set", len(declared))
	}

	// Home Assistant going down must not trigger anything: the configs it
	// will re-read are already retained.
	rig.broker.deliver(t, "homeassistant/status", []byte("offline"))
	if n := configPublishes(rig.broker.publishes(), len(declared)); n != 0 {
		t.Errorf("a death message replayed %d configs, want 0", n)
	}

	rig.broker.deliver(t, "homeassistant/status", []byte("online"))
	// The replay runs on the runtime's own worker, off the read loop —
	// deliberately, since each publish would otherwise wait on an
	// acknowledgement only the read loop could deliver. Close drains it.
	rig.coord.deps.HARuntime.Close()

	replayed := map[string]bool{}
	for _, rec := range rig.broker.publishes()[len(declared):] {
		if strings.HasPrefix(rec.Topic, "homeassistant/") {
			replayed[rec.Topic] = true
			if rec.QoS != mqtt.QoS0 || !rec.Retain {
				t.Errorf("replayed %s at QoS %v retain=%v, want QoS0 retained", rec.Topic, rec.QoS, rec.Retain)
			}
		}
	}
	if len(replayed) != len(declared) {
		t.Fatalf("birth replayed %d configs, want %d", len(replayed), len(declared))
	}
	for _, topic := range declared {
		if !replayed[topic] {
			t.Errorf("%s was declared but not replayed on the birth message", topic)
		}
	}
}

// configPublishes counts the discovery-config publishes after the first skip
// records.
func configPublishes(wire []wireRecord, skip int) int {
	n := 0
	for _, rec := range wire[min(skip, len(wire)):] {
		if strings.HasPrefix(rec.Topic, "homeassistant/") {
			n++
		}
	}
	return n
}

// TestOrphanSweepRetractsOnlyThisDevicesOwnStaleConfigs is the pin the
// measurement asked this step for: the sweep can retract a live config if
// ownership is wrong, and no golden file covers it.
//
// The retained tree below is deliberately hostile. It holds, beside this
// device's 29 live configs:
//
//   - a stale config of this device, in this bridge's namespace, that the
//     current catalog no longer publishes — the only topic that may be
//     cleared;
//   - a stale config of a *second* Zendure unit, which this device's pass
//     must leave for that device's own pass. A fleet-wide ownership
//     predicate would clear it during the poll in which only the first unit
//     has published, and the second unit's entities would vanish once per
//     boot;
//   - a foreign integration's config under the same discovery prefix;
//   - a config whose topic sits in this bridge's namespace but whose payload
//     belongs to somebody else, which is why ownership is checked on the
//     payload and not only on the topic;
//   - a five-segment config and a device bundle, neither of which is a form
//     this bridge publishes, and which therefore belong to another writer or
//     to a later migration step;
//   - an already-empty retained topic, which the broker is clearing anyway.
//
// Mutation check: widening the Owns predicate to drop the serial scope
// retracts the second unit's config; dropping the IsOwnConfig payload check
// retracts the impostor; removing the `!published[topic]` filter retracts all
// 29 live configs, which is the failure mode that cost a sibling consumer its
// whole security plane on every restart.
func TestOrphanSweepRetractsOnlyThisDevicesOwnStaleConfigs(t *testing.T) {
	const (
		ours       = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345_gone_away/config"
		otherUnit  = "homeassistant/sensor/zendure2mqtt_SF2400AC0099999_gone_away/config"
		foreign    = "homeassistant/sensor/zigbee2mqtt_0x001_battery/config"
		impostor   = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345_impostor/config"
		fiveSeg    = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345/legacy/config"
		bundle     = "homeassistant/device/zendure2mqtt_SF2400AC0012345/config"
		alreadyOut = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345_cleared/config"
	)
	retained := map[string][]byte{
		ours:      []byte(`{"unique_id":"zendure2mqtt_SF2400AC0012345_gone_away","state_topic":"zendure2mqtt/SF2400AC0012345/now/gone_away/state"}`),
		otherUnit: []byte(`{"unique_id":"zendure2mqtt_SF2400AC0099999_gone_away","state_topic":"zendure2mqtt/SF2400AC0099999/now/gone_away/state"}`),
		foreign:   []byte(`{"unique_id":"zigbee2mqtt_0x001_battery","state_topic":"zigbee2mqtt/0x001/battery"}`),
		// Our namespace on the topic, somebody else's state tree in the
		// payload.
		impostor:   []byte(`{"unique_id":"zendure2mqtt_SF2400AC0012345_impostor","state_topic":"elsewhere/SF2400AC0012345/x/state"}`),
		fiveSeg:    []byte(`{"unique_id":"zendure2mqtt_SF2400AC0012345_legacy","state_topic":"zendure2mqtt/SF2400AC0012345/now/legacy/state"}`),
		bundle:     []byte(`{"dev":{"ids":["zendure2mqtt_SF2400AC0012345"]},"cmps":{}}`),
		alreadyOut: nil,
	}
	rig := newBrokerRig(t, retained)

	// The live fleet has to be published before the sweep runs, or the pass
	// judges all 29 of this device's configs unclaimed and deletes them. That
	// ordering is the measured hazard, not a detail.
	ctx := t.Context()
	published := rig.seedFleet(t)

	before := len(rig.broker.publishes())
	// A budget the library trims its window against, so the pin does not sit
	// out the full two seconds.
	sweepCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	rig.coord.sweepOrphans(sweepCtx, rig.dev.SN, published)

	var retracted []string
	for _, rec := range rig.broker.publishes()[before:] {
		if len(rec.Payload) != 0 {
			t.Errorf("sweep published a non-empty payload to %s", rec.Topic)
			continue
		}
		if rec.QoS != mqtt.QoS0 || !rec.Retain {
			t.Errorf("retraction of %s at QoS %v retain=%v, want QoS0 retained", rec.Topic, rec.QoS, rec.Retain)
		}
		retracted = append(retracted, rec.Topic)
	}
	if len(retracted) != 1 || retracted[0] != ours {
		t.Fatalf("sweep retracted %v, want exactly [%s]", retracted, ours)
	}

	// And the retraction is remembered on both sides, so a transiently
	// shrunken report does not leave the entity deleted for the process
	// lifetime: the runtime no longer declares it, and internal/hass no
	// longer counts it as sent.
	for _, topic := range rig.coord.deps.HARuntime.Declared() {
		if topic == ours {
			t.Error("the retracted topic is still declared by the runtime")
		}
	}
}

// TestSweepLeavesAClaimedConfigAlone pins the ordering rule as a behaviour
// rather than as a comment.
//
// A sweep that runs before a device's configs are published judges every one
// of them an orphan and clears it — once per boot, with nothing left to
// re-declare it. Here the pass runs with an empty claim set and must still
// find nothing to do, because this device's live configs are not in the
// retained tree either; what it proves is that the pass keys on what the
// runtime declared, and the companion assertion below is that a claimed
// config is never retracted even when the caller's published set omits it.
func TestSweepLeavesAClaimedConfigAlone(t *testing.T) {
	const live = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345_electric_level/config"
	rig := newBrokerRig(t, nil)
	ctx := t.Context()
	rig.seedFleet(t)

	before := len(rig.broker.publishes())
	sweepCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// An empty published set: every retained config of this device is
	// unclaimed as far as this caller is concerned.
	rig.coord.sweepOrphans(sweepCtx, rig.dev.SN, nil)

	for _, rec := range rig.broker.publishes()[before:] {
		if rec.Topic == live {
			t.Fatalf("the sweep retracted a config the runtime is still claiming: %s", live)
		}
	}
}

// TestOwnsDeviceConfigTopicIsScopedToOneDevice covers the predicate directly,
// because the sweep only ever exercises the rows its retained tree happens to
// contain and the interesting rows here are the ones no broker would offer
// twice.
func TestOwnsDeviceConfigTopicIsScopedToOneDevice(t *testing.T) {
	const (
		root = "zendure2mqtt"
		sn   = "SF2400AC0012345"
	)
	cases := []struct {
		name  string
		topic publisher.ConfigTopic
		want  bool
	}{
		{"ours", publisher.ConfigTopic{Platform: "sensor", ObjectID: root + "_" + sn + "_electric_level"}, true},
		{"ours, pack entity", publisher.ConfigTopic{Platform: "sensor", ObjectID: root + "_" + sn + "_pack_AO4H_soc_level"}, true},
		{"another unit", publisher.ConfigTopic{Platform: "sensor", ObjectID: root + "_SF2400AC0099999_x"}, false},
		{"another integration", publisher.ConfigTopic{Platform: "sensor", ObjectID: "zigbee2mqtt_0x001"}, false},
		// A serial that is a prefix of ours must not match: the underscore
		// is part of the compared prefix.
		{"serial prefix", publisher.ConfigTopic{Platform: "sensor", ObjectID: root + "_SF2400AC001234_x"}, false},
		{"five-segment form", publisher.ConfigTopic{Platform: "sensor", NodeID: root + "_" + sn, ObjectID: "electric_level"}, false},
		{"device bundle", publisher.ConfigTopic{NodeID: root + "_" + sn, Bundle: true}, false},
		{"no platform", publisher.ConfigTopic{ObjectID: root + "_" + sn + "_x"}, false},
		{"no object id", publisher.ConfigTopic{Platform: "sensor"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hass.OwnsDeviceConfigTopic(root, sn, tc.topic); got != tc.want {
				t.Errorf("OwnsDeviceConfigTopic(%+v) = %v, want %v", tc.topic, got, tc.want)
			}
		})
	}
}

// TestRunSubscribesBothPlanes pins what Run registers, because the two
// subscriptions are wired in different places and one of them did not exist
// before this step.
//
// The command filter has always been there. The Home Assistant birth topic is
// new — F6 — and it is the cheapest half of the fix to get wrong: WatchBirth
// returns an error a caller could log and forget, and the symptom of a
// missing birth subscription is nothing at all until a broker loses its
// retained store. Asserting the filter set is what makes the wiring, rather
// than the library call, the thing under test.
//
// Mutation check: deleting either subscribe from Run fails this test;
// deleting the retry that follows a failed subscribe does not, and is covered
// by nothing — a deliberate gap, recorded here rather than left implied,
// because the retry needs a transport that fails once and then succeeds and
// the value of pinning it is lower than the value of saying it is unpinned.
func TestRunSubscribesBothPlanes(t *testing.T) {
	rig := newBrokerRig(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- rig.coord.Run(ctx) }()

	want := []string{"homeassistant/status", CommandFilter(rig.root)}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := rig.broker.filters()
		if strings.Join(got, ",") == strings.Join(want, ",") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run installed %v, want %v", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done
}

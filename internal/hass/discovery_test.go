// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// stubPub records every device document so tests can assert what discovery
// emitted.
type stubPub struct {
	mu    sync.Mutex
	sent  []*discovery.Bundle
	fail  error
	calls int
}

func (s *stubPub) PublishBundle(_ context.Context, b *discovery.Bundle) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail != nil {
		return false, s.fail
	}
	s.sent = append(s.sent, b)
	return true, nil
}

// stubRenderer is a [BundleRenderer] built by hand.
//
// internal/harender is the production renderer and this package cannot import
// it: harender calls [UniqueID], [DeviceName] and [EntityObjectID] out of
// here, so the dependency points that way and a test in this package that
// imported it would be a cycle. What this package is responsible for is *when*
// a document is published and which topics it reports, so a renderer that
// returns a predictable document is exactly the right collaborator; the real
// rendering is pinned in internal/coordinator against the fleet goldens.
type stubRenderer struct {
	root string
	err  error
}

func (r stubRenderer) Bundle(
	dev source.Device,
	_ *model.Report,
	packSN string,
	points []process.Point,
) (*discovery.Bundle, error) {
	if r.err != nil {
		return nil, r.err
	}
	node := r.root + "_" + dev.SN
	if packSN != "" {
		node += "_pack_" + packSN
	}
	b := &discovery.Bundle{
		NodeID:     node,
		Device:     discovery.DeviceInfo{Identifiers: []string{node}, Name: DeviceName(dev, packSN)},
		Origin:     discovery.Origin{Name: "test"},
		Components: map[string]discovery.Component{},
	}
	for _, p := range points {
		if p.PackSN != packSN || p.Entry == nil || p.Entry.Platform == "" {
			continue
		}
		b.Components[p.Topic] = discovery.Component{
			Platform:   hacatalog.Platform(p.Entry.Platform),
			UniqueID:   UniqueID(r.root, dev.SN, packSN, p.Topic),
			StateTopic: process.StateTopic(r.root, dev.SN, p),
		}
	}
	if len(b.Components) == 0 {
		return nil, nil
	}
	return b, nil
}

func newDisc(pub BundleWriter) *Discovery {
	return New("homeassistant", "zendure", stubRenderer{root: "zendure"}, pub, nil)
}

func sensorPoint(topic, group string) process.Point {
	return process.Point{Group: group, Topic: topic, Value: 55, Entry: &catalog.Entry{
		Property: topic, Topic: topic, Group: group, Platform: "sensor", Unit: "%", Name: topic,
	}}
}

func TestIsOwnConfig(t *testing.T) {
	d := newDisc(&stubPub{})
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"per-entity ours", `{"unique_id":"zendure_HOA1_electric_level","state_topic":"zendure/HOA1/now/electric_level/state"}`, true},
		{"per-entity ours no state", `{"unique_id":"zendure_HOA1_btn"}`, true},
		{"per-entity foreign unique_id", `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`, false},
		{"per-entity foreign state root", `{"unique_id":"zendure_HOA1_x","state_topic":"other/HOA1/state"}`, false},
		{"not json", `not-json`, false},
		// The device-document form. A sweep blind to it would leave every
		// document this daemon publishes today standing forever.
		{
			"document ours",
			`{"device":{"identifiers":["zendure_HOA1"]},"components":{"a":{"unique_id":"zendure_HOA1_a","state_topic":"zendure/HOA1/now/a/state"},"b":{"unique_id":"zendure_HOA1_b"}}}`,
			true,
		},
		{
			"document foreign",
			`{"device":{"identifiers":["zigbee2mqtt_lamp"]},"components":{"a":{"unique_id":"zigbee2mqtt_lamp_x","state_topic":"zigbee2mqtt/lamp/x"}}}`,
			false,
		},
		// A sibling instance on a NESTED root, which is the shape that
		// breaks this predicate in a sibling project. Its unique_ids read
		// "zendure/garage_HOA9_x" and its state topics "zendure/garage/…" —
		// so the state-topic half (`<root>/`) accepts, and only the `_`
		// separator on the unique_id half rejects. Retracting these would
		// delete another running bridge's entities.
		{"per-entity nested sibling root", `{"unique_id":"zendure/garage_HOA9_x","state_topic":"zendure/garage/HOA9/now/x/state"}`, false},
		{
			"document nested sibling root",
			`{"device":{"identifiers":["zendure/garage_HOA9"]},"components":{"a":{"unique_id":"zendure/garage_HOA9_a","state_topic":"zendure/garage/HOA9/now/a/state"}}}`,
			false,
		},
		{
			"document with one foreign component",
			`{"components":{"a":{"unique_id":"zendure_HOA1_a","state_topic":"zendure/HOA1/now/a/state"},"b":{"unique_id":"other_thing","state_topic":"other/thing"}}}`,
			false,
		},
	}
	for _, c := range cases {
		if got := d.IsOwnConfig([]byte(c.payload)); got != c.want {
			t.Errorf("%s: IsOwnConfig = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestOwnsDeviceConfigTopicCoversBothForms is the sweep's topic predicate.
//
// Both forms have to be owned, and the legacy one is a migration obligation:
// PublishBundle retracts the per-entity configs that correspond to a component
// of the new document, but a config left behind by an entity removed in some
// earlier release corresponds to nothing and can only be found by a sweep.
// ADR 0070 requires the first start to clear them so users are not left with
// duplicate ghost entities.
func TestOwnsDeviceConfigTopicCoversBothForms(t *testing.T) {
	const root, sn = "zendure", "HOA1"
	cases := []struct {
		name  string
		topic string
		want  bool
	}{
		{"our document", "homeassistant/device/zendure_HOA1/config", true},
		{"our pack document", "homeassistant/device/zendure_HOA1_pack_AO4H01/config", true},
		{"another unit's document", "homeassistant/device/zendure_HOA2/config", false},
		{"a serial we merely prefix", "homeassistant/device/zendure_HOA12/config", false},
		{"foreign document", "homeassistant/device/zigbee2mqtt_lamp/config", false},
		{"our legacy config", "homeassistant/sensor/zendure_HOA1_electric_level/config", true},
		{"another unit's legacy config", "homeassistant/sensor/zendure_HOA2_electric_level/config", false},
		{"foreign legacy config", "homeassistant/sensor/zigbee2mqtt_thing/config", false},
		{"five-segment config", "homeassistant/sensor/zendure_HOA1/electric_level/config", false},
	}
	for _, c := range cases {
		parsed, ok := publisher.ParseConfigTopic("homeassistant", c.topic)
		if !ok {
			t.Fatalf("%s: ParseConfigTopic(%q) refused the topic", c.name, c.topic)
		}
		if got := OwnsDeviceConfigTopic(root, sn, parsed); got != c.want {
			t.Errorf("%s: OwnsDeviceConfigTopic(%q) = %v, want %v", c.name, c.topic, got, c.want)
		}
	}
}

// TestDeviceNameSeedsEntityID checks that a configured DeviceName replaces the
// serial number in both the HA device name and the (language-independent)
// entity-id seed, while an unset name keeps the "Zendure <SN>" default — and
// that neither touches the unique_id, which is the stable HA identity the
// migration froze.
func TestDeviceNameSeedsEntityID(t *testing.T) {
	cases := []struct {
		name           string
		deviceName     string
		wantDeviceName string
		wantSeed       string
	}{
		{"default", "", "Zendure HOA1", "zendure_hoa1_electric_level"},
		{"configured", "Balkon Speicher", "Balkon Speicher", "balkon_speicher_electric_level"},
	}
	for _, c := range cases {
		dev := source.Device{SN: "HOA1", DeviceName: c.deviceName, Model: "SolarFlow 2400 AC"}
		if got := DeviceName(dev, ""); got != c.wantDeviceName {
			t.Errorf("%s: DeviceName = %q, want %q", c.name, got, c.wantDeviceName)
		}
		if got := EntityObjectID(DeviceName(dev, ""), "electric_level"); got != c.wantSeed {
			t.Errorf("%s: EntityObjectID = %q, want %q", c.name, got, c.wantSeed)
		}
		if got := UniqueID("zendure", dev.SN, "", "electric_level"); got != "zendure_HOA1_electric_level" {
			t.Errorf("%s: unique_id = %q, want the stable SN-based id (DeviceName must not affect it)", c.name, got)
		}
	}
}

func TestPublishReturnsTheDocumentTopicSet(t *testing.T) {
	pub := &stubPub{}
	d := newDisc(pub)
	dev := source.Device{SN: "HOA1", Model: "SolarFlow 2400 AC"}
	report := &model.Report{Product: "solarFlow2400AC"}
	packPoint := sensorPoint("soc_level", "battery")
	packPoint.PackSN = "AO4H01"
	points := []process.Point{
		sensorPoint("electric_level", "now"),
		packPoint,
		{Group: "misc", Topic: "raw_unmapped", Value: 1, Entry: nil},                                 // skipped: no entry
		{Group: "misc", Topic: "no_platform", Value: 1, Entry: &catalog.Entry{Topic: "no_platform"}}, // skipped: no platform
	}

	const (
		unitTopic = "homeassistant/device/zendure_HOA1/config"
		packTopic = "homeassistant/device/zendure_HOA1_pack_AO4H01/config"
	)

	published := d.Publish(context.Background(), dev, report, points)
	if !published[unitTopic] || !published[packTopic] {
		t.Fatalf("published set = %v, want both %q and %q", published, unitTopic, packTopic)
	}
	if len(published) != 2 {
		t.Errorf("published has %d topics, want 2 — one document per Home Assistant device", len(published))
	}
	if pub.calls != 2 {
		t.Errorf("publisher called %d times, want 2", pub.calls)
	}
	// A sub-device is a device, not a component of its parent: the pack's
	// entity must not appear in the unit's document.
	if _, leaked := pub.sent[0].Components["soc_level"]; leaked {
		t.Error("the pack's component was rendered into the unit's document")
	}

	// Idempotent: the second call still reports the full current set but does
	// not re-send the (retained) documents.
	published2 := d.Publish(context.Background(), dev, report, points)
	if len(published2) != 2 {
		t.Errorf("second publish reported %d topics, want 2", len(published2))
	}
	if pub.calls != 2 {
		t.Errorf("publisher re-sent a retained document: calls = %d, want 2", pub.calls)
	}
}

// TestAnOwnerWithNoEntityPublishesNothing guards the one payload this daemon
// must never write by accident. A device document with an empty `components`
// map is not "a device with no entities" to Home Assistant — it is the
// instruction to remove every entity of that device.
func TestAnOwnerWithNoEntityPublishesNothing(t *testing.T) {
	pub := &stubPub{}
	d := newDisc(pub)
	dev := source.Device{SN: "HOA1"}
	points := []process.Point{{Group: "misc", Topic: "raw", Value: 1, Entry: nil}}

	if published := d.Publish(context.Background(), dev, nil, points); len(published) != 0 {
		t.Errorf("published = %v, want nothing", published)
	}
	if pub.calls != 0 {
		t.Errorf("publisher called %d times, want 0", pub.calls)
	}
}

// TestAFailedDocumentIsRetriedOnTheNextReport is the half of the
// retract-then-publish contract this package owns.
//
// PublishBundle's error can be a failed *retraction* — it aborts before the
// document rather than publishing one while a superseded per-entity config is
// still retained, because Home Assistant would refuse it — so a failure here
// means the fleet is in the one state where nothing appears. Marking the
// document sent anyway would strand it there until a restart.
func TestAFailedDocumentIsRetriedOnTheNextReport(t *testing.T) {
	pub := &stubPub{fail: errors.New("retract superseded: broker said no")}
	d := newDisc(pub)
	dev := source.Device{SN: "HOA1"}
	points := []process.Point{sensorPoint("electric_level", "now")}

	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 1 {
		t.Fatalf("first publish: calls = %d, want 1", pub.calls)
	}
	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 2 {
		t.Errorf("a failed document was not retried: calls = %d, want 2", pub.calls)
	}

	pub.mu.Lock()
	pub.fail = nil
	pub.mu.Unlock()
	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 3 || len(pub.sent) != 1 {
		t.Errorf("calls = %d, documents sent = %d, want 3 and 1", pub.calls, len(pub.sent))
	}
	// And once it lands, the guard closes again.
	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 3 {
		t.Errorf("the guard did not close after a successful document: calls = %d, want 3", pub.calls)
	}
}

// TestForgetReopensTheGuardForOneDocument covers the mapping the document form
// does not give for free: the topic carries a node id and no unique_id, so
// undoing the guard needs the list this process recorded when it wrote the
// document.
func TestForgetReopensTheGuardForOneDocument(t *testing.T) {
	pub := &stubPub{}
	d := newDisc(pub)
	dev := source.Device{SN: "HOA1"}
	packPoint := sensorPoint("soc_level", "battery")
	packPoint.PackSN = "AO4H01"
	points := []process.Point{sensorPoint("electric_level", "now"), packPoint}

	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 2 {
		t.Fatalf("calls = %d, want 2", pub.calls)
	}

	d.Forget([]string{"homeassistant/device/zendure_HOA1/config"})
	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 3 {
		t.Errorf("Forget did not reopen the guard for the unit: calls = %d, want 3", pub.calls)
	}

	// A topic this process never published is ignored rather than guessed at.
	d.Forget([]string{"homeassistant/device/somebody_else/config"})
	d.Publish(context.Background(), dev, nil, points)
	if pub.calls != 3 {
		t.Errorf("Forget acted on a foreign topic: calls = %d, want 3", pub.calls)
	}
}

// TestRenderFailureIsReportedAndNotPublished keeps a rendering error from
// producing a topic in the published set: a topic reported but never written
// would be claimed against the sweep while the broker holds nothing.
func TestRenderFailureIsReportedAndNotPublished(t *testing.T) {
	pub := &stubPub{}
	d := New("homeassistant", "zendure", stubRenderer{root: "zendure", err: errors.New("boom")}, pub, nil)
	published := d.Publish(context.Background(), source.Device{SN: "HOA1"}, nil,
		[]process.Point{sensorPoint("electric_level", "now")})
	if len(published) != 0 || pub.calls != 0 {
		t.Errorf("published = %v, calls = %d, want nothing", published, pub.calls)
	}
}

// TestBundlePublishedIsLoggedWithTheWholeTopic pins the log line three
// operator documents send the reader to.
//
// `README.md`, `changelog.md` and `addon/DOCS.md` all tell an operator
// downgrading to 0.7.x or earlier to clear the retained device documents
// first, and all three say to take the topic **verbatim from this line**
// rather than compose it: the prefix is the operator-settable
// HASS_BASE_TOPIC and the node id is this bridge's own spelling of the
// device identity (the identifier as-is, deliberately not slugged — see
// harender.Context.NodeID). An operator who composed it from a hard-coded
// `homeassistant/` and a guessed node id would clear nothing, and the
// downgrade would then fail exactly as silently as the upgrade it mirrors.
//
// Mutation check: renaming the event, dropping the `topic` field, or logging
// a composed fragment instead of the whole topic fails here — and should be
// taken as a signal to fix the three documents in the same change.
func TestBundlePublishedIsLoggedWithTheWholeTopic(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pub := &stubPub{}
	d := New("ha-discovery", "zendure", stubRenderer{root: "zendure"}, pub, logger)
	dev := source.Device{SN: "HOA1", Model: "SolarFlow 2400 AC"}
	d.Publish(context.Background(), dev, &model.Report{}, []process.Point{
		sensorPoint("electric_level", "now"),
	})

	out := buf.String()
	if !strings.Contains(out, "hass.bundle_published") {
		t.Fatalf("no hass.bundle_published line logged; the operator documents point at it.\n%s", out)
	}
	if want := `topic=ha-discovery/device/zendure_HOA1/config`; !strings.Contains(out, want) {
		t.Errorf("log line does not carry the whole retained topic %q:\n%s", want, out)
	}
}

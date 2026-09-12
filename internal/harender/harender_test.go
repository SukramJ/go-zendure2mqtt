// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package harender_test

import (
	"strings"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// The whole-fleet byte-equality proof lives in internal/coordinator, beside
// the pins it compares against. What is left here is the handful of
// statements that proof cannot make: the cases the shipped catalog does not
// reach, and the one divergence from production that this package knowingly
// carries.

// TestSelectWithNoValueMapDivergesFromProduction records the one place where
// the library's rendering of this bridge's model is NOT byte-identical to
// what internal/hass publishes.
//
// A select entry whose value_map is empty: production writes
// `"options": []` unconditionally (Discovery.config's select case assigns
// Entry.Options, which returns an empty slice), while the shared
// Component.Options is `omitempty`, so the library drops the key entirely.
//
// Unreachable with the shipped zendure.yaml — both selects, acMode and
// smartMode, carry value maps — which is exactly why it is asserted rather
// than left to be discovered. zendure.yaml is the documented extension point,
// so a select added without a value_map is a data change nobody would review
// as a payload change, and the divergence would then land in a step whose
// changelog says the payload is unchanged.
//
// Neither answer is better on the wire: Home Assistant reads an empty
// `options` list as a select with no options, and an absent one as the same
// thing, and a select with no options is a defect either way. So this is
// recorded, not fixed: fixing it would change a published payload inside a
// step whose whole purpose is to prove one is unchanged.
func TestSelectWithNoValueMapDivergesFromProduction(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	entry := catalog.Entry{
		Property: "someMode", Topic: "some_mode", Group: "config",
		Platform: "select", Name: "Some mode",
	}
	p := process.Point{Group: "config", Topic: "some_mode", Entry: &entry}
	dev := source.Device{SN: "SF2400AC0012345", Model: "SolarFlow 2400 AC"}

	ent, ok := r.Entity(dev, p)
	if !ok {
		t.Fatal("a select with no value map minted no entity")
	}
	if ent.Desc().Options != nil {
		t.Errorf("Description.Options = %v, want nil for an entry with no value map", ent.Desc().Options)
	}

	comp, err := discovery.RenderComponent(r.Context(), r.Device(dev, nil, ""), ent, discovery.Origin{})
	if err != nil {
		t.Fatalf("RenderComponent: %v", err)
	}
	raw, err := comp.EntityJSON()
	if err != nil {
		t.Fatalf("EntityJSON: %v", err)
	}
	if got := string(raw); strings.Contains(got, `"options"`) {
		t.Errorf("the library emitted an options key for a select with no value map: %s", got)
	}
	// Production's answer, for the record: an empty list.
	if got := entry.Options("en"); got == nil || len(got) != 0 {
		t.Errorf("Entry.Options = %v, want a non-nil empty slice (this is the half that renders as `[]`)", got)
	}
}

// TestPointsWithoutAnEntryMintNoEntity pins the skip rule, which is the
// production one: a device property with no catalog entry, or an entry with
// no platform, is published as a state topic and gets no Home Assistant
// entity.
//
// It is what makes the pinned state-topic list 30 entries long for 29
// entities — the pack's softVersion has no catalog entry — and a renderer
// that minted an entity for those would add 30-minus-29 entities the pin
// would report as "not in the pin" without saying why.
func TestPointsWithoutAnEntryMintNoEntity(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	dev := source.Device{SN: "SF2400AC0012345"}

	noEntry := process.Point{Group: process.GroupMisc, Topic: "softVersion"}
	if _, ok := r.Entity(dev, noEntry); ok {
		t.Error("a point with no catalog entry minted an entity")
	}

	noPlatform := catalog.Entry{Property: "x", Topic: "x", Group: "now"}
	if _, ok := r.Entity(dev, process.Point{Group: "now", Topic: "x", Entry: &noPlatform}); ok {
		t.Error("an entry with no platform minted an entity")
	}
}

// TestLayoutHasNoDeviceAvailabilityTopic states F4 rather than leaving it to
// be inferred from an empty string.
//
// This bridge has no per-device availability topic at all: every entity's
// only availability source is the bridge LWT, so an unreachable device keeps
// its last values indefinitely while the bridge is up. The Layout has to
// implement the method — topic.Layout has four — and the honest answer is
// the empty string, not a topic nothing publishes to.
//
// Fixing F4 is additive (a second availability entry per entity, which the
// library has the vocabulary for) and it is deliberately not this step's
// business: it changes a published payload.
func TestLayoutHasNoDeviceAvailabilityTopic(t *testing.T) {
	layout := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}.Layout()
	slot := harender.Slot("SF2400AC0012345", process.Point{Group: "now", Topic: "electric_level"})

	if got := layout.Availability(slot); got != "" {
		t.Errorf("Layout.Availability = %q, want \"\" — this bridge publishes no per-device availability topic (F4)", got)
	}
	if got, want := layout.Bridge(), "zendure2mqtt/bridge/status"; got != want {
		t.Errorf("Layout.Bridge = %q, want %q", got, want)
	}
}

// TestContextRefusesToGuessAnIdentity pins the deliberate refusal in
// Context.UniqueID and Context.ObjectID.
//
// An entity that carries no frozen identity gets the empty string rather
// than StdContext's computed one. That is the whole safety property of this
// package: StdContext.UniqueID slugs the device identity, and slugging
// case-folds, so the fallback would hand back
// "zendure2mqtt_sf2400ac0012345_…" for a fleet published as
// "zendure2mqtt_SF2400AC0012345_…". Home Assistant has no unique-id
// migration path, so that one character-case change is every entity losing
// its history, its area and every automation naming it — applied silently,
// to whichever entity someone later added without a frozen identity.
//
// The check uses a bare hamodel.Basic, which is what such an entity would
// be.
func TestContextRefusesToGuessAnIdentity(t *testing.T) {
	ctx := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}.Context()
	dev := &hamodel.Device{
		Identity: hamodel.Identity{IDs: []hamodel.Identifier{{Value: "zendure2mqtt_SF2400AC0012345"}}},
	}
	stranger := &hamodel.Basic{
		EntityKey:      "electric_level",
		EntityPlatform: hacatalog.PlatformSensor,
		Description:    hamodel.Description{Name: hamodel.L("Battery level")},
	}

	if got := ctx.UniqueID(dev, stranger); got != "" {
		t.Errorf("UniqueID for an entity with no frozen identity = %q, want \"\"; a computed one here is a silent case-fold of the whole fleet", got)
	}
	if got := ctx.ObjectID(dev, stranger); got != "" {
		t.Errorf("ObjectID for an entity with no frozen identity = %q, want \"\" (which suppresses the key)", got)
	}
	if got := ctx.Availability(dev, stranger); got != nil {
		t.Errorf("Availability = %v, want nil; this bridge publishes the flat triple instead", got)
	}
	if got, want := ctx.NodeID(dev), "zendure2mqtt_SF2400AC0012345"; got != want {
		t.Errorf("NodeID = %q, want %q", got, want)
	}
	if got := ctx.NodeID(nil); got != "" {
		t.Errorf("NodeID(nil) = %q, want \"\"", got)
	}
}

// TestDeviceBlocksCarryTheInstalledIdentifiers pins the two device shapes at
// the model level, ahead of the payload comparison in internal/coordinator.
//
// The identifier carries no namespace, and that is the escape hatch
// hamodel.Identifier documents rather than an oversight: Home Assistant keys
// its device registry on these strings with no migration path, so a
// namespaced "zendure:zendure2mqtt_<sn>" would leave the installed device
// behind with its area and its name override while the entities moved to a
// new one.
func TestDeviceBlocksCarryTheInstalledIdentifiers(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	dev := source.Device{
		SN: "SF2400AC0012345", Model: "SolarFlow 2400 AC", Address: "192.168.1.50",
	}
	report := &model.Report{
		SN: "SF2400AC0012345", Product: "solarFlow2400AC",
		PackData: []map[string]any{{"sn": "AO4H2301X01", "softVersion": float64(4109)}},
	}

	unit := r.Device(dev, report, "")
	if got, want := unit.Identity.UID(), "zendure2mqtt_SF2400AC0012345"; got != want {
		t.Errorf("unit identifier = %q, want %q", got, want)
	}
	if unit.Via != nil {
		t.Errorf("unit has via_device %v, want none", unit.Via)
	}
	if got, want := unit.ConfigURL, "http://192.168.1.50"; got != want {
		t.Errorf("unit configuration_url = %q, want %q", got, want)
	}
	if got, want := unit.ModelID, "solarFlow2400AC"; got != want {
		t.Errorf("unit model_id = %q, want %q", got, want)
	}

	pack := r.Device(dev, report, "AO4H2301X01")
	if got, want := pack.Identity.UID(), "zendure2mqtt_SF2400AC0012345_pack_AO4H2301X01"; got != want {
		t.Errorf("pack identifier = %q, want %q", got, want)
	}
	if pack.Via == nil || pack.Via.UID() != unit.Identity.UID() {
		t.Errorf("pack via_device = %v, want %q", pack.Via, unit.Identity.UID())
	}
	if got, want := pack.SWVersion, "4109"; got != want {
		t.Errorf("pack sw_version = %q, want %q", got, want)
	}
	// A cloud-reached device has no Address by construction, so it gets no
	// configuration_url — which is correct, and is also the shape F1 makes
	// permanent for a local device whose first report arrived without one.
	if got := r.Device(source.Device{SN: "X"}, nil, "").ConfigURL; got != "" {
		t.Errorf("a device with no address got configuration_url %q", got)
	}
}

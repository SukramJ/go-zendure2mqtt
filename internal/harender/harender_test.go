// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package harender_test

import (
	"strings"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"

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
	r := renderer(t, "en")
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
	r := renderer(t, "en")
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

// renderer is the renderer of an instance that never set MQTT_TOPIC: topics
// under the 0.10.0 default name, identities on the pre-0.10.0 root.
func renderer(t *testing.T, lang string) harender.Renderer {
	t.Helper()
	layout, err := harender.NewLayout("zendure")
	if err != nil {
		t.Fatal(err)
	}
	return harender.Renderer{Topics: layout, IdentityRoot: "zendure2mqtt", Lang: lang}
}

// TestLayoutRendersTheConventionTopics pins the mqtt-smarthome 2.0 tree
// (openccu-loom ADR 0083): the function on the second level, the same item
// path under status and set, the unit's online item and the connected topic.
func TestLayoutRendersTheConventionTopics(t *testing.T) {
	layout := renderer(t, "en").Layout()
	unit := harender.Slot("SF2400AC0012345", process.Point{Group: "now", Topic: "electric_level"})
	pack := harender.Slot("SF2400AC0012345", process.Point{Group: process.GroupBattery, PackSN: "AO4H2301X01", Topic: "soc_level"})

	for _, c := range []struct{ name, got, want string }{
		{"state", layout.State(unit), "zendure/status/SF2400AC0012345/now/electric_level"},
		{"command", layout.Command(unit), "zendure/set/SF2400AC0012345/now/electric_level"},
		{"pack state", layout.State(pack), "zendure/status/SF2400AC0012345/battery/AO4H2301X01/soc_level"},
		{"availability", layout.Availability(unit), "zendure/status/SF2400AC0012345/online"},
		{"pack availability is the unit's", layout.Availability(pack), "zendure/status/SF2400AC0012345/online"},
		{"bridge", layout.Bridge(), "zendure/connected"},
		{"connected", layout.Connected(), "zendure/connected"},
		{"info", layout.Info(), "zendure/info"},
		{"maintenance", layout.Maintenance("stats"), "zendure/maintenance/stats"},
		{"command filter", layout.CommandFilter(), "zendure/set/+/+/+"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if !layout.Conformant() {
		t.Error("a single-level name reported non-conformant")
	}

	// A multi-level name is kept verbatim, as MQTT_TOPIC always was, and is
	// reported outside spec §3; a wildcard is refused.
	nested, err := harender.NewLayout("home/zendure")
	if err != nil {
		t.Fatalf("NewLayout(home/zendure): %v", err)
	}
	if nested.Conformant() || nested.Connected() != "home/zendure/connected" {
		t.Errorf("nested layout: conformant=%v connected=%q", nested.Conformant(), nested.Connected())
	}
	if _, err := harender.NewLayout("zen+dure"); err == nil {
		t.Error("NewLayout accepted a wildcard")
	}
}

// TestEntitiesReadTheStatusObject pins what every entity carries under the
// convention: the value read from `val`, a switch comparing the lowered
// boolean, a select mapping token to label in German and back, and the two
// availability entries with mode "all".
func TestEntitiesReadTheStatusObject(t *testing.T) {
	dev := source.Device{SN: "SF2400AC0012345", Model: "SolarFlow 2400 AC"}
	acMode := catalog.Entry{
		Property: "acMode", Topic: "ac_mode", Group: "config", Platform: "select", Writable: true,
		ValueMap: map[string]string{"1": "charge", "2": "discharge"}, ValueMapDE: map[string]string{"1": "Laden", "2": "Entladen"},
	}
	sw := catalog.Entry{Property: "charge_active", Topic: "charge_active", Group: "config", Platform: "switch", Writable: true}
	level := catalog.Entry{Property: "electricLevel", Topic: "electric_level", Group: "now", Platform: "sensor", Unit: "%"}

	render := func(lang string, e catalog.Entry) discovery.Component {
		t.Helper()
		r := renderer(t, lang)
		ent, ok := r.Entity(dev, process.Point{Group: e.Group, Topic: e.TopicLeaf(), Entry: &e})
		if !ok {
			t.Fatalf("%s minted no entity", e.Topic)
		}
		comp, err := discovery.RenderComponent(r.Context(), r.Device(dev, nil, ""), ent, discovery.Origin{})
		if err != nil {
			t.Fatalf("RenderComponent: %v", err)
		}
		return comp
	}

	sensor := render("en", level)
	if sensor.ValueTemplate != harender.ValueTemplate {
		t.Errorf("sensor value_template = %q", sensor.ValueTemplate)
	}
	if sensor.AvailabilityMode != "all" || len(sensor.Availability) != 2 {
		t.Fatalf("sensor availability = %+v mode %q, want two entries, mode all", sensor.Availability, sensor.AvailabilityMode)
	}
	if got := sensor.Availability[0]; got.Topic != "zendure/connected" || got.PayloadAvailable != "online" ||
		got.ValueTemplate != discovery.ConnectedTemplate(discovery.ConnectedOperational) {
		t.Errorf("bridge availability = %+v", got)
	}
	if got := sensor.Availability[1]; got.Topic != "zendure/status/SF2400AC0012345/online" ||
		got.PayloadAvailable != "true" || got.ValueTemplate != discovery.StatusBoolValueTemplate {
		t.Errorf("device availability = %+v", got)
	}
	if sensor.AvailabilityTopic != "" {
		t.Errorf("the flat availability_topic survived: %q", sensor.AvailabilityTopic)
	}

	// Since 0.10.1 an English select maps its tokens too, so a code outside
	// the map renders None (unknown) instead of an option Home Assistant
	// rejects; the command side needs no template (token == label).
	sel := render("en", acMode)
	if sel.ValueTemplate != harender.EnumValueTemplate(harender.Enum(acMode), "en") || sel.CommandTemplate != "" {
		t.Errorf("English select templates = %q / %q, want the guarded token map and no command template", sel.ValueTemplate, sel.CommandTemplate)
	}
	if strings.Join(sel.Options, ",") != "charge,discharge" {
		t.Errorf("English options = %v", sel.Options)
	}
	selDE := render("de", acMode)
	if strings.Join(selDE.Options, ",") != "Laden,Entladen" {
		t.Errorf("German options = %v", selDE.Options)
	}
	if !strings.Contains(selDE.ValueTemplate, "Laden") || !strings.Contains(selDE.CommandTemplate, "charge") {
		t.Errorf("German select must map token<->label: value %q command %q", selDE.ValueTemplate, selDE.CommandTemplate)
	}

	swc := render("en", sw)
	fields, _ := swc.Fields.(discovery.SwitchFields)
	if swc.ValueTemplate != discovery.StatusBoolValueTemplate || fields.PayloadOn != "true" || fields.PayloadOff != "false" {
		t.Errorf("switch = template %q, fields %+v", swc.ValueTemplate, swc.Fields)
	}

	// A value-mapped sensor is an enum sensor with the select's guarded map.
	mode := catalog.Entry{
		Property: "workMode", Topic: "work_mode", Group: "now", Platform: "sensor",
		ValueMap: map[string]string{"0": "idle", "1": "busy"}, ValueMapDE: map[string]string{"0": "frei", "1": "belegt"},
	}
	enumDE := render("de", mode)
	if enumDE.DeviceClass != "enum" || strings.Join(enumDE.Options, ",") != "frei,belegt" ||
		enumDE.ValueTemplate != harender.EnumValueTemplate(harender.Enum(mode), "de") || enumDE.StateClass != "" {
		t.Errorf("enum sensor = class %q options %v state_class %q template %q", enumDE.DeviceClass, enumDE.Options, enumDE.StateClass, enumDE.ValueTemplate)
	}
}

// TestEnumAcceptsTokensAndLabels is the set half of the token rule: the same
// enum discovery renders is what the coordinator hands SetValue.Enum, so the
// token in any case and either language's label resolve to the token.
func TestEnumAcceptsTokensAndLabels(t *testing.T) {
	enum := harender.Enum(catalog.Entry{
		ValueMap: map[string]string{"0": "persist", "1": "volatile"}, ValueMapDE: map[string]string{"0": "dauerhaft", "1": "flüchtig"},
	})
	if enum == nil || strings.Join(enum.Codes, ",") != "persist,volatile" {
		t.Fatalf("Enum codes = %+v", enum)
	}
	for _, in := range []string{"volatile", "VOLATILE", "flüchtig"} {
		if got, err := (publisher.SetValue{Text: in}).Enum(enum, true); err != nil || got != "volatile" {
			t.Errorf("Enum(%q) = %q, %v", in, got, err)
		}
	}
	if harender.Enum(catalog.Entry{}) != nil {
		t.Error("an entry without a value map rendered an enum")
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
	ctx := renderer(t, "en").Context()
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
		t.Errorf("Availability = %v, want nil for an entity that binds no slot", got)
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
//
// The renderer is the default one, whose topics moved to "zendure" in 0.10.0
// while the identifiers stay on "zendure2mqtt".
func TestDeviceBlocksCarryTheInstalledIdentifiers(t *testing.T) {
	r := renderer(t, "en")
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

// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package harender renders this bridge's Home Assistant entities through the
// shared go-hamqtt model.
//
// It began as step 2 of ADR 0070 phase 5 — the phase-5 pilot measurement of
// 2026-09-12, §7.2 — where it ran in parallel with internal/hass and
// published nothing: the whole migration rested on the claim that the shared
// library can reproduce what this bridge already publishes, and that claim
// was derived from reading the library rather than from running it. It came
// out byte-exact, 29 of 29 entities, and the pins in internal/coordinator are
// that result.
//
// Step 5 made it the production renderer. It is now the only thing that
// describes this bridge's fleet to Home Assistant: [Renderer.Bundle] renders
// the one retained device document per device that replaced the 29 retained
// per-entity configs, and internal/hass publishes it. Nothing here publishes
// — there is still no MQTT client, no runtime and no Publisher in this
// package; it turns a [source.Device] plus a [model.Report] plus the
// [process.Point]s the daemon already resolves into a [hamodel.Device], a set
// of [hamodel.Entity] values, a [discovery.Context] and a
// [discovery.Bundle].
//
// # What is deliberately preserved rather than improved
//
// Three things the shared library would do differently are held to this
// bridge's current answer, because the point of the pilot is to prove the
// runtime on an installed base without also re-keying it:
//
//   - Identity. unique_id, default_entity_id, the device identifiers and
//     via_device all come from [hass.UniqueID], [hass.EntityObjectID] and
//     [hass.DeviceName] — the production functions, called, not copied. The
//     library's own defaults would case-fold the serial and seed the entity
//     id from the topic root instead of the device name, and Home Assistant
//     has no migration path for any of the three.
//   - Topics. [Layout] delegates to [process.StateTopic] and
//     [process.CommandTopic]. topic.Default is not adopted: its Bucket enum
//     is paramset-shaped (values/master/calculated/custom) and this bridge's
//     groups (now/config/static/battery/misc) mean something else, so every
//     slot here carries [hamodel.BucketUnset] and the group travels in
//     Slot.Path. See the measurement's §5.2.
//   - Availability. This bridge publishes the flat availability_topic /
//     payload_available / payload_not_available triple, which Home Assistant
//     accepts on 28 to 30 of its 32 platforms and which the library types on
//     [discovery.Component] for exactly this case. [Context.Availability]
//     therefore returns no availability list at all and [Entity] writes the
//     three flat keys from its Builder.
//
// And the slug is not swapped. [hass.EntityObjectID] stays the source of
// every entity-id seed even though the library's topic.Slug is the better
// function, because two of the seeds it produces are pinned defects (F8's
// pack-serial collision and a dropped "é") and a pinned defect is one whose
// later fix can be seen.
package harender

import (
	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/topic"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Manufacturer is the device block's manufacturer, as published today.
const Manufacturer = "Zendure"

// PackModel is the model string every battery sub-device carries.
const PackModel = "Battery Pack"

// Availability payloads, as published today on the flat keys.
const (
	PayloadAvailable    = "online"
	PayloadNotAvailable = "offline"
)

// Switch payloads. Both virtual switches publish "1"/"0" rather than
// Home Assistant's ON/OFF default.
const (
	SwitchPayloadOn  = "1"
	SwitchPayloadOff = "0"
)

// Layout is this bridge's [topic.Layout]: the four methods the shared model
// asks for, answered by the bridge's own 31-line topic builder.
//
// Writing one is the cheapest part of the migration and it is what keeps the
// topic schema a decision of this repository — documented in
// docs/konzept.md and in every user's automations — rather than of the
// library. State and Command delegate to [process.StateTopic] and
// [process.CommandTopic] instead of restating the format, so there is one
// formula and not two.
type Layout struct {
	// Root is the bridge MQTT topic root, config.MQTTTopic.
	Root string
}

var _ topic.Layout = Layout{}

// Slot is the coordinate of one resolved point.
//
// The mapping is: the unit serial is the address, a battery pack serial is
// the channel, and the topic group and leaf travel in Path. Bucket stays
// [hamodel.BucketUnset] — it renders as the empty string and [topic.Join]
// drops empty segments — because the library's five buckets are paramset
// names and this bridge's five groups are not the same concept. That is the
// one place the shared model still smells of the reference consumer, and it
// costs nothing here.
func Slot(sn string, p process.Point) hamodel.Slot {
	return hamodel.Slot{
		Address: sn,
		Channel: p.PackSN,
		Bucket:  hamodel.BucketUnset,
		Path:    []string{p.Group, p.Topic},
	}
}

// pointOf is the inverse of [Slot], so the Layout can hand a coordinate back
// to the bridge's own topic builder.
func pointOf(s hamodel.Slot) process.Point {
	p := process.Point{PackSN: s.Channel}
	if len(s.Path) > 0 {
		p.Group = s.Path[0]
	}
	if len(s.Path) > 1 {
		p.Topic = s.Path[1]
	}
	return p
}

// State implements [topic.Layout].
func (l Layout) State(s hamodel.Slot) string {
	return process.StateTopic(l.Root, s.Address, pointOf(s))
}

// Command implements [topic.Layout].
func (l Layout) Command(s hamodel.Slot) string {
	return process.CommandTopic(l.Root, s.Address, pointOf(s))
}

// Availability implements [topic.Layout] and returns the empty string,
// because this bridge has no per-device availability topic at all: every
// entity's only availability source is the bridge LWT.
//
// That is F4 of the phase-5 measurement — while the bridge is up and a device
// is unplugged, its entities stay available showing the last value they ever
// saw — and it is not this step's business to fix. Returning "" states the
// absence rather than inventing a topic nothing publishes to; nothing in this
// package calls it, since [Context.Availability] renders no availability list.
func (Layout) Availability(hamodel.Slot) string { return "" }

// Bridge implements [topic.Layout]: the daemon's own status topic, carrying
// its LWT, and the topic every entity's flat availability_topic points at.
func (l Layout) Bridge() string { return l.Root + "/bridge/status" }

// Renderer builds the shared-model description of this bridge's fleet.
//
// It holds the three strings the rendering depends on and nothing else — no
// client, no broker, no state. Constructing one has no side effects and
// calling its methods puts nothing on a wire.
type Renderer struct {
	// Root is the bridge MQTT topic root, config.MQTTTopic. It namespaces
	// every unique_id, which is why changing it orphans every entity (F2).
	Root string
	// Lang is the display language, config.Language.
	Lang string
}

// Layout returns the topic layout this renderer's context uses.
func (r Renderer) Layout() Layout { return Layout{Root: r.Root} }

// Context is the [discovery.Context] for this bridge: the shared library's
// StdContext with the three identity answers and the availability list
// overridden.
func (r Renderer) Context() Context {
	return Context{
		StdContext: discovery.StdContext{
			Layout: r.Layout(),
			// Namespace is deliberately empty. StdContext.UniqueID is
			// overridden and never consulted, and filling this with the
			// bridge root would contradict the library's own rule that a
			// unique-id namespace "must be a constant of the bridge, never
			// configurable" — which this bridge breaks (F2) and which this
			// step must not appear to endorse.
			Namespace: "",
			Lang:      r.Lang,
			// The state plane publishes a bare scalar, not an envelope, so
			// no component gets a value_template. Flipping this to
			// EnvelopeEncoding adds one to all 29 payloads.
			Enc: discovery.RawEncoding,
		},
	}
}

// Device builds the shared-model device for one point's owner: the main unit
// when packSN is empty, that unit's battery sub-device otherwise.
//
// The identifier carries no namespace, which [hamodel.Identifier] documents
// as the escape hatch for a published fleet rather than as an edge case: Home
// Assistant keys its device registry on these strings and has no migration
// path for them, so a fleet already published under "zendure2mqtt_<sn>" has
// to be able to keep that spelling verbatim. A namespaced identifier would
// render "<ns>:zendure2mqtt_<sn>" and leave every existing device behind with
// its area and its name override.
//
// A sub-device is an ordinary device with Via set; nothing in the library
// special-cases the hierarchy, which is exactly this bridge's pack shape.
func (r Renderer) Device(dev source.Device, report *model.Report, packSN string) *hamodel.Device {
	unitID := r.Root + "_" + dev.SN
	out := &hamodel.Device{
		Name:         hamodel.L(hass.DeviceName(dev, packSN)),
		Manufacturer: Manufacturer,
		SerialNumber: dev.SN,
	}
	if packSN == "" {
		out.Identity = hamodel.Identity{IDs: []hamodel.Identifier{{Value: unitID}}}
		out.Model = dev.Model
		if report != nil && report.Product != "" {
			out.ModelID = report.Product
		}
		if dev.Address != "" {
			// The device's own local HTTP API host. A cloud-reached device
			// has no Address by construction and gets no configuration_url.
			out.ConfigURL = "http://" + dev.Address
		}
		return out
	}
	out.Identity = hamodel.Identity{IDs: []hamodel.Identifier{{Value: unitID + "_pack_" + packSN}}}
	out.Model = PackModel
	out.SerialNumber = packSN
	out.Via = &hamodel.Identity{IDs: []hamodel.Identifier{{Value: unitID}}}
	out.SWVersion = hass.PackSoftVersion(report, packSN)
	return out
}

// Entity builds the shared-model entity for one resolved point, or reports
// false for a point that mints no Home Assistant entity.
//
// The skip rule is the production one: a point with no catalog entry, or an
// entry with no platform, is published as a state topic and nothing else.
func (r Renderer) Entity(dev source.Device, p process.Point) (*Entity, bool) {
	if p.Entry == nil || p.Entry.Platform == "" {
		return nil, false
	}
	e := *p.Entry
	platform := hacatalog.Platform(e.Platform)
	slot := Slot(dev.SN, p)

	mode := hamodel.Read
	if e.Writable {
		mode = hamodel.ReadWrite
	}
	binds := []hamodel.Binding{{Role: hamodel.RoleState, Slot: slot, Mode: mode}}
	if e.Writable {
		binds = append(binds, hamodel.Binding{Role: hamodel.RoleCommand, Slot: slot, Mode: mode})
	}

	desc := hamodel.Description{
		// The name is resolved eagerly into the context's language rather
		// than carried as a Localized with a "de" entry, because
		// Entry.FriendlyName already falls back name_de -> name -> property
		// and reproducing that fallback inside a Localized would be a second
		// answer to the same question.
		Name:        hamodel.L(e.FriendlyName(r.Lang)),
		DeviceClass: hamodel.DeviceClass(e.DeviceClass),
		Unit:        hamodel.Unit(e.Unit),
		// LevelNone suppresses both the availability list and
		// availability_mode. Resolved() defaults an empty Availability to
		// bridge+device with mode "all", and a mode beside an absent list is
		// the one combination Home Assistant reads as a contradiction.
		Availability: hamodel.NoAvailability(),
	}

	// Each projection below is gated to the platform the production payload
	// gates it to. The library gates most of them again against Home
	// Assistant's own schema, so an ungated projection would mostly be
	// harmless — but "mostly" is how a key ends up on a platform that drops
	// it in silence, and the pins would then agree with the wrong thing.
	switch platform {
	case hacatalog.PlatformSensor:
		desc.StateClass = sensorStateClass(e.DeviceClass, e.Unit)
	case hacatalog.PlatformNumber:
		desc.Min, desc.Max, desc.Step = e.Min, e.Max, e.Step
	case hacatalog.PlatformSelect:
		desc.Options = options(e, r.Lang)
	default:
		// The catalog loader accepts exactly five platforms. Three are
		// answered above and switch is answered in [Entity.BuildDiscovery],
		// where its payload_on/payload_off belong. The fifth is
		// binary_sensor, which the loader admits and the documentation
		// offers while Discovery.config has no
		// case for it — F7 of the phase-5 measurement: the entity would be
		// published with a raw numeric state and no payload_on/payload_off
		// to interpret it. 0 of 27 catalog entries use it, so this is a trap
		// for the next person editing zendure.yaml rather than a live defect,
		// and reproducing production means reproducing the gap: adding the
		// two keys here would be a payload change inside the one step whose
		// purpose is to prove there is none.
	}

	return &Entity{
		Basic: hamodel.Basic{
			EntityKey:      p.Topic,
			EntityPlatform: platform,
			Description:    desc,
			Binds:          binds,
		},
		uniqueID:   hass.UniqueID(r.Root, dev.SN, p.PackSN, p.Topic),
		objectSeed: hass.EntityObjectID(hass.DeviceName(dev, p.PackSN), p.Topic),
		availTopic: r.Layout().Bridge(),
	}, true
}

// Entities builds the shared-model entities for a device's resolved points,
// in the order the points arrive, skipping the ones that mint no entity.
func (r Renderer) Entities(dev source.Device, points []process.Point) []*Entity {
	out := make([]*Entity, 0, len(points))
	for _, p := range points {
		if e, ok := r.Entity(dev, p); ok {
			out = append(out, e)
		}
	}
	return out
}

// sensorStateClass is the production rule, verbatim: an energy sensor
// accumulates, anything else carrying a unit is a measurement, and a unitless
// sensor gets no state class. Getting this wrong corrupts long-term
// statistics irreversibly, which is why it is not guessed from the unit alone.
func sensorStateClass(deviceClass, unit string) hacatalog.StateClass {
	switch {
	case deviceClass == "energy":
		return hacatalog.StateClassTotalIncreasing
	case unit != "":
		return hacatalog.StateClassMeasurement
	default:
		return ""
	}
}

// options renders a select's option list as a [hamodel.Enum].
//
// Codes are taken in the catalog's own ascending-code order and only codes
// that actually carry a label are listed: Entry.Options skips an unlabelled
// code, while Enum.Label falls back to the code itself, so listing everything
// would publish a bare number as an option. Returning nil for an entry with
// no value map is a divergence from production and is recorded as such — see
// the package's own test — rather than papered over here.
func options(e catalog.Entry, lang string) *hamodel.Enum {
	labels := e.Options(lang)
	if len(labels) == 0 {
		return nil
	}
	enum := &hamodel.Enum{
		Codes:  make([]string, 0, len(labels)),
		Labels: make(map[string]hamodel.Localized, len(labels)),
	}
	for _, code := range e.Codes() {
		label, ok := e.Label(code, lang)
		if !ok {
			continue
		}
		enum.Codes = append(enum.Codes, code)
		enum.Labels[code] = hamodel.L(label)
	}
	return enum
}

// Entity is one Home Assistant entity of this bridge, in the shared model.
//
// It embeds [hamodel.Basic] for the description and the bindings and adds the
// two identity strings the migration freezes plus the flat availability triple
// this bridge publishes. The identity strings are fields rather than
// recomputed in [Context] because the context is handed an entity and a
// device, not a point: the serial, the pack serial and the topic leaf are no
// longer separable there, and reassembling them from the rendered device
// block is how a second formula gets written.
type Entity struct {
	hamodel.Basic

	uniqueID   string
	objectSeed string
	availTopic string
}

var (
	_ hamodel.Entity    = (*Entity)(nil)
	_ discovery.Builder = (*Entity)(nil)
)

// HAUniqueID is the entity's frozen unique_id.
func (e *Entity) HAUniqueID() string { return e.uniqueID }

// HAObjectID is the entity's frozen default_entity_id seed, without the
// platform prefix the render pipeline adds.
func (e *Entity) HAObjectID() string { return e.objectSeed }

// BuildDiscovery writes the keys this bridge publishes that no
// [hamodel.Description] carries.
//
// The flat availability triple is here rather than in the description because
// the description's availability vocabulary is the list form: a
// [hamodel.Availability] renders an `availability` array of entries, and this
// bridge's installed payloads carry availability_topic, payload_available and
// payload_not_available as three top-level keys. Home Assistant accepts both
// and the library types both, so preserving the flat form is a choice and not
// a workaround — changing it would rewrite all 29 retained configs for no
// operator-visible gain.
//
// payload_on/payload_off go through [discovery.SwitchFields] because they are
// switch-platform keys with no place on a Description, and the two virtual
// switches publish "1"/"0" rather than Home Assistant's ON/OFF default.
func (e *Entity) BuildDiscovery(_ discovery.Context, comp *discovery.Component) error {
	comp.AvailabilityTopic = e.availTopic
	comp.PayloadAvailable = PayloadAvailable
	comp.PayloadNotAvail = PayloadNotAvailable
	if e.EntityPlatform == hacatalog.PlatformSwitch {
		comp.Fields = discovery.SwitchFields{
			PayloadOn:  SwitchPayloadOn,
			PayloadOff: SwitchPayloadOff,
		}
	}
	return nil
}

// Context is this bridge's [discovery.Context].
//
// It embeds [discovery.StdContext] and overrides the four methods whose
// library defaults would move a string Home Assistant cannot be migrated
// off. Everything else — the topics, the language, the encoding — is the
// StdContext's, which is the whole point of the embedding.
type Context struct {
	discovery.StdContext
}

var _ discovery.Context = Context{}

// identified is what [Context] needs of an entity to answer for its identity.
// Satisfied by [Entity]; nothing else in this bridge has a frozen identity to
// report.
type identified interface {
	HAUniqueID() string
	HAObjectID() string
}

// UniqueID returns the entity's frozen unique_id.
//
// The library's own default is StdContext.UniqueID, which slugs the device
// identity — and slugging case-folds, so "zendure2mqtt_SF2400AC0012345_…"
// would come back as "zendure2mqtt_sf2400ac0012345_…". Home Assistant has no
// unique-id migration path of any kind, so that single character-case change
// is 29 entities losing their history, their area and every automation that
// names them. It is the specific hazard the phase-5 measurement told this
// step to watch for.
//
// An entity that carries no frozen identity gets the empty string rather than
// a computed one, and Validate then reports the missing unique_id. Falling
// through to StdContext here would be the case-fold, applied silently, to
// whichever entity someone later added without thinking about it.
func (c Context) UniqueID(_ *hamodel.Device, e hamodel.Entity) string {
	if id, ok := e.(identified); ok {
		return id.HAUniqueID()
	}
	return ""
}

// ObjectID returns the entity's frozen default_entity_id seed.
//
// The library's default seeds it from the topic root and the entity key;
// this bridge seeds it from the *device name* and the topic leaf, so its
// first token is "zendure" and not "zendure2mqtt". The two strings therefore
// live in different namespaces by accident of history, and the accident is
// load-bearing: default_entity_id is what Home Assistant assigns an entity id
// from at first discovery, and a changed seed renames every entity on a new
// install while leaving every existing one alone — the divergence that would
// ship green.
//
// An entity with no frozen seed returns "", which suppresses the key
// entirely. That is the right answer and not a degradation: a consumer whose
// fleet never carried the key must be able to keep it absent.
func (c Context) ObjectID(_ *hamodel.Device, e hamodel.Entity) string {
	if id, ok := e.(identified); ok {
		return id.HAObjectID()
	}
	return ""
}

// Availability returns no availability list, because this bridge publishes
// the flat triple instead — written by [Entity.BuildDiscovery].
//
// Returning nil here rather than leaving StdContext's answer in place is what
// keeps the two forms from both appearing. A payload carrying an
// `availability` array *and* availability_topic is not a merge; Home
// Assistant reads the list and the flat keys become dead weight, so the
// entity's availability would quietly start coming from a topic this bridge
// does not publish.
func (Context) Availability(*hamodel.Device, hamodel.Entity) []discovery.AvailabilityEntry {
	return nil
}

// NodeID is the topic segment a device document is published under.
//
// It is the device identifier verbatim — the same string the device block's
// identifiers carry, "zendure2mqtt_<sn>" for a unit and
// "zendure2mqtt_<sn>_pack_<packSN>" for a battery pack — and choosing it was
// the one free decision ADR 0070 phase 5 step 5 had to make. The node id is a
// topic segment this fleet has never had: the per-entity configs it replaces
// are keyed on the unique_id and carry no node-id level, so there is no
// installed spelling to preserve. Home Assistant keys its *device* registry
// on `identifiers`, not on the node id, so nothing in the registry depends on
// the choice.
//
// Three properties are required of it and the device identifier has all
// three. It is stable across restarts, because it is derived from the serial
// the device reports and from the MQTT root and from nothing else — no
// counter, no map iteration order, no boot time. It is distinct per device,
// because the identifier is: a pack's identifier is its unit's plus
// "_pack_<packSN>". And it is a legal single topic segment, which
// [discovery.Validate] refuses one that is not. A re-keying later would
// orphan the retained document topic — the old document would stay on the
// broker announcing the same device from a second topic — so it is picked
// once, here, and frozen with the rest of the identity.
//
// It is deliberately NOT slugged. The library's own default node id runs the
// device identity through topic.Slug, which case-folds, so this fleet's
// "zendure2mqtt_SF2400AC0012345" would become
// "zendure2mqtt_sf2400ac0012345" — the same case-fold the measurement flagged
// against unique_id. Here it would merely be ugly rather than destructive,
// since nothing keys on it, but it would also disagree with every other
// appearance of the serial in this bridge's tree: the state topics come from
// process.StateTopic, whose serial is verbatim, and so are the identifiers.
// One spelling of a serial, everywhere. Note also that topic.Slug and the
// topic.Safe this bridge's own tree uses disagree — one more reason to take
// neither and keep the identifier.
func (Context) NodeID(dev *hamodel.Device) string {
	if dev == nil {
		return ""
	}
	return dev.Identity.UID()
}

// OriginName is the `origin.name` every device document carries.
//
// Home Assistant requires an origin block on a device document — it is the
// one key of the migration that cannot be preserved, because the per-entity
// configs this bridge published carried none and [discovery.Validate] refuses
// a document without `origin.name`. It is additive and identity-neutral: Home
// Assistant shows it as the integration that announced the device and keys
// nothing on it.
const OriginName = "go-zendure2mqtt"

// Origin is the origin block this bridge publishes.
//
// Name only, deliberately. [discovery.Origin] also carries `sw_version` and
// `support_url`, and this bridge's own version is the obvious candidate for
// the first — but it is stamped at link time, so the retained document's
// bytes would then depend on how the binary was linked, the pinned payload
// would depend on it too, and every release would rewrite two retained
// documents for no operator-visible gain. The build banner already says the
// version, once, at boot.
func Origin() discovery.Origin { return discovery.Origin{Name: OriginName} }

// Bundle renders the retained Home Assistant device document for one of this
// bridge's devices: the main unit when packSN is empty, that unit's battery
// sub-device otherwise.
//
// points may hold the whole report's points; only the ones belonging to
// packSN are taken, in arrival order, so a caller need not partition first.
// A sub-device is not a component of its parent's document — it is a device,
// with its own node id and its own retained document — which is why this
// takes one packSN rather than rendering the hierarchy in one call.
//
// It returns nil without an error when the device mints no entity at all. A
// document with an empty `components` map is not a device with no entities,
// it is a document Home Assistant reads as "remove every entity of this
// device", and this bridge must never publish one by accident.
func (r Renderer) Bundle(
	dev source.Device,
	report *model.Report,
	packSN string,
	points []process.Point,
) (*discovery.Bundle, error) {
	entities := make([]hamodel.Entity, 0, len(points))
	for _, p := range points {
		if p.PackSN != packSN {
			continue
		}
		if e, ok := r.Entity(dev, p); ok {
			entities = append(entities, e)
		}
	}
	if len(entities) == 0 {
		return nil, nil
	}
	return discovery.Render(r.Context(), r.Device(dev, report, packSN), entities, Origin())
}

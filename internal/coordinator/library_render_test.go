// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	hacatalog "github.com/SukramJ/go-ha-catalog"
	"github.com/SukramJ/go-hamqtt/discovery"
	hamodel "github.com/SukramJ/go-hamqtt/model"
	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// discoveryPrefix is the Home Assistant discovery root this bridge publishes
// under, as the daemon wires it in cmd/zendure2mqtt.
const discoveryPrefix = "homeassistant"

// resolvePoints runs the daemon's own point resolution — the shipped catalog
// through process.Resolve, plus the two virtual switches minted in code — and
// returns the points in the order the publish path sees them.
//
// It is the publish path's first two lines and nothing after them. The
// library render below starts from the same points as hass.Discovery does, so
// a difference in the rendered payload is a difference in the rendering and
// not in what was resolved.
func resolvePoints(t *testing.T, dev source.Device, report *model.Report) []process.Point {
	t.Helper()
	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	c := New(Deps{
		Cfg:     cfg,
		Backend: &goldenBackend{devices: []source.Device{dev}},
		MQTT:    &capturingClient{},
		Catalog: goldenCatalog(t),
		Logger:  discardLogger(),
	})
	points := process.Resolve(report, c.deps.Catalog, cfg.Language)
	return append(points, c.switchPoints(report)...)
}

// renderLibrary renders one device's entities through go-hamqtt and returns
// them in the shape the pins are stored in: keyed on unique_id, carrying the
// retained per-entity config topic and the decoded payload.
//
// Three library entry points do the work and none of them is reimplemented
// here:
//
//   - discovery.RenderComponent turns a hamodel.Entity into the per-entity
//     discovery form, device block and all.
//   - Component.EntityJSON encodes it without `platform` — the bundle's
//     discriminator, which Home Assistant declares on no platform and drops
//     in silence from a per-entity config.
//   - publisher.LegacyTopicByUniqueID renders the topic. Deliberately: the
//     topic form is the one thing about this migration that fails silently
//     (see TestLegacyTopicFormIsKeyedOnUniqueID), so it is proved against the
//     pins rather than formatted here and assumed.
func renderLibrary(t *testing.T, dev source.Device, report *model.Report) map[string]goldenEntry {
	t.Helper()

	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	ctx := r.Context()

	out := map[string]goldenEntry{}
	for _, p := range resolvePoints(t, dev, report) {
		ent, ok := r.Entity(dev, p)
		if !ok {
			continue
		}
		owner := r.Device(dev, report, p.PackSN)

		// Origin is the zero value on purpose. Home Assistant requires an
		// origin block on a device bundle and treats it as optional on a
		// per-entity config, and this bridge publishes none today — so the
		// per-entity comparison must not grow one. The bundle rendered by
		// TestRenderedBundleValidates does carry one, which is where the
		// additive key belongs.
		comp, err := discovery.RenderComponent(ctx, owner, ent, discovery.Origin{})
		if err != nil {
			t.Fatalf("%s: RenderComponent: %v", ent.HAUniqueID(), err)
		}
		raw, err := comp.EntityJSON()
		if err != nil {
			t.Fatalf("%s: EntityJSON: %v", ent.HAUniqueID(), err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: unmarshal rendered config: %v", ent.HAUniqueID(), err)
		}

		topic := publisher.LegacyTopicByUniqueID(publisher.LegacyEntity{
			Prefix:    discoveryPrefix,
			Platform:  string(comp.Platform),
			NodeID:    ctx.NodeID(owner),
			ObjectID:  ent.Key(),
			UniqueID:  comp.UniqueID,
			Component: comp,
		})
		if prev, dup := out[comp.UniqueID]; dup {
			t.Fatalf("unique_id %q rendered twice: %s and %s", comp.UniqueID, prev.Topic, topic)
		}
		out[comp.UniqueID] = goldenEntry{Topic: topic, Payload: body}
	}
	return out
}

// diffAgainstPin compares a rendered entry set against a pinned one and
// returns one line per difference, empty when the two are identical.
//
// It returns the differences instead of reporting them so the same comparison
// can be used twice: once to assert equality, and once — in
// TestLibraryRenderDiffCatchesMutations — to assert that it *notices*. An
// adversarial review of this programme found five library tests that could
// not fail; a comparison whose sensitivity is itself asserted is the cheapest
// guard against being the sixth.
//
// Missing, extra and changed rows are distinguished because they mean
// different things on a Home Assistant registry: a missing row is an entity
// that would vanish, an extra row is one that would appear beside the old
// one, and a changed row is an entity whose config moved under it.
func diffAgainstPin(want, got map[string]goldenEntry) []string {
	var out []string
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		w, pinned := want[k]
		if !pinned {
			out = append(out, fmt.Sprintf("%s: rendered by the library but not in the pin — the library would create an entity this bridge does not publish", k))
			continue
		}
		if got[k].Topic != w.Topic {
			out = append(out, fmt.Sprintf("%s: topic\n  library %s\n  pinned  %s", k, got[k].Topic, w.Topic))
		}
		g, wb := mustCanonical(got[k].Payload), mustCanonical(w.Payload)
		if g != wb {
			out = append(out, fmt.Sprintf("%s: payload\n  library %s\n  pinned  %s", k, g, wb))
		}
	}
	missing := make([]string, 0)
	for k := range want {
		if _, still := got[k]; !still {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		out = append(out, fmt.Sprintf("%s: in the pin but the library renders no such entity — the entity would disappear", k))
	}
	return out
}

// mustCanonical is canonicalJSON without a *testing.T, so diffAgainstPin can
// be called from a helper that is not reporting to one.
func mustCanonical(body map[string]any) string {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(raw)
}

// legacyPins loads the frozen per-entity pin — the 29 retained configs every
// release before ADR 0070 phase 5 step 5 published, captured from the
// production code before one byte moved — split into the unit's rows and the
// battery pack's.
//
// The tests below compare the library's per-entity rendering against *that*
// pin rather than against the current one, and they must: the current pin is
// the device-document form this bridge publishes now, and what these tests
// prove is the migration's premise — that the shared model reproduces what
// the installed base is running on, byte for byte, 29 of 29. That question
// does not change when the topic does, and the answer is what makes the move
// a re-addressing rather than a rewrite.
func legacyPins(t *testing.T) (unit, pack map[string]goldenEntry) {
	t.Helper()
	var all map[string]goldenEntry
	readGoldenJSON(t, legacyConfigPath, &all)
	if len(all) != 29 {
		t.Fatalf("%s holds %d rows, want 29", legacyConfigPath, len(all))
	}
	return splitByPack(all)
}

// legacyAll is [legacyPins] unsplit.
func legacyAll(t *testing.T) map[string]goldenEntry {
	t.Helper()
	unit, pack := legacyPins(t)
	for uid, e := range pack {
		unit[uid] = e
	}
	return unit
}

// TestLibraryRenderReproducesThePinnedPayloads is the decisive experiment of
// ADR 0070 phase 5: the shared go-hamqtt model, rendering this bridge's 29
// entities, produces the exact payloads and the exact retained topics PR #39
// pinned from the production code.
//
// Everything the whole migration rests on is in that sentence, and until this
// test ran it was derived from reading the library rather than from running
// it. The measurement (§3.1) listed every key and its source and could still
// only write "cannot name the exact byte sequence, because no consumer exists
// yet" against the rendered payload. If the bytes match, every later step is
// a switch-over with a pin behind it. If they do not, the difference is a
// finding about either this bridge or the library, and this — a branch with
// nothing on a broker — is the cheapest place in the programme for it to
// surface. The alternative place is a user's Home Assistant, where a changed
// unique_id is an entity that silently loses its history and a changed
// state_topic is one that silently goes unknown forever.
//
// Nothing here publishes. There is no broker, no runtime, no Publisher and no
// transport: the capturing client exists only so resolvePoints can construct
// a Coordinator, and it is never handed a discovery payload. The pins are
// read, never written — this test has no -update flag and must never acquire
// one, because a pin that the thing under test can rewrite is not a pin.
//
// The comparison is on the canonical re-encoding of both decoded payloads,
// which is exact for every key, every value and every type; only key order
// and whitespace, which no MQTT consumer sees and which json.Marshal
// normalises away on both sides, are out of scope.
func TestLibraryRenderReproducesThePinnedPayloads(t *testing.T) {
	rendered := renderLibrary(t, goldenUnit(), goldenReport())
	unit, pack := splitByPack(rendered)

	wantUnit, wantPack := legacyPins(t)

	for _, tc := range []struct {
		name string
		want map[string]goldenEntry
		got  map[string]goldenEntry
	}{
		{"unit", wantUnit, unit},
		{"pack", wantPack, pack},
	} {
		for _, line := range diffAgainstPin(tc.want, tc.got) {
			t.Errorf("%s: %s", tc.name, line)
		}
	}

	// The count is asserted as well as the contents. A rendering that
	// produced nothing at all would otherwise pass the loop above and fail
	// only the "in the pin but no such entity" arm, which is the same
	// message a single dropped entity produces — and the two are not the
	// same finding.
	if n := len(unit) + len(pack); n != 29 {
		t.Errorf("library rendered %d entities (%d unit + %d pack), want 29",
			n, len(unit), len(pack))
	}
}

// TestLibraryRenderReproducesTheIdentityHazards is the same experiment over
// the four inputs whose identity strings sit on a divergence between this
// bridge's slugify and the shared library's topic.Slug — including the two
// rows that are pinned *defects*.
//
// This is the half of the proof that matters most, and it is the half a
// reasonable person would have skipped. The 29 payloads above are ASCII with
// no hyphens; they would match under either slug. These four do not:
//
//   - "umlaut-u" ("Balkon Süd"): slugify folds "ü" to "u", topic.Slug
//     expands it to "ue".
//   - "non-german-accent" ("Café Nord"): slugify has no mapping for "é" and
//     folds it to a separator, losing the character. That is the same class
//     as the defect the library's own slug documents ("Größe → gr_e").
//   - "hyphen-in-name" ("Haus-Nord"): slugify folds "-" to "_", topic.Slug
//     preserves it.
//   - "pack-serial-hyphen-vs-underscore": F8. Packs "AB-12" and "AB_12" slug
//     to the same "ab_12", so two distinct unique_ids carry one identical
//     default_entity_id and Home Assistant resolves the collision by
//     suffixing, without saying so.
//
// Reproducing a defect is the requirement, not an accident of it. A pinned
// defect is one whose later fix can be *seen*; if the parallel path quietly
// did the right thing here, the eventual slug swap would show up as no diff
// at all and nobody would learn which entities it renamed. That is why
// internal/harender calls hass.EntityObjectID rather than restating the
// formula: a copy can be fixed on one side while the pin stays green.
//
// Each case carries the two virtual switches as well, which take their seed
// from the same device name, so every divergence is proved on three platforms
// rather than one.
func TestLibraryRenderReproducesTheIdentityHazards(t *testing.T) {
	var want map[string]goldenEntry
	readGoldenJSON(t, legacyIdentityPath, &want)

	got := map[string]goldenEntry{}
	for _, c := range identityCases() {
		for uid, e := range renderLibrary(t, c.dev, c.report) {
			got[c.name+"/"+uid] = e
		}
	}

	for _, line := range diffAgainstPin(want, got) {
		t.Errorf("%s", line)
	}
	if len(got) != len(want) {
		t.Errorf("library rendered %d hazard payloads, pin has %d", len(got), len(want))
	}
}

// TestLibraryRenderReproducesTheStateTopics proves the other half of the wire
// through the library's own topic layer: every state topic the bridge
// publishes comes back identical from harender.Layout, which delegates to
// process.StateTopic.
//
// The state plane is the half Home Assistant's registry does not protect. A
// changed unique_id orphans an entity visibly — it disappears and a new one
// appears in its place. A changed state_topic leaves the entity exactly where
// it is and makes it permanently unknown, with nothing anywhere to say why.
//
// The pinned list has 30 entries for 29 entities: every resolved point gets a
// state topic, including the pack's softVersion, which has no catalog entry
// and therefore mints no entity. The library path must reproduce all 30, not
// just the 29 that carry a config — the state plane is addressed by point,
// not by entity, and a layout that only answered for entities would drop the
// uncatalogued values this bridge deliberately still publishes.
func TestLibraryRenderReproducesTheStateTopics(t *testing.T) {
	var want []string
	readGoldenJSON(t, goldenStateTopicPath, &want)

	layout := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}.Layout()
	dev := goldenUnit()

	got := make([]string, 0, len(want))
	for _, p := range resolvePoints(t, dev, goldenReport()) {
		got = append(got, layout.State(harender.Slot(dev.SN, p)))
	}
	sort.Strings(got)

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("library state topics differ from the pin\n library %v\n pinned  %v", got, want)
	}
}

// TestLibraryRenderReproducesTheCommandTopics does the same for the writable
// points' command topics.
//
// They are pinned only inside the discovery payloads, so a layout that got
// them wrong while getting state right would be caught above — but only for
// the seven writable entities, and only as part of a whole-payload diff. This
// states it directly, because a wrong command_topic is the failure mode of F3
// generalised: an entity that accepts input in Home Assistant and silently
// does nothing.
func TestLibraryRenderReproducesTheCommandTopics(t *testing.T) {
	layout := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}.Layout()
	dev := goldenUnit()

	writable := 0
	for _, p := range resolvePoints(t, dev, goldenReport()) {
		if p.Entry == nil || !p.Entry.Writable {
			continue
		}
		writable++
		want := process.CommandTopic("zendure2mqtt", dev.SN, p)
		if got := layout.Command(harender.Slot(dev.SN, p)); got != want {
			t.Errorf("%s: command topic\n library %s\n bridge  %s", p.Topic, got, want)
		}
	}
	// 5 numbers + 2 selects + 2 virtual switches.
	if writable != 9 {
		t.Errorf("found %d writable points, want 9", writable)
	}
}

// TestLegacyTopicFormIsKeyedOnUniqueID settles the question go-hamqtt v0.27.0
// added LegacyTopicFunc for and then deliberately left to the consumer: which
// per-entity discovery topic form this bridge's installed fleet is on.
//
// The library cannot guess it — too narrow and PublishBundle retracts nothing,
// too wide and it retracts a topic belonging to another writer in a shared
// discovery tree — so the consumer states it. This test is the evidence
// behind the statement, taken against the pinned topics rather than against
// the library's doc comment: publisher.LegacyTopicByUniqueID reproduces all
// 29 pinned config topics exactly, and publisher.LegacyTopicByObjectID
// reproduces none of them, because this bridge's component key is the bare
// topic leaf ("electric_level") while its topic segment is the full unique id
// ("zendure2mqtt_SF2400AC0012345_electric_level").
//
// # Why this is worth a test of its own
//
// Nothing uses it yet, and that is the point: it has to be right before step
// 5 needs it, and step 5 is the one step of the migration whose failure is
// silent. v0.26.0's SupersededTopics rendered exactly one shape,
// <prefix>/<platform>/<node_id>/<object_id>/config — five segments. This
// bridge's 29 configs have four and no node-id level at all, so that
// SupersededTopics would have matched none of them and Runtime.PublishBundle
// would have retracted nothing before publishing. Home Assistant then refuses
// the bundle, entity by entity, because a per-entity config for the same
// unique_id is still retained — and the entire evidence is one line:
//
//	WARNING [mqtt.entity] Received a conflicting MQTT discovery message
//
// The bundle publishes, the broker accepts it, the log is otherwise clean,
// and the entities keep their old configs. The measurement called it "the
// single highest-risk step in the whole migration and the one most likely to
// be missed, because everything *looks* right."
//
// So: Config.LegacyEntityTopics must be set to
// publisher.LegacyTopicByUniqueID when step 5 wires the runtime up, and not
// to LegacyTopicByObjectID and not left unset.
func TestLegacyTopicFormIsKeyedOnUniqueID(t *testing.T) {
	pinned := legacyAll(t)

	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	ctx := r.Context()
	dev := goldenUnit()
	report := goldenReport()

	checked, byObjectIDMatches := 0, 0
	for _, p := range resolvePoints(t, dev, report) {
		ent, ok := r.Entity(dev, p)
		if !ok {
			continue
		}
		owner := r.Device(dev, report, p.PackSN)
		le := publisher.LegacyEntity{
			Prefix:   discoveryPrefix,
			Platform: p.Entry.Platform,
			NodeID:   ctx.NodeID(owner),
			ObjectID: ent.Key(),
			UniqueID: ent.HAUniqueID(),
		}
		want, ok := pinned[ent.HAUniqueID()]
		if !ok {
			t.Fatalf("%s: no pinned config topic", ent.HAUniqueID())
		}
		checked++
		if got := publisher.LegacyTopicByUniqueID(le); got != want.Topic {
			t.Errorf("%s: LegacyTopicByUniqueID\n got %s\nwant %s", ent.HAUniqueID(), got, want.Topic)
		}
		if publisher.LegacyTopicByObjectID(le) == want.Topic {
			byObjectIDMatches++
		}
		// The five-segment default is what SupersededTopics rendered before
		// v0.27.0, and it must not match — that is the whole reason the
		// consumer has to state its form.
		if got := publisher.LegacyTopicWithNodeID(le); got == want.Topic {
			t.Errorf("%s: the five-segment default matched the pinned topic %s; the premise of v0.27.0's LegacyTopicFunc no longer holds for this bridge",
				ent.HAUniqueID(), got)
		}
	}

	if checked != 29 {
		t.Errorf("checked %d topics, want 29", checked)
	}
	if byObjectIDMatches != 0 {
		t.Errorf("LegacyTopicByObjectID matched %d of %d pinned topics; the two forms were expected to be disjoint for this bridge, so the verdict in the doc comment needs re-taking",
			byObjectIDMatches, checked)
	}
}

// TestPinnedPayloadsPassTheLibraryValidator answers the second of the three
// questions the measurement could not settle without running code: whether
// discovery.Validate accepts a payload whose components carry the flat
// availability_topic / payload_available / payload_not_available triple
// instead of the `availability` list.
//
// It is asked of the *pinned* payloads — the ones production publishes today
// — rather than only of the library's re-render, because those are the bytes
// an installed base is running on. Home Assistant's MQTT discovery schemas
// are extra=REMOVE_EXTRA: a key it does not declare vanishes on arrival with
// no error on the wire and no line in any log. ValidateBody is the only way
// to learn that from outside Home Assistant, and this bridge has never once
// asked.
//
// A failure here is a real finding and not a test to relax: it means either
// this bridge publishes a key Home Assistant is silently dropping, or the
// library's schema view is wrong for these four platforms. Report which.
func TestPinnedPayloadsPassTheLibraryValidator(t *testing.T) {
	for _, path := range []string{legacyConfigPath, legacyIdentityPath} {
		var pins map[string]goldenEntry
		readGoldenJSON(t, path, &pins)

		keys := make([]string, 0, len(pins))
		for k := range pins {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			platform := platformOfTopic(t, pins[k].Topic)
			if err := discovery.ValidateBody(platform, pins[k].Payload); err != nil {
				t.Errorf("%s: %s: the payload this bridge publishes today does not validate: %v", path, k, err)
			}
		}
	}
}

// TestLibraryRenderPassesTheLibraryValidator is the same check on the
// library's output. It is not redundant with the test above: the two payloads
// are asserted byte-equal elsewhere, so a divergence here would mean the
// equality assertion is not comparing what it claims to.
func TestLibraryRenderPassesTheLibraryValidator(t *testing.T) {
	for uid, e := range renderLibrary(t, goldenUnit(), goldenReport()) {
		if err := discovery.ValidateBody(platformOfTopic(t, e.Topic), e.Payload); err != nil {
			t.Errorf("%s: library-rendered payload does not validate: %v", uid, err)
		}
	}
}

// platformOfTopic reads the platform out of a per-entity config topic:
// <prefix>/<platform>/<unique_id>/config.
func platformOfTopic(t *testing.T, topic string) hacatalog.Platform {
	t.Helper()
	parts := strings.Split(topic, "/")
	if len(parts) != 4 {
		t.Fatalf("config topic %q is not <prefix>/<platform>/<unique_id>/config", topic)
	}
	return hacatalog.Platform(parts[1])
}

// TestRenderedBundleValidates renders the device bundle step 5 will eventually
// publish and puts it through discovery.Validate — the whole-document
// validator, which the per-component ValidateBody above is a part of.
//
// This is the first of the measurement's three unknowns: the rendered bundle
// bytes. It is checked here and deliberately not pinned. Pinning it would
// freeze a payload nothing publishes yet, and the two keys that are new in
// the bundle form — `platform` per component and the `origin` block — are
// exactly the ones step 5 has to introduce deliberately, with its own note.
//
// The origin block is the one key that cannot be preserved: Validate requires
// origin.name on a device bundle where Home Assistant treats it as optional
// on a per-entity config. It is additive and identity-neutral, and it closes
// one of the five backlog items ADR 0070 counted against all six consumers.
//
// Still nothing publishes. Validate reads a *Bundle in memory.
func TestRenderedBundleValidates(t *testing.T) {
	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	ctx := r.Context()
	dev := goldenUnit()
	report := goldenReport()
	points := resolvePoints(t, dev, report)

	// One bundle per Home Assistant device: the unit, and one per battery
	// pack. Sub-devices are not components of their parent's document — they
	// are devices, with their own node id and their own retained bundle.
	byPack := map[string][]process.Point{}
	order := []string{}
	for _, p := range points {
		if p.Entry == nil || p.Entry.Platform == "" {
			continue
		}
		if _, seen := byPack[p.PackSN]; !seen {
			order = append(order, p.PackSN)
		}
		byPack[p.PackSN] = append(byPack[p.PackSN], p)
	}

	origin := discovery.Origin{Name: "go-zendure2mqtt"}
	total := 0
	for _, packSN := range order {
		owner := r.Device(dev, report, packSN)
		entities := make([]hamodel.Entity, 0, len(byPack[packSN]))
		for _, p := range byPack[packSN] {
			ent, ok := r.Entity(dev, p)
			if !ok {
				continue
			}
			entities = append(entities, ent)
		}

		bundle, err := discovery.Render(ctx, owner, entities, origin)
		if err != nil {
			t.Fatalf("pack %q: Render: %v", packSN, err)
		}
		if err := discovery.Validate(bundle); err != nil {
			t.Errorf("pack %q: the rendered bundle does not validate: %v", packSN, err)
		}

		wantNode := ctx.NodeID(owner)
		if bundle.NodeID != wantNode {
			t.Errorf("pack %q: node id %q, want %q", packSN, bundle.NodeID, wantNode)
		}
		if got, want := bundle.Topic(discoveryPrefix), discoveryPrefix+"/device/"+wantNode+"/config"; got != want {
			t.Errorf("pack %q: bundle topic\n got %s\nwant %s", packSN, got, want)
		}
		// Every component must carry the identity the per-entity form
		// carries. This is what makes the bundle a re-address of the same
		// entities rather than a new set of them, and it is what Home
		// Assistant's registry survives the migration on.
		for key, comp := range bundle.Components {
			if comp.UniqueID == "" {
				t.Errorf("pack %q: component %q has no unique_id", packSN, key)
			}
			if comp.Platform == "" {
				t.Errorf("pack %q: component %q has no platform", packSN, key)
			}
		}
		total += len(bundle.Components)
	}

	if total != 29 {
		t.Errorf("the bundles carry %d components in total, want 29", total)
	}
	if len(order) != 2 {
		t.Errorf("rendered %d bundles, want 2 (the unit and one pack)", len(order))
	}
}

// TestSupersededTopicsRetractsThePinnedConfigs is the end-to-end form of the
// legacy-topic verdict: the list Runtime.PublishBundle would actually retract,
// derived from the rendered bundles, is exactly the 29 pinned config topics —
// no more and no fewer.
//
// The per-component check above proves LegacyTopicByUniqueID renders the
// right string. This proves the library plumbs it: SupersededTopics takes the
// form variadically, unions several and de-duplicates, and a consumer that
// passes nothing keeps v0.26.0's five-segment default. Getting the argument
// right and getting it *passed* are two failures with one silent symptom.
//
// "No more" matters as much as "no fewer". A retraction list wider than the
// fleet clears a retained config belonging to another writer in a shared
// discovery tree, which is the failure mode the library refuses to risk by
// guessing.
func TestSupersededTopicsRetractsThePinnedConfigs(t *testing.T) {
	want := legacyAll(t)
	wantTopics := make([]string, 0, len(want))
	for _, e := range want {
		wantTopics = append(wantTopics, e.Topic)
	}
	sort.Strings(wantTopics)

	r := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}
	ctx := r.Context()
	dev := goldenUnit()
	report := goldenReport()

	byPack := map[string][]hamodel.Entity{}
	order := []string{}
	for _, p := range resolvePoints(t, dev, report) {
		ent, ok := r.Entity(dev, p)
		if !ok {
			continue
		}
		if _, seen := byPack[p.PackSN]; !seen {
			order = append(order, p.PackSN)
		}
		byPack[p.PackSN] = append(byPack[p.PackSN], ent)
	}

	got := make([]string, 0, len(wantTopics))
	for _, packSN := range order {
		bundle, err := discovery.Render(ctx, r.Device(dev, report, packSN), byPack[packSN],
			discovery.Origin{Name: "go-zendure2mqtt"})
		if err != nil {
			t.Fatalf("pack %q: Render: %v", packSN, err)
		}
		got = append(got, publisher.SupersededTopics(discoveryPrefix, bundle,
			publisher.LegacyTopicByUniqueID)...)
	}
	sort.Strings(got)

	if strings.Join(got, "\n") != strings.Join(wantTopics, "\n") {
		t.Errorf("SupersededTopics would not retract this bridge's fleet\n would retract %v\n pinned        %v\n(a config left retained makes Home Assistant refuse the bundle for that entity, with one WARNING line as the only evidence)",
			got, wantTopics)
	}
}

// TestLibraryRenderDiffCatchesMutations verifies that the comparison the
// three equality tests above rest on can actually fail.
//
// It exists because an adversarial review in this programme found five
// library tests that could not: they asserted over an empty set, or compared
// a value with itself, and passed for reasons unrelated to what they claimed.
// A byte-equality assertion is especially prone to it — every one of its
// moving parts (the pin loader, the canonical encoder, the key set) fails
// *closed*, by finding nothing to compare, and an empty diff reads exactly
// like a match.
//
// So each row below perturbs the library's real output in one specific way,
// and the comparison must report it. The perturbations are not arbitrary:
// each is a way this migration could actually go wrong, and the two identity
// rows are the two the library's own defaults would have produced if
// internal/harender had not overridden them.
func TestLibraryRenderDiffCatchesMutations(t *testing.T) {
	want, _ := legacyPins(t)
	base := func() map[string]goldenEntry {
		unit, _ := splitByPack(renderLibrary(t, goldenUnit(), goldenReport()))
		return unit
	}

	// The unperturbed comparison must be clean, or every row below proves
	// nothing: a diff that is already non-empty would "catch" any mutation.
	if diff := diffAgainstPin(want, base()); len(diff) != 0 {
		t.Fatalf("the unperturbed comparison is not clean, so no mutation below proves anything:\n%s",
			strings.Join(diff, "\n"))
	}

	const probe = "zendure2mqtt_SF2400AC0012345_electric_level"

	for _, tc := range []struct {
		name   string
		mutate func(map[string]goldenEntry)
	}{{
		// What StdContext.UniqueID would have produced: the serial
		// case-folded by the slug. Home Assistant has no unique-id migration
		// path, so this single change is 29 entities losing their history.
		name: "case-folded unique_id",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Payload["unique_id"] = strings.ToLower(e.Payload["unique_id"].(string))
			m[probe] = e
		},
	}, {
		// What StdContext.ObjectID would have produced: the seed's first
		// token taken from the topic root rather than from the device name.
		// Inert for installed entities, decisive for every new install —
		// which is exactly why it would ship green.
		name: "entity-id seed from the root instead of the device name",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Payload["default_entity_id"] = strings.Replace(
				e.Payload["default_entity_id"].(string), "zendure_", "zendure2mqtt_", 1)
			m[probe] = e
		},
	}, {
		// The availability form: the flat triple replaced by the list the
		// library's StdContext renders by default. Home Assistant accepts
		// both, so nothing would complain.
		name: "availability list instead of the flat triple",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			delete(e.Payload, "availability_topic")
			delete(e.Payload, "payload_available")
			delete(e.Payload, "payload_not_available")
			e.Payload["availability"] = []any{map[string]any{"topic": "zendure2mqtt/bridge/status"}}
			m[probe] = e
		},
	}, {
		// A value_template, which EnvelopeEncoding would add to all 29
		// payloads while the state plane still publishes a bare scalar.
		name: "value_template from the envelope encoding",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Payload["value_template"] = discovery.ValueTemplate
			m[probe] = e
		},
	}, {
		// topic.Default's shape instead of this bridge's: the bucket segment
		// appears and the group moves. The entity stays exactly where it is
		// and goes unknown forever.
		name: "state topic from topic.Default",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Payload["state_topic"] = "zendure2mqtt/SF2400AC0012345/values/electric_level"
			m[probe] = e
		},
	}, {
		// The stray `platform` key four of the reference consumer's twelve
		// planes grew, and which its pins caught: legal inside a bundle,
		// dropped in silence from a per-entity config.
		name: "stray platform key",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Payload["platform"] = "sensor"
			m[probe] = e
		},
	}, {
		// A namespaced device identifier, which leaves the existing device
		// behind with its area and its name override while the entities move
		// to a new one.
		name: "namespaced device identifier",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			dev, _ := e.Payload["device"].(map[string]any)
			dev["identifiers"] = []any{"zendure:zendure2mqtt_SF2400AC0012345"}
			m[probe] = e
		},
	}, {
		// The five-segment legacy topic form: same payload, different
		// address. The old config stays retained and Home Assistant orphans
		// the entity under it.
		name: "five-segment config topic",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			e.Topic = "homeassistant/sensor/zendure2mqtt_SF2400AC0012345/electric_level/config"
			m[probe] = e
		},
	}, {
		// A dropped state_class. Home Assistant would keep the entity and
		// stop recording long-term statistics for it.
		name: "dropped state_class",
		mutate: func(m map[string]goldenEntry) {
			e := m[probe]
			delete(e.Payload, "state_class")
			m[probe] = e
		},
	}, {
		// An entity that stops being rendered at all.
		name:   "missing entity",
		mutate: func(m map[string]goldenEntry) { delete(m, probe) },
	}, {
		// An entity the pin does not have.
		name: "extra entity",
		mutate: func(m map[string]goldenEntry) {
			m["zendure2mqtt_SF2400AC0012345_invented"] = goldenEntry{
				Topic:   "homeassistant/sensor/zendure2mqtt_SF2400AC0012345_invented/config",
				Payload: map[string]any{"unique_id": "zendure2mqtt_SF2400AC0012345_invented"},
			}
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := base()
			tc.mutate(got)
			if diff := diffAgainstPin(want, got); len(diff) == 0 {
				t.Errorf("the comparison did not notice %q — it cannot fail, and neither can the tests that use it", tc.name)
			}
		})
	}
}

// TestExportedIdentityHelpersAreTheProductionOnes guards the one way the
// byte-equality proof above could become vacuous.
//
// internal/harender calls hass.UniqueID, hass.EntityObjectID,
// hass.DeviceName and hass.PackSoftVersion — the production functions — so
// that the two rendering paths cannot drift and so that a pinned defect stays
// reproduced. That is a property of the source and nothing else checks it: if
// someone later inlines one of those formulas into harender "to decouple the
// packages", both paths keep agreeing with each other and the pins keep
// passing while the proof has quietly stopped being about production code.
//
// So the four are asserted against the strings the pins actually carry, which
// is the one thing an inlined copy could not stay equal to without being the
// same formula.
func TestExportedIdentityHelpersAreTheProductionOnes(t *testing.T) {
	dev := goldenUnit()

	if got, want := hass.UniqueID("zendure2mqtt", dev.SN, "", "electric_level"),
		"zendure2mqtt_SF2400AC0012345_electric_level"; got != want {
		t.Errorf("UniqueID = %q, want %q", got, want)
	}
	if got, want := hass.UniqueID("zendure2mqtt", dev.SN, "AO4H2301X01", "current"),
		"zendure2mqtt_SF2400AC0012345_pack_AO4H2301X01_current"; got != want {
		t.Errorf("UniqueID (pack) = %q, want %q", got, want)
	}
	if got, want := hass.DeviceName(dev, ""), "Zendure SF2400AC0012345"; got != want {
		t.Errorf("DeviceName = %q, want %q", got, want)
	}
	if got, want := hass.DeviceName(dev, "AO4H2301X01"), "Zendure SF2400AC0012345 Pack AO4H2301X01"; got != want {
		t.Errorf("DeviceName (pack) = %q, want %q", got, want)
	}
	if got, want := hass.EntityObjectID(hass.DeviceName(dev, ""), "electric_level"),
		"zendure_sf2400ac0012345_electric_level"; got != want {
		t.Errorf("EntityObjectID = %q, want %q", got, want)
	}
	// F8, stated directly rather than only as two pinned rows: two pack
	// serials differing by a character the slug folds produce one seed.
	a := hass.EntityObjectID(hass.DeviceName(dev, "AB-12"), "soc_level")
	b := hass.EntityObjectID(hass.DeviceName(dev, "AB_12"), "soc_level")
	if a != b {
		t.Errorf("F8 no longer reproduces: %q != %q — if the collision was fixed, the identity pin must be refreshed as the record of that fix", a, b)
	}
	if got, want := hass.PackSoftVersion(goldenReport(), "AO4H2301X01"), "4109"; got != want {
		t.Errorf("PackSoftVersion = %q, want %q", got, want)
	}
}

// TestSlotBucketIsInertForThisBridge states, as an assertion rather than as a
// comment, the one mutation the byte-equality proof provably cannot catch.
//
// model.Slot carries a Bucket, a closed enum of five paramset names
// (unset/values/master/calculated/custom) that topic.Default renders as a
// topic segment. This bridge's topic bucket is now|config|static|battery|misc
// — five values that mean something else entirely — so harender.Slot leaves
// Bucket unset and carries the group in Slot.Path instead, and harender.Layout
// never reads the field.
//
// The consequence is that changing the bucket on every slot this bridge
// constructs changes no published byte, which is exactly what the measurement
// (§5.2) predicted: "the Bucket field is dead weight in every slot this bridge
// constructs … the one place where the shared model still smells of the CCU".
// That was measured here by mutation — of ten source perturbations tried
// against the equality tests, this is the only one they did not notice, and
// they did not notice it because there was nothing to notice.
//
// Stating it matters for two reasons. An unexplained blind spot in a proof is
// indistinguishable from a hole in it, and the next person to reach for
// topic.Default — for which Bucket is not inert — needs to find out here
// rather than from a fleet of relocated state topics.
func TestSlotBucketIsInertForThisBridge(t *testing.T) {
	layout := harender.Renderer{Root: "zendure2mqtt", Lang: "en"}.Layout()
	dev := goldenUnit()

	for _, p := range resolvePoints(t, dev, goldenReport()) {
		base := harender.Slot(dev.SN, p)
		if base.Bucket != hamodel.BucketUnset {
			t.Fatalf("%s: harender.Slot set a bucket (%v); the group belongs in Path", p.Topic, base.Bucket)
		}
		wantState, wantCmd := layout.State(base), layout.Command(base)
		for _, b := range []hamodel.Bucket{
			hamodel.BucketValues, hamodel.BucketMaster,
			hamodel.BucketCalculated, hamodel.BucketCustom,
		} {
			moved := base
			moved.Bucket = b
			if got := layout.State(moved); got != wantState {
				t.Errorf("%s: bucket %v moved the state topic to %s (was %s) — Bucket is no longer inert and the equality proof has a blind spot that now matters",
					p.Topic, b, got, wantState)
			}
			if got := layout.Command(moved); got != wantCmd {
				t.Errorf("%s: bucket %v moved the command topic to %s (was %s)", p.Topic, b, got, wantCmd)
			}
		}
	}
}

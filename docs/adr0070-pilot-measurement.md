# ADR 0070 phase 5 — pilot measurement for go-zendure2mqtt

- Status: measurement, not a decision
- Date: 2026-09-12
- Subject: [ADR 0070](https://github.com/SukramJ/openccu-loom/blob/main/docs/adr/0070-shared-ha-discovery-model-module.md)
  and its rollout table, row *"5 | `go-zendure2mqtt` (375 LOC) as pilot |
  Smallest surface, catalog-driven, single device hierarchy"*
- Measured against: this repository at `origin/main`
  (`d7b9d4a`), `github.com/SukramJ/go-hamqtt` v0.26.0,
  `github.com/SukramJ/go-mqtt` v1.4.0, `github.com/SukramJ/go-ha-catalog`
  v0.2.1

This document measures what phase 5 costs, before any code moves. It is the
counterpart of `notes/adr0070-moveup-inventory.md` in openccu-loom: that one
measured the packages moving *up*, this one measures the consumer moving
*onto* them. No Go file in any repository was modified. Defects found while
reading are recorded in [Findings](#findings) and were not fixed.

Every count below is measured, and the command is stated where the number
could be got two ways. Two figures in circulation turn out to be right about
the wrong tree, and one is off by three lines; both are explained rather than
quietly corrected.

> **A note on which tree.** The local working copy at the time of measuring sat
> on branch `chore/exclude-go-ha-catalog-from-automerge` (`6187f94`), which is
> three commits *behind* `origin/main` and still publishes an `object_id` key
> that `origin/main` dropped in
> [#36](https://github.com/SukramJ/go-zendure2mqtt/pull/36) (`70611fa`). Every
> measurement here is taken from `origin/main`. The first pass of the payload
> capture was taken from the stale branch and had to be discarded — which is
> the reason this paragraph exists.

---

## 1. What is actually there

### 1.1 The whole repository

Measured with `find . -name '*.go' [-not] -name '*_test.go' | xargs cat | wc -l`
over `git archive origin/main`, test and non-test separately:

| | Lines |
| --- | ---: |
| Go, non-test | **3 763** |
| Go, test | **976** |
| `zendure.yaml` (the catalog) | 292 |
| `docs/konzept.md` | 198 |

Per package:

| Package | Non-test | Test |
| --- | ---: | ---: |
| `cmd/zendure2mqtt` | 223 | 95 |
| `cmd/zendure2mqtt-util` | 243 | 0 |
| `internal/catalog` | 243 | 39 |
| `internal/config` | 496 | 108 |
| `internal/coordinator` | 476 | 62 |
| `internal/discovery` (mDNS, not HA) | 283 | 119 |
| **`internal/hass`** | **372** | **182** |
| `internal/process` | 224 | 173 |
| `internal/source` | 74 | 0 |
| `internal/state` | 88 | 0 |
| `internal/version` | 28 | 0 |
| `internal/virtual` | 76 | 66 |
| `internal/web` | 189 | 84 |
| `internal/zendure/cloud` | 486 | 48 |
| `internal/zendure/local` | 205 | 0 |
| `internal/zendure/model` | 57 | 0 |

Note that `internal/discovery` is **mDNS service discovery**, not Home
Assistant discovery. It is 283 lines that have nothing to do with this ADR,
and the name collides with `go-hamqtt/discovery`. A naive `grep -r discovery`
over this repo is misleading in both directions.

### 1.2 The 375 LOC figure

The rollout table's **375 is exactly `internal/hass`, non-test, and it is now
372.** The three-line difference is #36 dropping the `object_id` key
(`internal/hass/discovery.go` went 306 → 303; `cleanup.go` is unchanged at
69). The ADR's own context table, which carries the same 375, was taken before
that commit. So the figure is right about what it covers and three lines stale.

What it does **not** cover — and this is the part the pilot has to pay for:

| Also in scope for the migration | File | Lines |
| --- | --- | ---: |
| Discovery payload assembly, `unique_id`/entity-id policy, slugify, device block | `internal/hass/discovery.go` | 303 |
| Ownership test + orphan selection | `internal/hass/cleanup.go` | 69 |
| The topic schema itself | `internal/process/topic.go` | 31 |
| Publish loop, orphan reconcile, birth/LWT, command subscribe + routing | `internal/coordinator/coordinator.go` (see below) | ~198 |
| MQTT bootstrap, will, breaker, lifecycle, `mqttSession` | `cmd/zendure2mqtt/main.go:84-141, 169-181` | ~71 |
| **Total addressable surface** | | **~672** |
| Its tests | `internal/hass/cleanup_test.go` + the topic cases in `process_test.go` | 182 + ~20 |

The coordinator figure is the sum of the functions whose job `go-hamqtt`'s
`publisher` package would take over, measured by function extent:

| Function | Lines | Replaced by |
| --- | ---: | --- |
| `Run` (subscribe + birth) | 18 | `CommandRouter.Start`, `Runtime.AnnounceOnline` |
| `retrySubscribe` | 15 | `CommandRouter.Start`/`Resubscribe` |
| `PublishOnline` / `PublishOffline` | 6 + 6 | `Runtime.AnnounceOnline`/`AnnounceOffline` |
| `publish` (discovery + state fan-out) | 22 | `Runtime.PublishBundle`, `StatePublisher.PublishComponentValue` |
| `reconcileOrphans` | 69 | `Runtime.Sweep` |
| `clearOrphanConfigs` | 12 | `Runtime.Retract` |
| `discoverySignature` | 8 | `Runtime.Publish`'s byte-dedup |
| `handleSet` (topic parsing half) | 42 | `CommandRouter` + `Command.Wildcards` |
| **subtotal** | **198** | |

The other 139 lines of `coordinator.go`'s functions (`New`, `onReading`,
`reReadSoon`, `switchPoints`, `handleSwitchSet`, `isOn`, `decodeCommand`,
`formatValue`) are Zendure domain and stay.

### 1.3 Does it still carry the pre-extraction `internal/mqtt`? No.

It did, and the removal is in the history: commit `8fb1943`
*"refactor(mqtt): adopt shared go-mqtt module (0.2.0) (#16)"* deletes
`internal/mqtt/{adapter_tcp,client,lifecycle,test_mock_broker}.go` and
`internal/mqtt/protocol/`. Verified with
`git log --diff-filter=D --name-only -- 'internal/mqtt/*'`.

Today it consumes the shared transport, one minor behind:

```
// go.mod (origin/main)
require github.com/SukramJ/go-mqtt v1.3.0    // latest tag: v1.4.0
require golang.org/x/sync v0.23.0
require gopkg.in/yaml.v3 v3.0.1
```

Two consequences worth naming before the pilot starts:

- **`go-hamqtt` v0.26.0 requires `go-mqtt` v1.4.0.** Adopting the library
  forces the transport bump as part of the same change. That is not a
  problem — v1.4.0 is additive — but it means the pilot is not a
  single-dependency step.
- **v1.4.0 is where ADR 0070's phase-1 deliverable landed**, and this repo
  hand-rolls it. `mqtt.SplitClient(p Publisher, s Subscriber) Client`
  (`go-mqtt/client.go:193`) carries a doc example that is character-for-
  character this repo's bootstrap:
  `session := mqtt.SplitClient(breaker, client)`. Here it is the
  `mqttSession` struct at `cmd/zendure2mqtt/main.go:174-181`. Nine lines
  delete themselves on the bump, independently of anything else in this
  document.

There is **no** `go-hamqtt` and no `go-ha-catalog` in `go.mod`. The commit
`6187f94` *"chore(ci): exclude go-ha-catalog from Dependabot auto-merge"*
prepares the CI side for a dependency the module does not yet have.

---

## 2. How it builds discovery payloads

**Hand-built `map[string]any`, one retained config per entity**, assembled in
`Discovery.config` (`internal/hass/discovery.go:116-176`) and marshalled with
`encoding/json`. No struct, no template, no validation. The ADR's payload-style
column is correct.

### 2.1 The shape

| Concern | Code | Value |
| --- | --- | --- |
| Config topic | `discovery.go:180-182` | `<base>/<platform>/<uniqueID>/config` — **four** segments, no `node_id` level |
| Config filter for the sweep | `cleanup.go:17` | `<base>/+/+/config` |
| `unique_id` | `discovery.go:108-112` | `<root>_<sn>_<topic>`, or `<root>_<sn>_pack_<packSN>_<topic>` |
| `default_entity_id` | `discovery.go:129` | `<platform>.` + `collapseTokens(slugify(deviceName + "_" + topic))` |
| `object_id` | — | **not published** since #36 (`0 of 32` MQTT platforms declare it) |
| `node_id` | — | **no such concept.** The serial lives inside the object-id level |
| Device `identifiers` | `discovery.go:189-192` | `[<root>_<sn>]` |
| Pack `identifiers` | `discovery.go:207` | `[<root>_<sn>_pack_<packSN>]`, with `via_device: <root>_<sn>` |
| Availability | `discovery.go:133-135` | flat `availability_topic` = `<root>/bridge/status` + `payload_available`/`payload_not_available` |
| `origin` | — | **absent.** As the ADR predicted for all six consumers |
| Idempotency | `discovery.go:66-84` | a `map[string]bool` keyed on `unique_id`, one publish per process lifetime |

`<root>` is `config.MQTTTopic`, default `zendure2mqtt`
(`internal/config/config.go:22-23`), passed in at
`cmd/zendure2mqtt/main.go:121`, and exposed to operators as a free-text
add-on option (`addon/config.yaml:58`, schema `str` at `:80`). That is
[F2](#f2).

The slug is the four-times-duplicated one the ADR describes, umlaut
transliteration included (`discovery.go:269-303`): `ä→a ö→o ü→u ß→ss`, fold
everything non-`[a-z0-9]` to a single `_`, then drop adjacent duplicate
tokens. Note it folds `ü` to `u`, where `go-hamqtt`'s `topic.Slug` expands it
to `ue` — see [§5.3](#53-slug-and-naming).

### 2.2 Platforms and entity count

The catalog (`zendure.yaml`) carries **27 entries**, every one of them with a
platform; the loader accepts `sensor | binary_sensor | number | select |
switch` (`internal/catalog/load.go:54`). Measured from the file:

| | Entries |
| --- | ---: |
| Main-unit properties | 20 (10 `now`, 7 `config`, 3 `static`) |
| `packData[]`-only properties | 7 (no `group:`; forced to `battery` at `internal/process/process.go:86-92`) |
| Writable | 7 (5 `number`, 2 `select`), all main-unit |

Plus two synthetic switches minted in code, not the catalog
(`internal/virtual/virtual.go:48-63`, wired at
`internal/coordinator/coordinator.go:366-382`).

Ran through the real code — `catalog.LoadFile("zendure.yaml")` →
`process.Resolve` → `hass.Discovery.Publish` with a capturing publisher, in a
throwaway copy of `origin/main`, with the 20 main properties present and one
`packData` entry — the measured result for **one SolarFlow 2400 AC with one
battery pack is 29 entities across 2 HA devices**:

| Platform | Count |
| --- | ---: |
| `sensor` | 20 (13 unit + 7 pack) |
| `number` | 5 |
| `select` | 2 |
| `switch` | 2 (virtual) |
| `binary_sensor` | 0 — accepted by the loader, used by nothing ([F7](#f7)) |

Each additional pack adds 7 entities and one sub-device. A two-pack unit is 36
entities, 3 devices, 36 retained config topics.

### 2.3 One measured payload, verbatim

`homeassistant/sensor/zendure2mqtt_SF2400AC0012345_electric_level/config`,
captured from `origin/main` (serial and pack serial are plausible
stand-ins, the rest is real output):

```json
{
  "availability_topic": "zendure2mqtt/bridge/status",
  "default_entity_id": "sensor.zendure_sf2400ac0012345_electric_level",
  "device": {
    "configuration_url": "http://192.168.1.50",
    "identifiers": ["zendure2mqtt_SF2400AC0012345"],
    "manufacturer": "Zendure",
    "model": "SolarFlow 2400 AC",
    "model_id": "solarFlow2400AC",
    "name": "Zendure SF2400AC0012345",
    "serial_number": "SF2400AC0012345"
  },
  "device_class": "battery",
  "name": "Battery level",
  "payload_available": "online",
  "payload_not_available": "offline",
  "state_class": "measurement",
  "state_topic": "zendure2mqtt/SF2400AC0012345/now/electric_level/state",
  "unique_id": "zendure2mqtt_SF2400AC0012345_electric_level",
  "unit_of_measurement": "%"
}
```

Two things in there are worth stopping on, because both are load-bearing for
§3:

- **`unique_id` carries the serial in its original case; `default_entity_id`
  carries it lower-cased, and its first token is the word `zendure`, not the
  root `zendure2mqtt`.** The two identity strings live in different
  namespaces, by accident of the seed being built from the *device name*
  ("Zendure SF2400AC0012345") rather than from the root.
- **The device block is richer than loom's needs**: `model_id`,
  `serial_number`, `configuration_url` on the unit; `via_device` +
  `serial_number` + `sw_version` on the pack. All of it is expressible in
  `go-hamqtt` ([§5.1](#51-what-maps-cleanly)).

The pack sub-device, same capture:

```json
"device": {
  "identifiers": ["zendure2mqtt_SF2400AC0012345_pack_AO4H2301X01"],
  "manufacturer": "Zendure",
  "model": "Battery Pack",
  "name": "Zendure SF2400AC0012345 Pack AO4H2301X01",
  "serial_number": "AO4H2301X01",
  "via_device": "zendure2mqtt_SF2400AC0012345"
}
```

### 2.4 The topic schema

`internal/process/topic.go:12-30`, and it is 31 lines:

```
zendure2mqtt/<sn>/<group>/<leaf>/state            group ∈ now|config|static|misc
zendure2mqtt/<sn>/battery/<packSN>/<leaf>/state   pack values — six levels
zendure2mqtt/<sn>/<group>/<leaf>/set              command
zendure2mqtt/bridge/status                        online|offline, retained, = the LWT
```

The command subscription is a single filter, `<root>/+/+/+/set`
(`coordinator.go:87`), and `handleSet` rejects any topic that does not split
into exactly five parts (`coordinator.go:323`). The pack command topic has
six. That is [F3](#f3).

---

## 3. What adoption would move on the wire

This is the decisive section. ADR 0070 sanctions a clean break for the bridges
and withdraws it only for openccu-loom, so nothing here is forbidden — but it
has to be *known*. The short answer is better than the ADR assumes:

> **Every identity string this bridge publishes can be preserved byte-exactly.
> The one thing that cannot stay is the discovery *topic*, and Home
> Assistant's registry survives that — measured, in ADR 0070's second
> amendment — provided the retraction happens first.**

### 3.1 Before / after, per string

"After (default)" is what the library produces if the consumer takes
`discovery.StdContext` and `discovery.NodeID`/`ObjectID`/`UniqueID` as they
come. "After (preserving)" is what it produces with the overrides named in the
last column. Library references are `go-hamqtt` v0.26.0.

| String | Before (measured) | After (library default) | After (preserving) | How |
| --- | --- | --- | --- | --- |
| Discovery topic | `homeassistant/sensor/zendure2mqtt_SF2400AC0012345_electric_level/config` ×29 | `homeassistant/device/zendure2mqtt_sf2400ac0012345/config` ×1 per device | **cannot be preserved** | device-bundle only, `discovery/bundle.go:5-12` |
| `unique_id` | `zendure2mqtt_SF2400AC0012345_electric_level` | `zendure2mqtt_sf2400ac0012345_electric_level` — **case-folded** | identical to before | override `Context.UniqueID` (`discovery/render.go:30-45`); the field is a plain string, `discovery/bundle.go:80` |
| device `identifiers` | `["zendure2mqtt_SF2400AC0012345"]` | `["zendure2mqtt_sf2400ac0012345"]` if built from a slug | identical to before | `model.Identifier{Namespace: "", Value: "zendure2mqtt_SF2400AC0012345"}` — the empty namespace is the documented escape hatch for a published fleet (`model/device.go:41-58`) |
| pack `identifiers` | `["…_pack_AO4H2301X01"]` | as above | identical | same, one `model.Device` per pack with `Via` set |
| `via_device` | `zendure2mqtt_SF2400AC0012345` | `dev.Via.UID()` | identical | `model.Device.Via` (`model/device.go:183`), rendered at `discovery/render.go:415-417` |
| `default_entity_id` | `sensor.zendure_sf2400ac0012345_electric_level` | `sensor.zendure2mqtt_sf2400ac0012345_electric_level` — different first token | identical to before | override `Context.ObjectID`; returning `""` suppresses the key entirely (`discovery/render.go:46-51`) |
| `object_id` | absent | absent | absent | `discovery/bundle.go:81-85` — no such field, deliberately |
| `state_topic` | `zendure2mqtt/SF2400AC0012345/now/electric_level/state` | `topic.Default` renders `<root>/<uid>/<bucket>/<path>` — **different** | identical to before | own `topic.Layout` (4 methods, `topic/topic.go:26-50`) |
| `command_topic` | `…/config/input_limit/set` | as above | identical | same `Layout` |
| availability | flat `availability_topic` + two payload keys | an `availability` **list** of `AvailabilityEntry` | flat keys are still typed on `Component` (`discovery/bundle.go:115-118`) — preservable via a `Builder` | `discovery.Builder` (`discovery/render.go:137-139`) |
| `origin` | absent | `{"name": …, "sw_version": …}` | **cannot be omitted** — `Origin.Name` is required on a bundle (`discovery/validate.go:120-123`) | additive, HA-safe |
| `platform` per component | n/a | `"platform": "sensor"` inside `components` | n/a | the bundle discriminator, `discovery/bundle.go:78` |
| `state_class`, `device_class`, `unit_of_measurement` | flat keys | typed on `Component` | identical | `model.Description` (`model/description.go:157-343`) |
| `min`/`max`/`step` | flat, `number` only | typed, gated to `number` (`discovery/render.go:276-284`) | identical | `Description.Min/Max/Step` |
| `options` | flat, `select` only | typed `Component.Options` | identical | `Description.Options *model.Enum` |
| `payload_on`/`payload_off` | flat, `switch` only | **not on `Description`** — only via `discovery.SwitchFields` (`discovery/gen_fields.go:410-415`) | identical | set `Component.Fields = SwitchFields{…}` |
| State payload | bare scalar, e.g. `55` | `StatePublisher.Publish` takes raw bytes | identical | no envelope needed |
| Every publish's QoS | **0** (`discovery.go:78`, `coordinator.go:88,128,138,171,269`) | **1 — not expressible as 0** | **cannot be preserved** | the zero value is coerced: `publisher.go:209-210`, `state.go:260-261`, `availability.go:138-139`, `command.go:360-361` |

### 3.2 The three unavoidable changes

1. **29 per-entity configs become 1 device bundle per device.** Registry-safe:
   ADR 0070's second amendment measured it on HA 2026.9 with 958 MQTT entities
   — the entity keeps its `unique_id`, its custom name, its icon, its renamed
   `entity_id` and its `device_id`. The *order* is mandatory and fails
   silently in one direction: retract the per-entity config first, publish the
   bundle second. `Runtime.PublishBundle` implements exactly that
   (`publisher/publisher.go:377-386`: the dedup check runs before the
   retraction, and a failed retraction aborts the publish).

2. **The `origin` block appears.** New key, no identity impact, and one of the
   five backlog items the ADR counted.

3. **Every publish and subscribe moves from QoS 0 to QoS 1.** There is no
   opt-out: all four runtime types coerce a zero `QoS` to 1, and
   `StateConfig.QoS`'s doc comment (`publisher/state.go:117-130`) argues
   against the QoS-0 default explicitly. For this bridge that interacts with
   something it deliberately built: the output `mqtt.Breaker`
   (`cmd/zendure2mqtt/main.go:105-111`) exists because a degraded broker makes
   publishes stall on the ack timeout — and QoS 1 is what gives them an ack to
   stall on. Not a payload change, but a wire-behaviour change big enough to
   belong in the migration note.

### 3.3 What blocks the rest of the "after" column, and what settles it

Three things in §3.1 are derived from reading the library, not from running
it. Each needs one concrete act to settle:

- **The rendered bundle JSON.** I can name every key and its source, but not
  the exact byte sequence, because no consumer exists yet.
  *Settled by:* writing the `Layout` + one `model.Device` + one `model.Basic`
  entity and dumping `discovery.Render(...)`, then `cmd/hacheck` over the
  output. That is a half-day and it is the first migration step in §7.
- **Whether `Validate` accepts a bundle whose components carry the flat
  `availability_topic` / `payload_available` / `payload_not_available`
  instead of the `availability` list.** The keys are typed on `Component`, and
  the unknown-key check runs against the per-platform catalog schema
  (`discovery/validate.go:270-275`); `availability_topic` is one of the keys
  the library places on `Component` precisely because 28-30 of 32 platforms
  accept it. For `sensor`/`number`/`select`/`switch` that is almost certainly
  yes — but "almost certainly" is not a measurement.
  *Settled by:* `cmd/hacheck` on the rendered payload.
- **Whether Home Assistant's conflict refusal fires for *this* bridge's
  legacy topic shape.** The amendment's measurement used the five-segment
  per-entity form `<prefix>/<platform>/<node_id>/<object_id>/config`. This
  repo publishes the four-segment form, with no `node_id` level. HA keys the
  conflict on the entity, not on the topic's arity, so the refusal should
  fire identically — but it must be seen, because the failure mode is one
  `WARNING` line and a retained bundle that does nothing.
  *Settled by:* one throwaway device against a live HA 2026.9, watching the
  log, exactly as the amendment was taken.

And one thing is **already settled and is a trap**:
`publisher.SupersededTopics(prefix, b)` (`publisher/publisher.go:568-583`) —
the list `PublishBundle` retracts before it publishes — derives its topics
from `EntityConfigTopic`, which renders
`<prefix>/<platform>/<nodeID>/<objectID>/config`
(`publisher/topic.go:41-44`). **That is five segments and this repo's legacy
configs are four.** So `PublishBundle` will not retract them. The pilot must
retract its 29 legacy topics itself, via `Runtime.Retract` or a `Sweep` whose
`Owns` recognises the old shape. This is the single highest-risk step in the
whole migration and the one most likely to be missed, because everything
*looks* right: the bundle publishes, the log is clean, and the entities keep
their old configs.

---

## 4. Is there anything to pin it against?

**No golden files, no captured payload fixture, no `testdata/` directory
anywhere in the repository.** Verified: `ls internal/*/testdata` → no such
path; `grep -rn golden --include='*.go' .` → zero hits.

What exists that would notice a changed payload, in full:

| Test | File | What it actually pins |
| --- | --- | --- |
| `TestDeviceNameSeedsEntityID` | `internal/hass/cleanup_test.go:95-144` | for **one** sensor: the config topic string, `unique_id`, `default_entity_id`, `device.name` — two cases (default name, configured name) |
| `TestPublishReturnsTopicSet` | `cleanup_test.go:146-182` | one config topic; that entry-less and platform-less points are skipped; that a second `Publish` does not re-send |
| `TestConfigFilter` | `cleanup_test.go:42-46` | `homeassistant/+/+/config` |
| `TestIsOwnConfig` / `TestOrphanConfigs` | `cleanup_test.go:48-90` | ownership and device scoping — over **hand-written payload literals**, not over anything `config()` produced |
| `TestStateAndCommandTopics` | `internal/process/process_test.go:126-137` | three topic strings from the *builder*, including the six-level pack form |

That is the whole guard. Counted against the 29-entity payload of §2.3, the
following are pinned by **nothing**: `state_topic` and `command_topic` *as
they appear in a config* (only the builder is pinned, and the discovery code
calls it from a second site, `discovery.go:130` and `145`),
`availability_topic`, `payload_available`, `payload_not_available`,
`device.identifiers`, `via_device`, `model_id`, `serial_number`,
`configuration_url`, `sw_version`, `device_class`, `state_class`,
`unit_of_measurement`, `min`/`max`/`step`, `options`, `payload_on`/
`payload_off`, and every one of the 28 other entities.

Note also that the tests construct `Discovery` with root `"zendure"`
(`cleanup_test.go:39`), which no deployment uses — the default is
`zendure2mqtt`. So the one pinned `unique_id` literal
(`"zendure_HOA1_electric_level"`, `cleanup_test.go:140`) does not have the
shape of any string in the field. It pins the *formula*, not the output.

### 4.1 What pinning it would take

loom's twelve planes were byte-pinned before they moved, and the inventory
records that of the eight measured slug divergences, **zero** appeared in any
fixture — the pins that existed shipped green through a change that would have
orphaned entities, and the gap was only visible because someone counted. On
that evidence the pins are not a nicety here; they are the precondition.

Concretely, and this is small because the surface is small:

1. **One golden file per HA device shape: unit and pack.**
   `internal/hass/testdata/discovery_unit.json`,
   `discovery_pack.json` — the full 29-entity map, topic → payload, sorted,
   `json.MarshalIndent`-formatted. Produced the way §2.3 was produced:
   `catalog.LoadFile("../../zendure.yaml")` → `process.Resolve` →
   `Discovery.Publish` with a capturing publisher. That is one test of maybe
   60 lines and it pins **every** key above at once, including the ones no
   human would think to list.
2. **The real catalog, not a test catalog.** `process_test.go` uses a 27-line
   inline `testCatalog` (`process_test.go:15-41`). The golden must load
   `zendure.yaml`, or a catalog edit — the most likely source of an
   accidental payload change, since the catalog is the declared extension
   point — is invisible to it.
3. **Rows for the inputs that diverge, chosen from the diff and not from
   intuition.** For the `slugify` → `topic.Slug` question ([§5.3](#53-slug-and-naming))
   that means at minimum: a `DeviceName` containing `ü` (today `u`, library
   `ue`), one containing a non-German accent (`é`: today dropped, library
   `e`), one containing a hyphen (today folded to `_`, library **preserved**),
   and a pack serial containing `-` next to one containing `_` ([F8](#f8)).
   None of these four exists in any current test.
4. **A state-topic golden.** The 29 state topics of §2.4, as a sorted list.
   Cheap, and it is the half of the wire that HA's registry does *not*
   protect: a changed `state_topic` leaves the entity in place and makes it
   permanently unknown.
5. **A `cmd/zendure2mqtt-util` sub-command that dumps the same thing.** The
   util already has `resolve` (`cmd/zendure2mqtt-util/main.go:37-46`), which
   is two thirds of the way there. A `discovery-dump` would let an operator
   produce a before/after diff on their own hardware, which is what the
   migration note needs users to be able to do.

Steps 1, 2 and 4 are pure test additions with no production change, and they
must land **before** the first line of the migration.

---

## 5. What the library still does not cover for this consumer

`go-hamqtt` v0.26.0 is 10 695 non-test lines across 11 packages, with
13 223 test lines. It is not short of vocabulary. What follows is what this
bridge specifically has to bring or work around.

### 5.1 What maps cleanly

Worth saying first, because it is most of it: the device block maps
completely — `ModelID`, `SerialNumber`, `ConfigURL`, `Manufacturer`, `Model`,
`SWVersion`, `HWVersion`, `SuggestedArea`, `Connections`
(`model/device.go:176-184`, `discovery/bundle.go:43-57`). Sub-devices are
ordinary `model.Device`s with `Via` set and are not special-cased
(`model/device.go:164-169`), which is exactly this bridge's pack hierarchy.
`select` options are typed with a reverse lookup that matches the code *and*
every language's label (`model.Enum.Code`, `model/device.go:272-303`) — which
is `catalog.Entry.CodeForLabel` (`internal/catalog/catalog.go:106-118`)
generalised, localisation included. `number` bounds are typed and gated to the
platforms that accept them.

### 5.2 `topic.Layout` — the interface is right, and `Bucket` does not fit

The interface is four methods (`topic/topic.go:26-50`):
`State(model.Slot)`, `Command(model.Slot)`, `Availability(model.Slot)`,
`Bridge()`. There is exactly **one** production implementation in the library,
`topic.Default` (`topic/topic.go:61-66`), plus two test stubs. loom implements
its own over its own topic builder, and this bridge would have to do the same —
which the interface exists for and which is the cheapest part of the
migration: `internal/process/topic.go` is 31 lines and becomes a `Layout`
almost verbatim.

The friction is one level down. `model.Slot` carries
`Scope []string`, `Address`, `Channel`, `Bucket`, `Path []string`, and
`Bucket` is a closed enum with five spellings — `BucketUnset`,
`BucketValues`, `BucketMaster`, `BucketCalculated`, `BucketCustom`
(`model/slot.go:13-61`). Those are paramset names. This bridge's topic bucket
is `now | config | static | battery | misc`, five values that mean something
else. They do not map, and they do not need to: `BucketUnset` renders to
nothing, and the group can be carried in `Slot.Path` and rendered by the
bridge's own `Layout`. But it means `topic.Default` is unusable here and the
`Bucket` field is dead weight in every slot this bridge constructs. That is
the one place where the shared model still smells of the CCU.

**The measured trap — `Layout.State` is not the config's `state_topic` —
does not bite this bridge, and it must still be respected.** The gate is
`discovery/render.go:345-352`, and the ten platforms without a `state_topic`
are enumerated at `publisher/state.go:91-105`: climate, water_heater,
lawn_mower, camera, tag, button, device_automation, image, notify, scene. This
bridge publishes `sensor`, `number`, `select`, `switch` — four of the 22 that
do have one. So `layout.State(slot) == comp.StateTopic` holds for every entity
it will ever publish. The rule still applies: publish through
`StatePublisher.PublishComponentValue` / `ComponentStateTopic`
(`publisher/state.go:392-398`), never through `Layout.State`, because the day
a `binary_sensor` or a `button` is added ([F7](#f7)) the two answers diverge
and the failure is a state topic no entity reads. The library's own test
pins exactly this asymmetry
(`publisher/state_test.go:912-1006`,
`TestComponentStateTopicIsTheOnlyProvablyEqualRoute`).

Worth noting that this bridge *already* has the two-sites shape the rule
guards against: `process.StateTopic` is called once from the discovery payload
(`discovery.go:130`) and once from the publish loop
(`coordinator.go:170`). Same function, so no drift today — but it is two call
sites, and the library's README records two reference bridges that ended up
inconsistent that way.

### 5.3 Slug and naming

| | this bridge (`discovery.go:269-303`) | `go-hamqtt` (`topic/topic.go:146-197`) |
| --- | --- | --- |
| `ü` | `u` | `ue` |
| `ö`, `ä`, `ß` | `o`, `a`, `ss` | `oe`, `ae`, `ss` |
| `é`, `ñ`, `ø` | dropped (fold to `_`) | `e`, `n`, `oe` |
| `-` | folded to `_` | **preserved** (`topic/topic.go:174-179`) |
| empty result | `""` | `"x"` (`topic/topic.go:163-165`) |
| adjacent duplicate tokens | collapsed (`collapseTokens`) | **not** collapsed |

Six divergence classes, and the current test suite exercises **none** of them
— every name in every test is ASCII with no hyphen. The two that matter for
this bridge's real inputs are the hyphen (Zendure pack serials are
`[A-Za-z0-9_-]`, `internal/process/process.go:130-142`) and `ü` (a German
operator naming a device `Balkon Süd`). Both change `default_entity_id`, which
is inert for installed entities and decisive for new ones.

The library's `Slug` is the better function — it is the one whose doc comment
names *"Größe → gr_e"* as the defect a consuming bridge shipped
(`topic/topic.go:142-145`) — but "better" is not "same", and swapping it is a
step with byte risk, taken alone, last, with fixtures ahead of it.

### 5.4 `CommandRouter` — the overlap rule, and what it would fix here

`CommandRouter.Handle` refuses any filter pair that structurally overlaps, not
one that is observed to collide: `filtersOverlap`
(`publisher/command.go:731-746`) walks the two filter's segments and returns
true on any `#`, on a `+` against anything, or on equal literals to the end.
The error tells the consumer to *"register the general shape only and branch
inside the handler"* (`publisher/command.go:436-446`). The rationale is
measured, against Mosquitto 2.1.2 on both protocol versions:
`ccu/+/+/set` and `ccu/+/PRESS_SHORT/set` turned one published message into
two handler runs (`publisher/command.go:60-65`).

For this bridge the rule is free, and the router is a net fix:

- Its one command filter is `<root>/+/+/+/set` (`coordinator.go:87`). Nothing
  to overlap with. The orphan reconcile's `homeassistant/+/+/config`
  (`cleanup.go:17`) is on a different root, and the cloud backend's
  `iot/{productKey}/{deviceId}/#` is on a different client
  (`internal/zendure/cloud/source.go:248`).
- The pack command gap of [F3](#f3) becomes expressible: `<root>/+/+/+/set`
  and `<root>/+/battery/+/+/set` are five and six segments with no `#`, so
  `filtersOverlap` returns false and both register. The library makes the
  currently-unroutable pack write routable, which is the kind of thing a pilot
  is supposed to surface.
- `CheckDisjoint` (`publisher/command.go:716-731`) is the guard this bridge
  has no equivalent of: it wants every topic the consumer publishes — state,
  availability, birth — checked against the command filters at boot, and the
  boot failed on a collision. Checked by hand here: `…/<leaf>/state` never
  matches `…/set`, and `<root>/bridge/status` is three segments against a
  five-segment filter. Clean today, unguarded today.

One real cost: `CommandHandler` runs on a router worker, not the read loop
(`publisher/command.go:143-178`). This bridge already does that by hand —
`handleSet` spawns a goroutine with the comment *"go-mqtt dispatches handlers
synchronously, so a slow/unreachable device would otherwise stall all inbound
dispatch"* (`coordinator.go:348-351`) — so the behaviour is identical and 20
lines of it delete.

### 5.5 `Envelope` has no timestamp, and this bridge does not want one

`publisher.Envelope` is `{Value any; Available bool}`
(`publisher/state.go:44-58`), and the omission is argued rather than
overlooked (`publisher/state.go:33-43`): a timestamp would make every payload
unique and turn the dedup gate — `bytes.Equal` over the **full** payload,
`publisher/state.go:344-348` — into a no-op.

This bridge publishes a bare scalar (`formatValue`, `coordinator.go:462-476`).
No timestamp, no envelope, so it keeps the dedup gate — and the gate is the
single biggest measurable win in the whole migration. Today `coordinator.publish`
re-publishes **every** point retained on **every** poll
(`coordinator.go:169-174`), default interval 15 s
(`internal/zendure/local/source.go:41-43`): 29 retained publishes per device
every 15 s, ~7 000 per hour, nearly all of them byte-identical to the value
already on the broker. Under `StatePublisher` that collapses to the changes
only. A `packNum` or `chargeMaxLimit` sensor would publish once per process.

If the bridge ever wants a timestamp on the wire it opts out of the gate by
marshalling its own struct and calling `Publish` with the bytes — the library
says so explicitly. It should not.

### 5.6 What the consumer must still write itself

| Must supply | Where | Size |
| --- | --- | --- |
| `topic.Layout` (4 methods) | `topic/topic.go:26-50` | ~40 lines, from the existing 31 |
| `publisher.Transport` | satisfied by `gomqtt.Transport(client)` | 1 line |
| `model.Entity` per point | embed `*model.Basic` (`model/entity.go:45-63`) | an adapter over `process.Point` |
| The `Owns` sweep predicate | `publisher/sweep.go:28-60`, `ErrSweepUnscoped` if nil | this is `IsOwnConfig`, ~15 lines |
| `switch` `payload_on`/`payload_off` | `discovery.SwitchFields` | 4 lines |
| `Context.UniqueID` / `ObjectID` overrides | if identity is preserved (§3.1) | ~10 lines |
| A `Builder` if the flat availability keys are kept | `discovery/render.go:137-139` | ~10 lines |

Nothing on that list is hard, and nothing on it is missing from the library.

---

## 6. What this bridge does better, or differently, than loom

loom is the tiebreaker on design conflicts. It is not automatically right, and
the library's own artefacts record defects in the other bridges without naming
them. Three of those were checked against **this** bridge:

**The inert daemon LWT — not present here, and this bridge is the
counter-example.** `publisher/publisher.go:10-14` and
`CHANGELOG.md:450-453` record *"two reference bridges configure a will no
published entity references, so a hard crash writes 'offline' where nothing
reads it and every entity stays available forever."* Here the will is
`mqtt.Will{Topic: cfg.MQTTTopic + "/bridge/status", Payload: "offline",
Retain: true}` (`cmd/zendure2mqtt/main.go:84-91`) and **every** entity's
`availability_topic` is the same string, built from the same root
(`discovery.go:133`). Confirmed identical by construction — both derive from
`config.MQTTTopic`. It also publishes `offline` explicitly on a graceful stop
(`coordinator.go:136-141`, called at `main.go:163-165`), because the LWT only
fires on an ungraceful one. That is more correct than the two bridges the
library measured, and the `Config.StatusTopic`/`Layout.Bridge()`
cross-check that panics on disagreement (`publisher/publisher.go:215-229`)
would pass here on the first try.

**The availability topic inside HA's birth tree — not present here.** The
other bridge's `<hass_base>/status/lwt` is *"both wrong and inert"*. This one
is under its own root, correctly outside `HASS_BASE_TOPIC`.

**Non-retained state — not present here.** The mtec defect (*"after a Home
Assistant restart every entity sits at `unknown` until the next poll"`) does
not apply: every state publish is retained (`coordinator.go:171`, the `true`
argument), as is discovery (`discovery.go:78`) and the bridge status
(`coordinator.go:128,138`).

**The umlaut slug — not present here.** `slugify` transliterates
(`discovery.go:269-270`), so `Größe` becomes `grosse`, not `gr_e`. The ADR
names mtec as the only one of the four missing this step; this one has it.

**The `object_id` dead key — already fixed here, ahead of the library's own
tooling.** `cmd/hacheck`'s CHANGELOG entry (`CHANGELOG.md:833-836`) reports
finding `object_id` in *"the retained configs of two shipped bridges"*, and
`cmd/hacheck/main_test.go:25` still carries a fixture with both keys. This
repo dropped it in #36 with its own measurement — *"object_id is accepted by
0 of 32 MQTT platforms; default_entity_id by 28"* — which is the same
catalog-derived number the library reasons from. The two lines of work
converged independently, and this repo got there first.

### Three things the library should learn from this bridge rather than overwrite

1. **`configuration_url` from the device's own address.**
   `discovery.go:201-202` sets `"configuration_url": "http://" + dev.Address`
   so the HA device page links to the unit's local web UI. The field is typed
   in the library (`model/device.go:181`) but nothing derives it; it is a
   pattern worth documenting for every LAN-local consumer.
2. **A localised label set that is reversible on the command path.**
   `value_map` + `value_map_de` + `CodeForLabel` (`catalog.go:80-118`) accepts
   a command in **either** language and maps it back to the raw code, because
   HA echoes back whatever string it was handed at discovery time. The library
   has exactly this in `model.Enum.Code` (`model/device.go:272-303`) — so
   this is a convergence, not a gap, and it is evidence the design is right.
3. **The sweep's own re-entry guard.** `Discovery.Forget`
   (`discovery.go:94-105`), called at `coordinator.go:257`, drops just-cleared
   topics from the sent-set so a *wrongly* swept entity — a transiently
   shrunken report — comes back on the next poll instead of staying deleted
   for the process lifetime. `Runtime.Sweep` retracts and returns
   `SweepResult.Retracted` (`publisher/sweep.go:69-74`); the caller is left to
   work out that its dedup cache now disagrees with the broker. Small, real,
   and the kind of thing only a consumer finds.

---

## 7. Verdict and sequencing

### 7.1 Is zendure the right pilot?

**Yes, and for a reason the rollout table does not state.** The table's
reasons — smallest surface, catalog-driven, single device hierarchy — are all
true as measured (372 lines, 27 declarative entries, one `via_device` level).
But the load-bearing property is a different one:

> **This is the only bridge where the clean break the ADR sanctions can be
> declined.** Every identity string is reproducible byte-exactly (§3.1), so
> the pilot can prove the *runtime* — bundle migration, dedup, sweep ordering,
> birth, command routing — on an installed base, with `unique_id` frozen, and
> settle the retract-then-publish ordering against a live Home Assistant at a
> cost of zero orphaned entities. That is a much stronger result than a
> re-keyed pilot, and it is the same discipline the first amendment imposed on
> loom.

It is also the bridge that exercises the two library features nothing has
exercised in anger: the sub-device `Via` hierarchy with per-parent
availability, and a `Layout` whose bucket vocabulary is not the CCU's.

Would another bridge prove more? `go-mtec2mqtt` (591) would fix four recorded
defects as a side effect, which is tempting — but that is precisely why it
should not go first: it cannot distinguish "the library changed the payload"
from "the library fixed the payload", and it has the same absent pins. Prove
the machinery where the answer is *nothing changed*, then go fix mtec. The
table's order is right.

### 7.2 Recommended sequencing

The ordering principle, borrowed from loom's inventory: every step that cannot
change a published byte goes first, so the steps that can arrive alone, on a
clean tree, each with its own note.

**Step 0 — bump `go-mqtt` to v1.4.0 and delete `mqttSession`.** No byte risk.
`mqtt.SplitClient(breaker, client)` replaces `main.go:174-181`. Independent of
everything else, and `go-hamqtt` needs v1.4.0 anyway. *Pins: compilation.*

**Step 1 — pin the current payload.** No production change. The two golden
files and the state-topic list of §4.1, loaded from the real `zendure.yaml`,
plus the four divergence rows (`ü`, `é`, `-`, `_` vs `-` pack serials). This
is the step that makes every later one measurable, and on loom's evidence it
is the step that finds the defects a review does not. *Nothing may be skipped
here for being obvious.*

**Step 2 — write the model adapter and render one bundle, publishing
nothing.** `topic.Layout` over `process.StateTopic`/`CommandTopic`; one
`model.Device` per unit and per pack with the empty-namespace `Identifier`;
`*model.Basic` per `process.Point`; `Context.UniqueID`/`ObjectID` overrides
returning today's strings. Then `discovery.Render` → `discovery.Validate` →
`cmd/hacheck`, and diff the component bodies against the step-1 goldens key by
key. This is where §3.3's three unknowns are settled, on a branch, with
nothing on a broker. *Byte risk: none — nothing publishes.*

**Step 3 — move the state plane onto `StatePublisher`, per-entity configs
unchanged.** The dedup gate is the win and it is registry-neutral: same
topics, same payloads, fewer of them. This is also where the QoS 0 → 1 change
lands, so it wants its own release and a changelog line. *Byte risk: the
topics are pinned by step 1's state-topic golden; the QoS change is
wire-visible and payload-invisible.*

**Step 4 — move birth/LWT and the orphan sweep onto `Runtime`, still
per-entity.** `Will()` applied at CONNECT, `AnnounceOnline` on reconnect,
`WatchBirth` (which this bridge does not have at all — [F6](#f6)),
`Sweep` with `Owns` = today's `IsOwnConfig`. Keep the `Forget` behaviour of
§6.3 by hand. *Byte risk: the sweep can retract a live config if `Owns` is
wrong; step 1's goldens do not cover the sweep, so this step wants a mock-broker
test of its own.*

**Step 5 — the bundle migration. Alone, last, with the migration note.**
Retract all 29 legacy four-segment configs explicitly — `SupersededTopics`
will not do it (§3.3) — then `PublishBundle`. Verify against a live HA 2026.9
that no conflict warning appears and that a renamed, re-iconed entity survives.
*Byte risk: maximal, and the failure mode is silent. This step is the reason
the four above are separate.*

**Not in the sequence, deliberately:**

- **Re-keying `unique_id` or `default_entity_id`.** The break is sanctioned and
  it is still not worth taking: it buys harmonisation with five bridges that
  will not converge anyway (the ADR's first amendment accepted permanently
  divergent formats), and it costs every installed entity its history, area
  and automations. Freeze both strings with the §3.1 overrides. If they are
  ever taken, they are taken as their own major release, alone, after fixtures
  cover all six slug divergence classes of §5.3.
- **Swapping `slugify` for `topic.Slug`.** Same reason, one level smaller. It
  only changes `default_entity_id`, which is inert for installed entities —
  and *inert for installed entities* is precisely why it would ship green and
  silently split new installs from old ones.
- **Adopting `topic.Default`.** The `Bucket` enum does not fit (§5.2) and the
  topic tree is documented in `docs/konzept.md:66-74` and in every user's
  automations.
- **Moving the cloud backend's MQTT client onto anything.** It is a separate
  client, pinned to `ProtocolV311` against a third-party broker
  (`docs/konzept.md:36-40`), and it is not a Home Assistant surface.
- **Fixing the defects below as part of the migration.** Each is its own
  commit, before or after, never inside a step that also moves a payload.

### 7.3 What the migration note must say

1. **Discovery moves from 29 retained per-entity configs to one device bundle
   per device.** Entities keep their `unique_id`, their `entity_id`, their
   customisations, their history and their device assignment — measured on HA
   2026.9, not assumed. Home Assistant ≥ 2024.11 is required from this
   release on.
2. **Nothing is re-keyed.** `unique_id`, `default_entity_id`, `state_topic`,
   `command_topic`, `availability_topic`, device `identifiers` and
   `via_device` are byte-identical to the previous release. State this
   explicitly; it is the sentence users of the other five bridges will not
   get.
3. **Every publish moves from QoS 0 to QoS 1.** Brokers with tight inflight
   limits or an ACL on QoS will notice; nothing else will.
4. **State is now de-duplicated.** A value that does not change is no longer
   re-published every 15 s. Anything downstream that counted messages rather
   than reading the retained value — a separate MQTT consumer, a Node-RED
   flow triggered on message rather than on change — will see far fewer
   messages. This is the one behaviour change a user could reasonably be
   surprised by.
5. **The first start retracts the old configs before publishing the bundle.**
   If it is interrupted between the two, the entities are briefly *absent*
   rather than unavailable; restarting the bridge restores them.
6. **Rolling back needs the same care in reverse.** Turning bundle mode off is
   not "stop publishing bundles" — the retained device document has to be
   retracted first, or every per-entity config of the next boot is refused
   with nothing but a log line. Ship the rollback instruction, not just the
   upgrade one.
7. **`MQTT_TOPIC` must not be changed.** Not new, but this release is the
   moment to say it: it namespaces every `unique_id`, and changing it orphans
   every entity *and* makes the old configs unsweepable ([F2](#f2)).

---

## Findings

Defects found while reading. None were fixed; no Go file in any repository was
modified. Ordered by consequence, not by discovery.

<a id="f1"></a>
**F1 — a discovery config is published once per process and never updated,
however much it changes.** `Discovery.Publish` guards on
`d.sent[uniqueID]` (`internal/hass/discovery.go:66-84`), a set keyed on the
`unique_id` alone. The guard is payload-blind, so the **first** report of a
process decides the retained config for that process's lifetime. Measured
consequences with the shipped catalog: a first report without `product` leaves
`model_id` out permanently (`discovery.go:198-199`); a pack whose first
appearance lacks `softVersion` never gets `sw_version`
(`discovery.go:214-215`); a cloud device, whose `Address` is empty by
construction (`internal/source/source.go:39-40`), never gets
`configuration_url`, which is correct — but a *local* device learned later
would not get it either. A catalog edit that changes a `name`, `unit`, `min`
or `options` does not reach the broker until a restart. `go-hamqtt`'s
`Runtime.Publish` compares the payload bytes
(`publisher/publisher.go:279-284`) and fixes this by construction, which is
one more reason step 1's goldens must exist before step 5.

<a id="f2"></a>
**F2 — `unique_id` is namespaced with the *configurable* MQTT root, and the
orphan sweep is namespaced with it too.** `Discovery.uniqueID` builds
`<root>_<sn>_<topic>` (`discovery.go:108-112`) where `root` is
`config.MQTTTopic` (`cmd/zendure2mqtt/main.go:121`,
`internal/config/config.go:82`), surfaced as a free-text add-on option
(`addon/config.yaml:58`, schema `str` at `:80`). Changing `MQTT_TOPIC` therefore
re-keys every entity in Home Assistant — and `IsOwnConfig` tests
`strings.HasPrefix(uid, d.root+"_")` (`cleanup.go:28`), so after the change
the *old* retained configs are no longer recognised as this daemon's own and
are never swept. The user is left with a full set of orphaned entities that
the cleanup is now structurally unable to remove. ADR 0070 records the first
half of this; the second half is confirmed here. `go-hamqtt` states the rule
directly — *"the namespace must be a constant of the bridge, never
configurable"*, `discovery/naming.go:42-46`, citing this defect.

<a id="f3"></a>
**F3 — a writable battery-pack property would publish an unroutable
`command_topic`.** `process.CommandTopic` renders six levels for a point with
a `PackSN` (`internal/process/topic.go:19-30`), but the only command
subscription is `<root>/+/+/+/set` (`coordinator.go:87`) and `handleSet`
returns immediately unless the topic splits into exactly five parts
(`coordinator.go:321-325`). Latent today: all 7 writable catalog entries are
main-unit properties (`zendure.yaml`), 0 of the 7 pack-only properties is
writable. It becomes real the day one is marked `writable: true` — and the
symptom is an HA entity that accepts input and silently does nothing.
Nothing tests it.

<a id="f4"></a>
**F4 — there is no per-device availability; an unreachable device keeps its
values forever.** A failed poll is logged and dropped
(`internal/zendure/local/source.go:99-104`) and every entity's only
availability source is the bridge LWT (`discovery.go:133`). So while the
bridge is up and a device is unplugged, all 29 entities stay *available*,
showing the last value they ever saw. This is the class `cmd/hadoctor` reports
as `no-availability` (`go-hamqtt/CHANGELOG.md:796-803`) and the library has the
vocabulary for it: `model.LevelDevice` +
`AvailabilityPublisher.Device` (`publisher/availability.go:289`). Fixing it is
additive — a new `availability` entry per entity — and it is the best
argument for the migration that is not about deduplication.

<a id="f5"></a>
**F5 — a `select` whose device reports an unmapped code publishes a state
Home Assistant will reject.** `applyEntry` returns the raw value when the
value map has no label for the code (`internal/process/process.go:153-158`),
so the published state is a bare number that is not in `options`. `acMode`'s
map covers `1` and `2` only (`zendure.yaml`); a device reporting `0`
(idle) publishes `0` to a select whose options are `charge`/`discharge`. HA
logs an invalid option and the entity goes unknown. `smartMode` has the same
shape. The fix is one line — publish nothing, or publish a mapped `unknown` —
and it is a payload change, so it belongs before step 1 or after step 5, never
inside.

<a id="f6"></a>
**F6 — no Home Assistant birth-message subscription.** All four `Subscribe`
call sites are accounted for (`coordinator.go:88`, `:117`, `:233`,
`internal/zendure/cloud/source.go:248`) and none is `<hass_base>/status`.
Discovery is retained, so an HA restart is survived — but a broker restart
without persistence, or a broker whose retained store is cleared, leaves Home
Assistant with no entities at all until the daemon is restarted, and there is
nothing in the logs to say so. `publisher.WatchBirth`
(`publisher/birth.go:129`) exists for exactly this, with the
single-worker dispatcher that keeps the republish off the read loop.

<a id="f7"></a>
**F7 — `binary_sensor` is an accepted platform that would publish an
uninterpretable entity.** The catalog loader admits it
(`internal/catalog/load.go:54`) and the documentation offers it
(`internal/catalog/catalog.go:29-31`, `zendure.yaml:11`), but
`Discovery.config`'s platform switch (`discovery.go:147-169`) has no
`binary_sensor` case, so no `payload_on`/`payload_off` is emitted while the
state payload is a raw number. 0 of 27 entries use it, so this is a trap for
the next person editing the catalog, not a live defect. It is also the case
that would make §5.2's `Layout.State` warning bite.

<a id="f8"></a>
**F8 — two packs whose serials differ only in a character the slug folds
collide on `default_entity_id` while keeping distinct `unique_id`s.**
`validPackSN` admits both `-` and `_` (`internal/process/process.go:130-142`);
`slugify` folds both to `_` (`discovery.go:274-290`). So packs `AB-12` and
`AB_12` produce the same entity-id seed and two different `unique_id`s: two
entities competing for one `entity_id`, which Home Assistant resolves by
suffixing, silently. Low likelihood, no guard, and — note — the library's
`topic.Slug` **preserves** the hyphen (`topic/topic.go:174-179`) specifically
to avoid this collision class, which is one concrete thing the migration would
fix.

# Unreleased

## What's Changed

### Changed

- **State is no longer re-published when it has not changed.** Every point
  this bridge resolves now goes out through `publisher.StatePublisher` from
  `go-hamqtt`, whose dedup gate compares the payload bytes against what the
  broker last accepted and writes nothing when they are equal. Before this
  release the publish loop wrote all 30 retained state topics on every poll
  — with the default 15 s interval that is roughly 7 000 messages an hour
  per device, nearly all of them byte-identical to the value already
  retained on the broker. A `packNum` or `chargeMaxLimit` sensor now
  publishes once per process.

  **Nothing about the wire moved except the number of messages.** Same
  topics, same payloads, same retain flag and the same QoS 0 — the payload
  is still rendered by this repository's own `formatValue`, not by the
  library's renderer, because the two disagree on a Go bool (`"1"`/`"0"` here
  against `"true"`/`"false"` there) and a state-plane migration is not the
  place to change a payload. The QoS is stated explicitly with
  `publisher.QoSAtMostOnce`: the library's zero value means *unset* and
  resolves to QoS 1, so adopting the runtime naively would have changed the
  delivery guarantee of every installed deployment. That is an inherited
  choice being preserved and not an endorsement — a message lost at QoS 0 is
  lost, and the broker then keeps serving the previous retained value until
  the datapoint next changes.

  **What a downstream consumer may notice:** anything that counted messages
  rather than reading the retained value — a second MQTT subscriber, a
  Node-RED flow triggered on message rather than on change — now sees far
  fewer of them. Home Assistant is unaffected; it reads state, not message
  rates.

  On every (re)connect the gate is reopened (`StatePublisher.Reset`) so a
  broker that came back without its retained store is rewritten by the next
  poll instead of being left blank until each value happens to change.

  The state plane is also now checked against this bridge's own command
  subscription: a state topic that fell inside `<root>/+/+/+/set` would have
  been echoed straight back into the command handler, and the filter is
  stated once (`coordinator.CommandFilter`) and read by both the subscriber
  and the guard. All 30 published state topics are asserted to clear it.

  This is ADR 0070 phase 5, step 3. The four pinned fixtures were not
  regenerated and hold byte-for-byte; a new pin reads the QoS and the retain
  flag off the transport call itself, which no golden file can see.

### Added

- **The shared Home Assistant discovery library now provably reproduces this
  bridge's published payload, byte for byte, for all 29 entities — and
  publishes nothing.** `github.com/SukramJ/go-hamqtt v0.27.0` is a new
  dependency and `internal/harender` is a second, parallel rendering path
  built on it: a `topic.Layout` over this bridge's own topic builder, one
  `model.Device` per unit and per battery pack, a `model.Basic` entity per
  resolved point, and a `discovery.Context` that freezes every identity
  string. New tests in `internal/coordinator` render that model through
  `discovery.RenderComponent` and compare the result against the four golden
  files pinned in the previous release.

  They match exactly. All 29 discovery payloads and all 29 retained config
  topics, plus all 14 payloads of the identity-hazard pin, are identical to
  what `internal/hass` publishes today — `unique_id`, `default_entity_id`,
  device `identifiers`, `via_device`, `state_topic`, `command_topic`, the
  flat `availability_topic` / `payload_available` / `payload_not_available`
  triple, `device_class`, `state_class`, `unit_of_measurement`, `min`/`max`/
  `step`, `options`, `payload_on`/`payload_off`, `model_id`,
  `serial_number`, `configuration_url` and `sw_version`. So are all 30 state
  topics and all nine command topics. `discovery.Validate` accepts the
  rendered device bundles and `discovery.ValidateBody` accepts every payload
  — including the flat availability triple, which was an open question, and
  which no test in this repository had ever checked against Home Assistant's
  schemas at all.

  **Nothing about the daemon changes.** `internal/harender` is imported by
  tests only; no publish path, no MQTT bootstrap and no discovery payload is
  touched, and the pinned fixtures were never regenerated. This is ADR 0070
  phase 5, step 2 — the pilot's decisive experiment, run on a branch with
  nothing on a broker, because the alternative place to discover a mismatch
  is a user's Home Assistant, where a changed `unique_id` silently costs an
  entity its history and a changed `state_topic` silently makes it unknown
  forever.

  Four things the experiment settled that reading the library could not:
  the rendered bytes; that the flat availability keys validate; that
  `publisher.LegacyTopicByUniqueID` — and not `LegacyTopicByObjectID`, and
  not the pre-v0.27.0 five-segment default — is the form that matches this
  fleet's 29 retained config topics, which is what the eventual bundle
  migration has to retract before it publishes; and that `model.Slot`'s
  `Bucket` is provably inert for this bridge, which is now asserted rather
  than assumed.

  Also exported, additively and with no behaviour change: `hass.UniqueID`,
  `hass.EntityObjectID`, `hass.DeviceName`, `hass.PackSoftVersion` and
  `catalog.Entry.Codes`. The parallel path calls the production identity
  functions rather than restating them, so the two paths cannot drift — and
  so that the two pinned *defects* stay reproduced: the pack-serial
  hyphen-vs-underscore `default_entity_id` collision, and the dropped "é".

### Added

- **The published Home Assistant discovery payload is now pinned, byte for
  byte.** `internal/coordinator/testdata/` holds four new golden files: the
  29 retained discovery configs a SolarFlow 2400 AC with one battery pack
  produces (22 on the main unit, 7 on the pack sub-device — 20 sensors,
  5 numbers, 2 selects, 2 virtual switches), the 30 state topics that go with
  them, and the four device-name and pack-serial inputs whose entity-id seeds
  no test had ever exercised. They are captured through the real publish path
  over the shipped `zendure.yaml`, so a catalog edit is visible to them.

  Nothing about the daemon changes — this is a test-only addition. It exists
  because there was nothing here that would have noticed a changed payload:
  no `testdata/` anywhere in the tree, no golden file, and a guard consisting
  of four asserted keys on a single sensor. Home Assistant drops an
  undeclared discovery key silently (`extra=REMOVE_EXTRA`, no error, no log
  line) and keys its entity registry on `unique_id` and its device registry
  on `identifiers`, neither of which has a migration path, so a payload
  regression here is invisible until entities are already orphaned.

  Two pinned rows record defects rather than intended output, and the test's
  doc comment names them: two battery packs whose serials differ only in
  `-` versus `_` collapse onto one `default_entity_id` while keeping distinct
  `unique_id`s, and a device name containing a non-German accent loses the
  character instead of transliterating it. Both are pinned as published — a
  defect that is pinned is one a later change can be seen to fix.

### Changed

- **Five CI gates added; every one green on `main` when it landed.** PR #40
  made `golangci-lint` a gate and, in passing, enumerated the gates this
  repo was missing. These are those, each in its own commit.

  Two are not hygiene. **`make vuln` and `make licenses` existed in the
  Makefile and ran in no job** — and this repo stages cross-compiled release
  archives and ships a Docker image, so every dependency it links is
  redistributed: `make licenses` forbidding GPL/AGPL/LGPL/MPL against an MIT
  tree is a statement about a published artifact, and a known-vulnerable
  dependency reaching a shipped binary had nothing reporting it. Both now
  run in a `security` job. And **there was no `go mod tidy` / `go.sum`
  verification gate**, which is immediately relevant rather than
  theoretical: the ADR 0070 phase 5 migration ahead is a series of
  `go-hamqtt` version bumps, and a bump is exactly what leaves go.mod drift.
  The new `make tidy-check` found drift immediately: when this branch was
  cut, `go.sum` still carried both hashes for `go-mqtt` v1.3.0 after the
  bump to v1.4.0. That has since been cleared upstream by #41's own tidy, so
  no dependency version moves here — the gate is what stops it recurring
  across the bumps still to come.

  Also added: **`gitleaks`** in git mode over the full history (a secret
  committed once and reverted is still in the history and still a secret,
  and this daemon handles a cloud app token plus MQTT credentials);
  deliberately configless, because the default ruleset is clean over the whole
  history and an allowlist added before anything tripped a rule would only
  pre-exempt future findings. **A per-package coverage floor**
  (`make cover-check`, `COVER_MIN=25`), per package rather than on a merged
  total so one well-tested package cannot hide a package nothing executes,
  with the eight packages below the floor pinned at their current numbers —
  a ratchet, not an aspiration. Six of those eight have no test file at all. And **`make fuzz-smoke`** over three new
  `./internal/process` targets, covering the parsing that turns
  device-supplied strings into topic levels and `unique_id`s.

  Every tool is pinned in the workflow *and* in `make setup`, so the local
  and CI gates cannot drift: `govulncheck` v1.8.0, `go-licenses` v1.6.0,
  `gitleaks` v8.30.1, alongside PR #40's `gofumpt` v0.12.0 and
  `golangci-lint` v2.13.2.

  Nothing about the daemon changes. The four pinned discovery payloads are
  untouched, including the two rows that pin defects.

  Reported and deliberately not fixed: nothing bounds the length of a
  device-supplied property name. `validPackSN` caps a battery serial at 64
  bytes, but `sanitizeSegment` passes a name of any length through, so a
  property name longer than MQTT's 65535-byte topic limit produces exactly
  the rejected PUBLISH that function exists to prevent. The fuzz target
  documents the carve-out rather than asserting it, so the gate reports on
  the parsing instead of re-reporting the known gap on every run.

- **`golangci-lint` is now a CI gate, and the four findings it had been
  reporting on `main` are fixed.** The `lint` job ran `go vet` plus
  `gofumpt -l` only, so the `.golangci.yaml` in the repo was enforced
  nowhere but on a developer's own machine — where it already failed:
  `exhaustive` in `internal/config/load.go`, `nilerr` and `revive` in
  `internal/zendure/cloud/source.go`, and `staticcheck` in
  `internal/process/process.go`. A local gate that is already red is worse
  than no gate, because the next person to run `make check` cannot tell
  their own finding from the four that were already there.

  Both lint tools are now pinned by version — `gofumpt v0.12.0` and
  `golangci-lint v2.13.2` — in the workflow *and* in `make setup`, so a
  local run reports what CI reports and an unrelated upstream release
  cannot turn someone else's PR red. No published byte moves: the four
  discovery and state-topic goldens are unchanged.

### Fixed

- **`coerceEnvValue`'s kind switch states its open set explicitly**
  (`internal/config/load.go`). It handles bool, the signed ints and the
  floats, and every other `reflect.Kind` keeps the raw string so `Validate`
  can report it — which its doc comment already promised. An explicit
  `default` says that; enumerating the 19 remaining kinds would have
  claimed the opposite. No behaviour change.

- **The cloud backend's nil return on a cancelled login retry is now
  annotated, not silent** (`internal/zendure/cloud/source.go`).
  `loginWithRetry` only ever returns `ctx.Err()`, so `Run` returning `nil`
  there is a clean shutdown, consistent with every other `Run` in the
  daemon; propagating the error would have made an ordinary SIGTERM exit
  non-zero. The retry loop's `if`/`else-if`/`else` is also flattened, which
  makes that single non-nil exit visible on the page.

- **`sanitizeSegment` uses a tagged switch** (`internal/process/process.go`)
  instead of five `||`-chained equality tests in one case. Pure form change
  — same constants, same default — and the pinned goldens prove it, since
  this function feeds published topic levels and `unique_id`s.

### Changed

- **go-mqtt v1.3.0 → v1.4.0, and the hand-rolled split client is gone.**
  v1.4.0 is purely additive — it adds `SplitClient(Publisher, Subscriber)`
  and `ConnectWithRetry`, and changes no behaviour — and `SplitClient` is
  exactly the nine-line `mqttSession` struct this repo carried to put the
  circuit breaker on the publish half while subscriptions went to the raw
  client. `cmd/zendure2mqtt/main.go` now uses the shared helper. No wire
  behaviour changes; the tests that pinned the split still pin it, now
  against the library type.

- **Dropped the dead `object_id` key from HA discovery payloads.** Home
  Assistant's MQTT discovery schemas are `extra=REMOVE_EXTRA`; measured against
  the schemas of HA 2026.9, `object_id` is accepted by 0 of 32 MQTT platforms
  (`default_entity_id` by 28), so it was being silently dropped on arrival.
  The payload already carried `default_entity_id` with the same seed at every
  site, so this is a pure deletion with no user-visible effect: entity ids are
  unchanged, retained configs just get smaller and stop advertising a key that
  no longer does anything.

# Version 0.7.0 (2026-08-16)

## What's Changed

Dependency update, no functional changes in this repo's own code.

### Changed

- **go-mqtt v1.2.0 → v1.3.0.** Picks up the upstream audit release (42
  adversarially verified findings — 3 high, 7 medium, 32 low — across
  concurrency, decoder robustness, resource limits, and spec
  conformance). Exported API changes are purely additive; no code
  changes were needed here to build against it.
- **Reconnect flap damping is now the default.** The output broker's
  `Lifecycle` (`cmd/zendure2mqtt/main.go`, built with just a `Logger`
  set) picks up `LifecycleConfig.FlapWindow`'s new 10s default: if the
  link dies within 10s of coming up, the reconnect now backs off
  exponentially instead of redialling immediately, damping a flapping
  broker instead of hammering it at full speed. A one-off drop still
  reconnects promptly via the existing event-driven path. Left at the
  library default — appropriate for this bridge, not set explicitly.
- **Output circuit breaker no longer trips on client-side validation
  errors.** `mqtt.Breaker` (wraps the output client in `main.go`) used
  to count a rejected malformed publish (bad topic, oversized string,
  ...) as a broker-side failure; that could open the circuit and mute
  otherwise-healthy publishes. Complements the 0.6.0 hardening in this
  repo that sanitizes device-supplied topic segments before they reach
  MQTT.
- **No more spurious reconnect after an intentional `Disconnect`.**
  Benefits the cloud backend's own hand-rolled `connectLoop`
  (`internal/zendure/cloud/source.go`), which calls `client.Disconnect`
  itself on shutdown.

Not applicable to this bridge: the QoS 2 unknown-identifier recovery
fix and the new `$share/...` delivery support (every publish/subscribe
here is QoS 0 and uses plain filters, no shared subscriptions), and the
stricter inbound frame validation (transparent to callers).

# Version 0.6.1 (2026-07-07)

## What's Changed

Dependency update, no functional changes in this repo.

### Changed

- **go-mqtt v1.1.0 → v1.2.0.** Picks up the upstream hardening release (28
  adversarially verified fixes across concurrency, decoder robustness, resource
  limits, and spec conformance). Most relevant here: concurrent
  `Connect`/`Disconnect` calls are now fully serialised, which benefits the
  cloud backend's reconnect loop. No API changes; no config changes required.

# Version 0.6.0 (2026-07-07)

## What's Changed

Codebase hardening pass: a multi-agent review found and adversarially verified a
set of robustness and untrusted-input defects, fixed here. No config changes are
required, except that `CHARGE_ACTIVE_VALUE` / `DISCHARGE_ACTIVE_VALUE` now reject
an explicit `0` (see below).

### Security / robustness

- **mDNS parser no longer hangs on a crafted packet.** The hand-written DNS
  name-compression decoder (`zendure2mqtt-util discover`) followed backward
  pointers with a guard that a self-referential pointer could defeat, spinning
  the CPU and allocating without bound. Pointer indirections are now capped, so
  a malicious LAN response yields a parse error instead of a hang.
- **Device and cloud HTTP responses are size-capped.** `FetchReport` (local,
  unauthenticated plain HTTP) and the cloud login response were read with an
  unbounded `io.ReadAll`; a malfunctioning or hostile peer could stream
  gigabytes and OOM the daemon. Both now read through a `LimitReader` (4 MiB /
  1 MiB) and error on overflow.
- **Diagnostic web server got read/write/idle timeouts.** Only
  `ReadHeaderTimeout` was set, so an idle keep-alive or slow reader could pin a
  connection indefinitely (fd exhaustion). `ReadTimeout`, `WriteTimeout` and
  `IdleTimeout` are now set.
- **Device-supplied topic segments are sanitized.** Unmapped property keys and
  battery-pack serials flowed verbatim into MQTT topics; a key or serial with
  `/`, `+` or `#` produced protocol-violating publishes that counted against the
  output circuit breaker and could mute the whole bridge. Segments are now
  neutralized and implausible pack serials dropped (which also bounds the
  otherwise-unbounded set of discovery sub-devices a rogue device could mint).

### Fixed

- **Fatal map-race in cloud mode.** `bySN` was read by the command handler while
  the telemetry goroutine wrote it, which the Go runtime turns into a fatal
  `concurrent map read and map write`. It is now guarded by an `RWMutex`.
- **Inbound writes no longer block the MQTT read loop.** A slow or unreachable
  device could stall all inbound command dispatch (and trip the keep-alive
  watchdog) because backend writes ran synchronously in the handler. Writes are
  now performed off the read loop.
- **`/set` command payloads are validated.** `NaN`, `Inf` and out-of-range
  values were converted to garbage `int64` and written to the hardware,
  bypassing the catalog's advertised min/max. Numeric commands are now clamped
  to the catalog bounds and non-finite values rejected.
- **Transient cloud-login failure now retries** with backoff instead of idling
  the backend permanently until a manual restart.
- **Failed startup `/set` subscription now retries** in the background; a drop
  in the connect/subscribe window no longer silently disables all commands until
  restart.
- **Orphan-reconcile no longer permanently deletes live entities.** A
  transiently shrunken report could clear still-live discovery configs that were
  then never republished; cleared configs are now forgotten from the sent-cache
  so the next report restores them. Concurrent per-device reconciles are also
  serialized so they no longer truncate each other's collection.
- **Type-blind env coercion corrupted string credentials.** A numeric-looking
  `ZENDURE_MQTT_PASSWORD`/`WEB_PASSWORD` (e.g. `0123456`, `1e5`) was silently
  rewritten. Env values are now coerced per target field type.
- **Catalog load rejects duplicate properties/topic leaves and unknown
  platforms** instead of silently last-winning (which could misroute a write to
  the wrong device property).
- **`CHARGE_ACTIVE_VALUE` / `DISCHARGE_ACTIVE_VALUE` reject an explicit `0`.**
  A `0` was silently rewritten to the 1200 W default (opposite of intent); the
  valid range is now `1..2400` and omitting the key still takes the default.

# Version 0.5.1 (2026-07-06)

## What's Changed

### Fixed

- **Entity-id seed now published as both `object_id` and `default_entity_id`.**
  Current Home Assistant releases do not yet honour `default_entity_id`
  reliably (home-assistant/core#157241 — the seed is ignored and a generic
  `entity_id` is generated instead), while the deprecated `object_id` still
  works. The discovery payload now carries both — `object_id` for today's HA,
  `default_entity_id` for future HA — so the configured `DEVICE_NAME` (0.5.0)
  actually lands in the `entity_id`. Both carry the same English,
  language-independent seed; only the display `name` is localized. Matches the
  go-mtec2mqtt twin.

# Version 0.5.0 (2026-07-06)

## What's Changed

### Added

- **Optional friendly device name.** Each `LOCAL_DEVICES` entry now takes an
  optional `DEVICE_NAME` (add-on option `device_name`). When set it replaces
  the serial number in the Home Assistant device name and seeds the
  language-independent `entity_id`s — entities read `<DEVICE_NAME> …`
  (e.g. `sensor.balkon_speicher_electric_level`) instead of
  `Zendure <SN> …`. Battery sub-devices inherit it (`<DEVICE_NAME> Pack <sn>`).
  MQTT topics, the retained discovery config topics and the `unique_id`s all
  stay keyed on the serial number — the name is purely cosmetic and needs **no
  migration**. Note: Home Assistant does not rename already-registered
  `entity_id`s — setting `DEVICE_NAME` on a device that is already onboarded
  updates the device name but needs a discovery reset (or a manual rename) to
  move existing entity_ids.

# Version 0.4.0 (2026-07-04)

## What's Changed

### Changed

- **MQTT publishes are circuit-protected.** Upgraded to `go-mqtt` v1.1.0
  and adopted its new `Breaker` decorator on the output-broker publish
  path (coordinator state/availability/discovery-clear publishes and the
  Home Assistant discovery configs): during a degraded-broker phase (TCP
  link up, acknowledgements missing) publishes fail fast with
  `ErrCircuitOpen` instead of each stalling on the full ack timeout.
  After 5 consecutive broker-side failures the circuit opens; after 30
  seconds a single half-open probe tests recovery, and one success
  closes the circuit again. Local conditions (caller cancellation,
  oversized packets) never trip it. Every state transition is logged as
  a `zendure2mqtt.mqtt_breaker_state` warning. Subscriptions are
  deliberately not gated — they carry their own SUBACK-bounded wait and
  must keep working while the publish side is browned out. The Zendure
  cloud link is untouched — the breaker guards the output broker only.
  The lifecycle's reconnect loop remains in charge of the link itself.

# Version 0.3.0 (2026-07-04)

## What's Changed

Adopts [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt) v1.0.0.
**MQTT 5.0 is now the wire default** for the output broker link (3.1.1 is still
selectable via `ProtocolVersion`); reconnects are event-driven off the client's
`ConnectionLost()` channel for tighter recovery; and the underlying client now
supports full QoS 0/1/2 (this bridge itself still only ever publishes/subscribes
at QoS 0).

### Changed

- Every `Subscribe` call (output-broker command subscription, the HA-discovery
  orphan-reconcile subscribe, and the cloud backend's per-device subscribes) now
  blocks until the broker's SUBACK and returns a hard error on a rejected filter,
  instead of the subscription silently going live with only a broker-side log
  line to notice a reject.
- `Publish` now fails fast when the underlying connection is down, rather than
  blocking until a dial/write timeout.
- `MessageHandler` is now `func(*mqtt.Message)` (was
  `func(topic string, payload []byte, retained bool)`); the coordinator's
  `handleSet`, the discovery orphan-reconcile handler, and the cloud backend's
  `handleMessage` were migrated to `msg.Topic`/`msg.Payload` natively.
- Output broker LWT is now configured via `Will: &mqtt.Will{Topic, Payload,
  Retain}` (was `WillTopic`/`WillPayload`/`WillRetain`); `CleanSession` was
  renamed to `CleanStart` (MQTT 5.0 terminology) — both apply to the output
  broker only, with no config-file or env-var change for this bridge's users.
- The Zendure cloud link (`internal/zendure/cloud/source.go`) is pinned to
  `ProtocolVersion: mqtt.ProtocolV311`: the third-party `mqtteu.zen-iot.com`
  broker's MQTT 5.0 support is unverified, so the cloud connection stays on
  3.1.1 while the local output broker uses the new v5 default.

# Version 0.2.1 (2026-07-03)

## What's Changed

Adopts [`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt) v0.2.0:
retained MessageHandler flag, per-filter QoS replay on reconnect, and a
hardened ping watchdog (no more spurious ping_timeout reconnects).

# Version 0.2.0 (2026-07-02)

## What's Changed

Replaces the per-repo `internal/mqtt` copy with the shared
[`github.com/SukramJ/go-mqtt`](https://github.com/SukramJ/go-mqtt) module
(v0.1.0), so MQTT transport fixes land once and are picked up via `go get -u`
instead of drifting across the four `go-*2mqtt` bridges.

### Changed

- MQTT client switched from the local `internal/mqtt` package to the shared
  `github.com/SukramJ/go-mqtt` module (v0.1.0) — a superset of the four
  previously-duplicated `internal/mqtt` copies. No behavioral change to the
  cloud backend's event-driven reconnect, which keeps using
  `TCPClient.ConnectionLost()`.

### Security / Fixed (inherited from the shared module)

- MQTT frame-size cap: the wire codec now rejects an oversized `remaining
  length` before allocating a body buffer, closing an OOM/DoS vector against
  a malicious or malfunctioning broker.
- Broker-rejected subscriptions are now logged: SUBACK return codes are
  parsed and surfaced instead of being silently ignored.

# Version 0.1.4 (2026-07-02)

## What's Changed

Fixes MQTT half-open connection detection so a broker or network drop without a
TCP FIN/RST no longer wedges the daemon until a manual restart.

### Fixed

- MQTT half-open connections are now detected and recovered. The keep-alive loop
  sent PINGREQ but never checked that the matching PINGRESP came back, and the
  read loop runs without a read deadline — so a broker/network drop without a TCP
  FIN/RST (e.g. a Mosquitto or Home Assistant restart) left the read loop blocked
  in `ReadFrame` forever: the socket was never torn down, no reconnect happened,
  and QoS-1 publishes timed out with `context deadline exceeded` on the dead
  socket until a manual restart. A PINGRESP watchdog now declares the connection
  lost when a keep-alive ping goes unanswered, so the existing reconnect logic
  re-dials automatically (within one keep-alive interval).

# Version 0.1.3 (2026-06-27)

## What's Changed

Adds Home Assistant MQTT discovery orphan cleanup, so entities that a newer
release no longer publishes (a property removed, renamed or re-platformed in the
catalog) stop lingering as permanently "unavailable" entities in Home Assistant.

### Added

- Discovery orphan reconciliation. After publishing a device's discovery configs
  the coordinator collects the broker's retained configs under the discovery
  prefix and clears (empty retained payload) any that are ours but no longer in
  the published set. Ownership is guarded by the `zendure_` unique_id namespace
  and the bridge's state-topic root, and scoping is per device (by the
  `zendure_<sn>_` unique_id prefix), so configs from other integrations or other
  Zendure devices are never touched. The reconcile is asynchronous, gated per
  device, and only runs when a device's entity set actually changes.

# Version 0.1.2 (2026-06-27)

## What's Changed

Maintenance release. Internal code-quality cleanup only — no functional or
user-facing changes.

### Changed

- Resolved all 20 `golangci-lint` findings (`httpNoBody`, `rangeValCopy`,
  `unnamedResult`, revive naming, `contextcheck`, gosec G115, `makezero`,
  staticcheck) so `make check` passes the full linter gate. No behavior change.

# Version 0.1.1 (2026-06-27)

## What's Changed

Maintenance release. No functional changes to the daemon; adds the Home Assistant
add-on icon and tightens the CI supply chain.

### Added

- Home Assistant add-on icon (`addon/icon.png`) — the Zendure brand logo, sourced
  from the `home-assistant/brands` repository. Attributed in the README as a
  Zendure trademark, not covered by this project's MIT license.

### Changed

- CI supply-chain hardening, Dependabot auto-merge (patch/minor) with a 7-day
  cooldown, and dependency bumps (`actions/setup-go`, `dependabot/fetch-metadata`).
- Documentation updates and upstream attribution for the Zendure zenSDK and the
  `Zendure/zendure-ha` integration.

### Dependencies

- Go modules already at their latest releases (`golang.org/x/sync` v0.21.0,
  `gopkg.in/yaml.v3` v3.0.1) — no changes required.

# Version 0.1.0 (2026-06-19)

## What's Changed

First release. A pure-Go bridge connecting Zendure devices (e.g. the SolarFlow
2400 AC) to MQTT with Home Assistant auto-discovery — locally over the device's
on-board HTTP API (zenSDK) or via the Zendure cloud. Verified end-to-end against
a real SolarFlow 2400 AC. Project setup adopted from `go-daikin2mqtt`.

### Added

- Project skeleton: `cmd/zendure2mqtt` (daemon) + `cmd/zendure2mqtt-util` (CLI),
  Makefile, Dockerfile (distroless), `.golangci.yaml`, GitHub Actions.
- Pure-Go MQTT client (`internal/mqtt`, TLS-capable) with a reconnecting lifecycle.
- Transport-neutral data model (`internal/zendure/model`) and `Source`/`Controller`
  seam (`internal/source`): the local and cloud backends share one pipeline.
- Local HTTP backend (`internal/zendure/local`): polls `GET /properties/report`
  and writes via `POST /properties/write`, with an immediate re-read after a write
  (`reReadSoon`) so Home Assistant reflects changes sub-second.
- Cloud backend (`internal/zendure/cloud`): app-token decode + signed
  `/api/ha/deviceList` login, then a TLS MQTT stream to the Zendure cloud broker —
  subscribes telemetry and publishes control. `CLOUD_TLS_VERIFY` (default false)
  toggles strict certificate verification (the Zendure cloud cert is non-standard;
  the connection stays TLS-encrypted). Event-driven, stability-aware reconnect
  (new `TCPClient.ConnectionLost()` signal): occasional drops recover in ~1 s,
  flapping is throttled 1→2→…→30 s.
- Declarative property catalog (`zendure.yaml`) for the SolarFlow 2400 AC, with
  offset/scale, English/German value maps and per-pack battery values.
- Coordinator: publishes resolved state, routes `…/set` commands to writes, and
  drives Home Assistant discovery (`internal/hass`) — `default_entity_id`
  (English, language-independent) instead of the removed `object_id`, localized
  display names, an `availability_topic` bound to `bridge/status`, battery packs
  as their own sub-devices (`via_device`), and rich device-registry info
  (`serial_number`, `model_id`, `sw_version`, `configuration_url`).
- Virtual charge/discharge switches (`internal/virtual`): synthetic HA switches
  that write a property set (`acMode` + power limit + `smartMode`) on toggle and
  derive their state from the report; limits via `CHARGE_ACTIVE_VALUE` /
  `DISCHARGE_ACTIVE_VALUE`.
- Read-only diagnostic web UI (`internal/web` + `internal/state`): an embedded
  single-page app (no build step) over `/api/health` + `/api/snapshot`, optional
  HTTP basic auth, served on `WEB_BIND`.
- Dependency-free mDNS browser (`internal/discovery`) plus `zendure2mqtt-util`
  subcommands: `discover`, `report`, `resolve`, `set`, `cloud-login`,
  `catalog-check`.
- Home Assistant add-on (`addon/` + root `repository.yaml`): a Zendure options
  schema, `build.yaml`, a multi-stage Dockerfile, and `script/run.sh` mapping
  options onto `ZENDURE_*` env + a generated `LOCAL_DEVICES` config. Multi-arch
  images via `addon-image.yml`.
- Config loader with `ZENDURE_*` env overrides + validation; unit tests for
  config, process (scaling / value maps / packData), cloud token handling, mDNS
  parsing, and the web handlers.

### Verified against hardware

- Live local state publish (79 points) with correct scaling — `socSet`/`minSoc`
  are deci-percent (raw 950 → 95 %), the read scale inverted on writes.
- HA auto-discovery: 34 entities across 3 devices (main unit + 2 battery packs),
  English entity_ids, German display names, localized select options. Graceful
  shutdown publishes `bridge/status=offline`.
- Cloud: TLS connect to `mqtteu.zen-iot.com:8883` (MQTT 3.1.1), subscribe, live
  telemetry republished. The cloud enforces a single session and drops this
  clientId every ~1 s, so cloud mode trickles telemetry — **local mode is the
  recommended path for this device.**

### Known limitations

- This SolarFlow 2400 AC does not advertise `_zendure._tcp` over mDNS, so local
  mode uses static `LOCAL_DEVICES` IPs; the browser is provided for models that
  do advertise.
- Home Assistant does not move already-registered entities to a new device nor
  rename entity_ids, so a discovery-schema change needs a one-time reset (clear
  the retained `homeassistant/.../config` topics, then republish).
- Roadmap: wire mDNS auto-discovery into the local backend; web-UI live push
  (SSE) and write access.

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

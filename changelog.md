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

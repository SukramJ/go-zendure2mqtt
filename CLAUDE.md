# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A pure-Go daemon that bridges **Zendure** devices (e.g. SolarFlow 2400 AC) to **MQTT**
with **Home Assistant** auto-discovery — locally over the device's on-board HTTP API
(zenSDK) or via the Zendure cloud (TLS MQTT). Twin of `go-daikin2mqtt` / `go-mtec2mqtt`;
shares their setup (custom MQTT client, declarative catalog, distroless image, HA add-on).
Two binaries: `zendure2mqtt` (daemon) and `zendure2mqtt-util` (diagnostics CLI).

`docs/konzept.md` is the design doc and milestone plan. **State: M0–M4 complete** and
verified against a real SolarFlow 2400 AC — local + cloud transports, HA discovery with
battery sub-devices and i18n, virtual switches, a diagnostic web UI, an mDNS browser, and
a Home Assistant add-on.

## Commands

```bash
make build            # both binaries → bin/ (ldflags inject version/commit/date)
make test             # full suite with -race
make check            # pre-commit gate: vet + fmt-check + lint + test
make fmt              # gofumpt + goimports
go build ./... && go test ./...   # quick local loop (no dev tooling needed)

# util CLI
go run ./cmd/zendure2mqtt-util report   --host 192.168.1.50
go run ./cmd/zendure2mqtt-util resolve  --host 192.168.1.50 --lang de
go run ./cmd/zendure2mqtt-util cloud-login --token <app-token>
go run ./cmd/zendure2mqtt-util discover
```

Go ≥ 1.26, `CGO_ENABLED=0`. Minimal deps (`golang.org/x/sync`, `yaml.v3`) — keep it that way.

## Architecture

**Core idea:** local and cloud share one property model; they differ only in transport.
A transport-neutral `source.Backend` (`Source` with `Run`/`Read` + `Controller` with
`Write`) has two impls: `local` (HTTP poll/write) and `cloud` (TLS MQTT pub/sub). Everything
downstream — catalog, process, coordinator, hass, state/web — is transport-neutral.

### Runtime wiring (`cmd/zendure2mqtt/main.go`, `run()`)
Load config → load catalog (`zendure.yaml`) → `buildBackend` (local or cloud per
`CONNECTION`) → connect the output MQTT (custom client + Lifecycle, LWT) → optional
`hass.Discovery` and `state.Store` → `coordinator.Run` (+ optional `web` server) in an
errgroup; on graceful shutdown publish `bridge/status=offline`, then stop.

### Coordinator (`internal/coordinator/`)
Subscribes `<root>/+/+/+/set`, then drives the backend via an `onReading` callback →
`publish()`: `process.Resolve(report, catalog, lang)` + synthetic `switchPoints` → update
the optional `state.Store` → HA discovery (once per unique_id) → publish each point's state.
Inbound `…/set`: `handleSwitchSet` (virtual switches) first, else `catalog.ByTopic` →
`decodeCommand` (select label → code in either language; numeric value un-scaled to raw) →
`backend.Write`. Every successful write triggers `reReadSoon` (≈750 ms later: one-shot
`Read` + republish) so HA reflects the change sub-second.

### Backends (`internal/zendure/`)
- `model` — `Report`/`WriteRequest`, the shared data shape (`ParseReport`).
- `local` — `FetchReport` (`GET /properties/report`), `WriteProperties`
  (`POST /properties/write`), a one-shot `Read`, and a poll `Backend` (a goroutine per device).
- `cloud` — `DecodeToken` (base64 → `<api_url>.<appKey>`) + signed `Login`
  (`POST /api/ha/deviceList`, `sign = SHA1(HAKEY + sorted(params) + HAKEY)`), then a **TLS
  MQTT stream**: subscribe `iot/{productKey}/{deviceId}/properties/report`, publish control
  to `.../properties/write`. Its own **event-driven, stability-aware reconnect loop**
  (`connectLoop` on `TCPClient.ConnectionLost()`): occasional drops recover in ~1 s, a
  flapping broker is throttled 1→2→…→30 s. `CLOUD_TLS_VERIFY` (default false) toggles strict
  cert verification — the Zendure cloud cert is non-standard. **Caveat:** the cloud drops
  this clientId every ~1 s (single-session), so cloud mode trickles telemetry; prefer local.

### Catalog & process (`zendure.yaml`, `internal/catalog`, `internal/process`)
`Entry` maps a raw property → topic/group/platform/unit, with `offset`/`scale`
(`value = (raw-offset)/scale`), `value_map`/`value_map_de` (code → en/de label, reversed on
write), `writable`, `min`/`max`/`step`. `process.Resolve` flattens a report into `Point`s
and expands `packData[]` into per-pack battery sub-entities; unmapped properties still
publish under `…/misc/`. Topic helpers (`StateTopic`/`CommandTopic`) are the single source of
the topic scheme.

### HA discovery (`internal/hass`)
**Entity-ID invariant (do not break):** `default_entity_id` (not `object_id`, which HA
removed) is English/language-independent — `<platform>.<slug(deviceName)>_<english_topic>`;
the display `name` is localized (German when `LANGUAGE: de`), and select option labels too
(`value_map_de`). Battery packs are split into their own sub-devices via `via_device`. Rich
device registry: `serial_number`, `model_id` (= report `product`), `sw_version` (pack), and
`configuration_url` (device IP). **HA does not move already-registered entities to a new
device nor rename entity_ids** — a discovery-schema change needs a one-time reset (clear the
retained `homeassistant/.../config` topics, then republish).

### Virtual switches (`internal/virtual`)
`charge_active` / `discharge_active`: synthetic HA switches with no single backing property.
ON writes a property set (`acMode` + `inputLimit`/`outputLimit` + `smartMode`); state is
derived from the report (`acMode==1 && inputLimit>0`, etc.). Limits via
`CHARGE_ACTIVE_VALUE`/`DISCHARGE_ACTIVE_VALUE`. They flow through the normal publish +
discovery path as synthetic points; only the `/set` write is special-cased in the coordinator.

### Web UI + state (`internal/web`, `internal/state`)
Optional read-only diagnostic UI (`WEB_ENABLE`): an embedded vanilla SPA (`//go:embed`, no
build step) over `GET /api/health` + `GET /api/snapshot`, optional HTTP basic auth, served
on `WEB_BIND`. The coordinator feeds a thread-safe `state.Store` (allocated only when the UI
is on). HA-Ingress-friendly (relative asset URLs).

### mDNS discovery (`internal/discovery`)
A tiny dependency-free mDNS browser (PTR/SRV/A/TXT parse with name compression) for
`_zendure._tcp`. Exposed via `zendure2mqtt-util discover`. Not yet wired into the local
backend as auto-discovery (and this SolarFlow does not advertise mDNS anyway).

### MQTT (`github.com/SukramJ/go-mqtt`, `github.com/SukramJ/go-mqtt/protocol`)
Custom pure-Go MQTT client (no third-party lib), TLS-capable, with a reconnecting `Lifecycle`
(used for the output broker). Since go-mqtt v1.0.0 the wire default is **MQTT 5.0**; the
output/local broker link uses that default, while the Zendure cloud link
(`internal/zendure/cloud/source.go`) pins `ProtocolVersion: mqtt.ProtocolV311` — the
third-party broker's MQTT 5.0 support is unverified. Shared module extracted from the twin
projects (formerly a per-repo `internal/mqtt` copy); includes the `TCPClient.ConnectionLost()`
channel that the cloud backend's reconnect loop reads.

### Topic layout
```
<MQTT_TOPIC>/<sn>/<group>/<topic>/state            # retained, QoS0 (group: now|config|static)
<MQTT_TOPIC>/<sn>/battery/<packSn>/<topic>/state   # per-pack values
<MQTT_TOPIC>/<sn>/<group>/<topic>/set              # subscribed, writable entities + switches
<MQTT_TOPIC>/bridge/status                         # LWT + explicit offline on shutdown
homeassistant/<platform>/zendure2mqtt_<sn>_<topic>/config  # HA discovery, retained
```

## Config

Flat YAML (`config-template.yaml` documents every key); scalar keys overridable via
`ZENDURE_<KEY>` env (bool/int/float coerced). Loader: file → env → defaults → validate.
Key fields: `CONNECTION` (local|cloud), `LOCAL_DEVICES` (YAML list of SN+HOST), `REFRESH`,
`CLOUD_APP_TOKEN`/`CLOUD_TLS_VERIFY`, `MQTT_*`, `HASS_*`, `WEB_*`, `LANGUAGE`,
`CHARGE_ACTIVE_VALUE`/`DISCHARGE_ACTIVE_VALUE`. Required: `MQTT_SERVER`. Missing local
devices or cloud token is non-fatal — the daemon starts and stays idle. `config.yaml` holds
credentials and is gitignored.

## Home Assistant add-on (`addon/` + `repository.yaml`)
`addon/config.yaml` is the options schema; `script/run.sh` maps options onto `ZENDURE_*` env
plus a generated `LOCAL_DEVICES` config file (env overrides only carry scalars). Multi-arch
images are built by `.github/workflows/addon-image.yml` (`ghcr.io/sukramj/go-zendure2mqtt-addon-{arch}`).

## Testing

Plain table/assert tests with hermetic inputs (no broker/device needed): `config` (env
override + validation), `process` (scaling / value maps / packData + topic scheme), `virtual`
(state derivation + write props), `cloud` (token decode + sign), `discovery` (mDNS parse incl.
compression), `web` (handlers + basic auth). MQTT-client tests live in the shared
`github.com/SukramJ/go-mqtt` module, not in this repo. Run with `-race` (`make test`).

## Release

Bump the version in **four** places in one commit: `internal/version/version.go` (`Version`
default), `addon/config.yaml` (`version`), `addon/Dockerfile` (`BUILD_VERSION`), and add a
`# Version X.Y.Z (YYYY-MM-DD)` section at the top of `changelog.md` (extracted by
`script/extract-release-notes.sh`). **Also** add a matching `## X.Y.Z` entry to the
Home Assistant add-on changelog `addon/CHANGELOG.md` (user-facing, HA-oriented summary;
call out any breaking add-on-option changes). Branch → PR → squash-merge → tag **`vX.Y.Z`**
(the `v` triggers `release-on-tag.yml`, `docker-build-push.yml`, `addon-image.yml`).

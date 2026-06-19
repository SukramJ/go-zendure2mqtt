# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A pure-Go daemon that bridges **Zendure** devices (e.g. SolarFlow 2400 AC) to **MQTT**
with optional **Home Assistant** auto-discovery. Twin of `go-daikin2mqtt` /
`go-mtec2mqtt`; shares their setup (custom MQTT client, declarative catalog, distroless).
Two binaries: `zendure2mqtt` (daemon) and `zendure2mqtt-util` (diagnostics CLI).

`docs/konzept.md` is the design doc and milestone plan. **Current state: M0 scaffold** —
the local HTTP backend, catalog, MQTT publish/command path and HA discovery are wired;
the cloud backend implements login + device discovery but not the live MQTT stream (M3).

## Commands

```bash
make build            # both binaries → bin/ (ldflags inject version/commit/date)
make test             # full suite with -race
make check            # pre-commit gate: vet + fmt-check + lint + test
make fmt              # gofumpt + goimports
go build ./... && go test ./...   # quick local loop (no dev tooling needed)

# util CLI
go run ./cmd/zendure2mqtt-util catalog-check --catalog zendure.yaml
go run ./cmd/zendure2mqtt-util report --host 192.168.1.50
go run ./cmd/zendure2mqtt-util cloud-login --token <app-token>
```

Go ≥ 1.26, `CGO_ENABLED=0`. Minimal deps (`golang.org/x/sync`, `yaml.v3`) — keep it that way.

## Architecture

**Core idea:** local and cloud share one property model; they differ only in transport.
A transport-neutral `source.Backend` (`Source` + `Controller`) has two impls: `local`
(HTTP poll/write) and `cloud` (MQTT pub/sub, M3). Everything downstream is transport-neutral.

### Runtime wiring (`cmd/zendure2mqtt/main.go`, `run()`)
Load config → load catalog (`zendure.yaml`) → `buildBackend` (local or cloud per
`CONNECTION`) → connect MQTT (custom client + Lifecycle, LWT) → optional `hass.Discovery`
→ `coordinator.Run` in an errgroup; block until signal or error.

### Coordinator (`internal/coordinator/`)
Subscribes `<root>/+/+/+/set`, then runs the backend with an `onReading` callback:
`process.Resolve(report, catalog)` → publish HA discovery (once per unique_id) → publish
each point's state. Inbound `…/set` → `catalog.ByTopic` → `decodeCommand` (select label →
code, numeric coercion) → `backend.Write`.

### Backends (`internal/zendure/`)
- `model` — `Report`/`WriteRequest`, the shared data shape (`ParseReport`).
- `local` — `FetchReport` (`GET /properties/report`), `WriteProperties`
  (`POST /properties/write`), and a poll `Backend` (one goroutine per device).
- `cloud` — `DecodeToken` (base64 → `<api_url>.<appKey>`) + signed `Login`
  (`POST /api/ha/deviceList`, `sign = SHA1(HAKEY + sorted(params) + HAKEY)`). The MQTT
  telemetry/control stream is the M3 TODO; `Backend.Run` currently logs in and blocks.

### Catalog & process (`zendure.yaml`, `internal/catalog`, `internal/process`)
`Entry` maps a raw property → topic/group/platform/unit, with `offset`/`scale`
(`value = (raw-offset)/scale`), `value_map` (code → label, reversed on write), `writable`,
`min`/`max`/`step`. `process.Resolve` flattens a report into `Point`s and expands
`packData[]` into per-pack battery sub-entities. Topic helpers (`StateTopic`/`CommandTopic`)
are the single source of the topic scheme.

### MQTT (`internal/mqtt`, `internal/mqtt/protocol`)
Custom pure-Go MQTT 3.1.1 (no third-party lib), TLS-capable, with a reconnecting
`Lifecycle` that re-fires `OnConnect` (re-announces availability). Lifted verbatim from the
twin projects — treat as a stable vendored library.

### Topic layout
```
<MQTT_TOPIC>/<sn>/<group>/<topic>/state            # retained, QoS0 (group: now|config|static)
<MQTT_TOPIC>/<sn>/battery/<packSn>/<topic>/state   # per-pack values
<MQTT_TOPIC>/<sn>/<group>/<topic>/set              # subscribed, writable entities
<MQTT_TOPIC>/bridge/status                         # LWT: online/offline
homeassistant/<platform>/zendure2mqtt_<sn>_<topic>/config  # HA discovery, retained
```

## Config

Flat YAML (`config-template.yaml` documents every key); scalar keys overridable via
`ZENDURE_<KEY>` env (bool/int/float coerced). Loader: file → env → defaults → validate.
Required: `MQTT_SERVER`. `LOCAL_DEVICES` is a YAML list (SN + HOST). Missing local devices
or a missing `CLOUD_APP_TOKEN` is non-fatal — the daemon starts and stays idle.

## Testing

Plain table/assert tests with hermetic inputs (no broker needed): `config` (env override +
validation), `process` (scaling/value_map/packData + topic scheme), `cloud` (token decode).
The `internal/mqtt` tests come from the upstream twin. Run with `-race` (`make test`).

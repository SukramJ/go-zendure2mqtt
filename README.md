# go-zendure2mqtt

[![Open your Home Assistant instance and add this add-on repository.](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2FSukramJ%2Fgo-zendure2mqtt)

A small, dependency-light Go bridge that connects **Zendure** devices
(e.g. the SolarFlow 2400 AC) to a local **MQTT** broker, with **Home Assistant**
auto-discovery. It works **locally** over the device's on-board HTTP API
(the [zenSDK](https://github.com/Zendure/zenSDK) protocol) or via the **Zendure
cloud** — both feed the same normalised MQTT topic tree. Verified end-to-end
against a real SolarFlow 2400 AC.

Twin of [`go-daikin2mqtt`](https://github.com/SukramJ/go-daikin2mqtt) and
[`go-mtec2mqtt`](https://github.com/SukramJ/go-mtec2mqtt); shares their project
setup (pure Go, custom MQTT client, declarative catalog, distroless image).

## Features

- **Two transports, one pipeline** — `local` polls each device's HTTP API
  (`GET /properties/report`); `cloud` streams telemetry over a TLS MQTT session
  to the Zendure cloud broker. Both resolve through the same catalog/coordinator.
- **mqtt-smarthome 2.0 topics** — `zendure/status/…`, `zendure/set/…`,
  `zendure/connected`, `zendure/info` and the maintenance topics, with
  `{"val","ts","lc"}` status objects (see [MQTT topics](#mqtt-topics)).
- **Bidirectional** — `set` commands (from Home Assistant or anything else on
  the broker) are written back to the device, with an immediate re-read so the
  status reflects the change sub-second.
- **Declarative catalog** — [`zendure.yaml`](zendure.yaml) maps raw properties to
  topics/units/HA entities (offset/scale, English/German value maps); adding a
  property is a data change.
- **Home Assistant discovery** — sensors, numbers, selects and switches with
  `default_entity_id` (English, stable) and localized display names; **battery
  packs become their own sub-devices** with rich device-registry info.
- **Virtual charge/discharge switches** — synthetic HA switches that write a mode
  + power-limit property set and derive their state from the report.
- **Diagnostic web UI** — optional read-only embedded SPA over `/api/health` +
  `/api/snapshot` (Home Assistant Ingress-friendly).
- **mDNS discovery** + a diagnostic CLI (`zendure2mqtt-util`).
- **Home Assistant add-on** — installable from this repo (see [addon/](addon/)).
- **Pure Go** — only `gopkg.in/yaml.v3` and `golang.org/x/sync`; `CGO_ENABLED=0`,
  static distroless image.

## Quickstart

```bash
make build              # → bin/zendure2mqtt, bin/zendure2mqtt-util
cp config-template.yaml config.yaml
# local mode: set MQTT_SERVER + LOCAL_DEVICES (SN + HOST)
# cloud mode: set CONNECTION: cloud + CLOUD_APP_TOKEN (from the Zendure app)
./bin/zendure2mqtt --config ./config.yaml
```

Or install the **Home Assistant add-on**: Settings → Add-ons → Add-on Store →
⋮ → Repositories → add `https://github.com/SukramJ/go-zendure2mqtt`, then install
**go-zendure2mqtt**. See [addon/DOCS.md](addon/DOCS.md).

## Transports

| | Local (`connection: local`) | Cloud (`connection: cloud`) |
|---|---|---|
| Reach | device IP on the LAN (`LOCAL_DEVICES`) | Zendure cloud (`CLOUD_APP_TOKEN`) |
| Telemetry | HTTP poll (`REFRESH`) | TLS MQTT stream |
| Control | `POST /properties/write` | MQTT publish |

> The Zendure cloud broker enforces a single session per credential and drops it
> every ~1 s, so cloud mode only trickles telemetry — **local mode is the
> recommended path.** The reconnect is hardened (event-driven, stability-aware
> backoff). The cloud cert is non-standard, so `CLOUD_TLS_VERIFY` defaults off
> (the connection stays TLS-encrypted).

## Diagnostic CLI

```bash
zendure2mqtt-util discover                                   # browse mDNS for devices
zendure2mqtt-util report   --host 192.168.1.50               # dump /properties/report
zendure2mqtt-util resolve  --host 192.168.1.50 --lang de     # catalog-resolved preview
zendure2mqtt-util set      --host 192.168.1.50 --sn SF... --prop acMode --value 2
zendure2mqtt-util cloud-login   --token <app-token>          # test the cloud login
zendure2mqtt-util catalog-check --catalog zendure.yaml       # validate the catalog
```

## Configuration

All scalar keys in [`config-template.yaml`](config-template.yaml) can be
overridden via `ZENDURE_*` environment variables (e.g. `ZENDURE_MQTT_PASSWORD`).
Booleans accept `true`/`false`. With the web UI enabled (`WEB_ENABLE`), a
read-only dashboard is served on `WEB_BIND` (default `127.0.0.1:8080`).

| Key | Default | Meaning |
|---|---|---|
| `MQTT_TOPIC` | `zendure` | The instance name, the first level of every topic. |
| `MQTT_MAINTENANCE` | `true` | Enable the [maintenance topics](#maintenance-topics). |
| `MQTT_STATS_INTERVAL` | `60` | Seconds between `<name>/maintenance/stats` publishes; `0` switches them off. |

> **`MQTT_TOPIC` is the only thing that keeps two instances apart.** Two
> instances of this bridge on one broker (two sites, two cloud accounts) with
> the same name write the same topics and overwrite each other's `connected`
> and `info`; nothing detects it. Give each instance its own name. The name
> must not contain `+` or `#`; a name with `/` is kept but is outside the
> convention (a tool scanning `+/info` will not see the instance, and the
> daemon says so once at start).
>
> The name also namespaces the Home Assistant identities — see
> [Home Assistant identities](#home-assistant-identities) — so changing it
> later re-keys every entity, exactly as it always has.

## MQTT topics

Since 0.10.0 the topics follow the
[mqtt-smarthome 2.0](https://github.com/mqtt-smarthome/mqtt-smarthome/blob/master/SPEC.md)
convention, `<name>/<function>/<item…>` (openccu-loom ADR 0083, shared by all
six projects of this family). `<name>` is `MQTT_TOPIC`, `zendure` unless
configured.

| Old (≤ 0.9.x) | New (0.10.0) |
|---|---|
| `zendure2mqtt/<sn>/<group>/<key>/state` | `zendure/status/<sn>/<group>/<key>` |
| `zendure2mqtt/<sn>/battery/<packSn>/<key>/state` | `zendure/status/<sn>/battery/<packSn>/<key>` |
| `zendure2mqtt/<sn>/<group>/<key>/set` | `zendure/set/<sn>/<group>/<key>` |
| `zendure2mqtt/bridge/status` (`online`/`offline`) | `zendure/connected` (`0`/`1`/`2`) |
| — | `zendure/status/<sn>/online` (`true`/`false`) |
| — | `zendure/info` |
| — | `zendure/maintenance/set/loglevel`, `…/set/restart`, `…/stats` |

`<group>` is `now`, `config`, `static` or `misc` (properties the catalog does
not map yet, published without a Home Assistant entity).

**Status** items are retained, QoS 0, and always a status object:

```json
{"val": 50.39, "ts": 1791273600123, "lc": 1791273480000}
```

`val` is a JSON number in display units (no unit strings), an enum's stable
**token** (`charge`, `discharge`, `persist`, `volatile` — never a localised
label, whatever `LANGUAGE` says; Home Assistant shows the label through its
discovery templates), or a JSON boolean (`charge_active`, `discharge_active`,
`online`). `ts` is when the value was read and `lc` when it last changed, both
in milliseconds. A value is published when it changes and again after every
broker reconnect, not on every poll.

**`<name>/connected`** is retained: `0` from the Last Will and on a graceful
stop, `1` while the bridge is on the broker but its upstream is unusable, `2`
while it is operational. In local mode the upstream is usable while at least
one device is reachable; in cloud mode while the cloud MQTT session is up.
**`<name>/status/<sn>/online`** says whether that one device is reachable:
locally, whether it answers its HTTP API — it goes `false` at the second
failed poll in a row, not the first, and `true` again at the next answer; in
cloud mode every device the cloud lists is `true` and is re-published as soon
as a dropped session is back (the drop itself shows on `<name>/connected`).

**`set`** takes a plain value or `{"val": …}` on the item's own path. Numbers
are clamped to the entity's range and converted to the device's raw units;
enums take the token in any case, a German or English label, or the raw code;
the switches take `true`/`false`, `1`/`0`, `on`/`off` or `yes`/`no`. Empty
and retained messages are ignored, the subscription is QoS 1 (publish at QoS 0
if a duplicate would matter to you), and a rejected or failed request is
logged at `warn` with its topic and payload. A request is not echoed; the
status follows from the device's re-read.

```bash
mosquitto_pub -t zendure/set/SF2400AC0012345/config/ac_mode -m discharge
mosquitto_pub -t zendure/set/SF2400AC0012345/config/input_limit -m '{"val": 1200}'
```

**`<name>/info`** is retained JSON published on every broker connect:
`name` (`go-zendure2mqtt`), `version`, `spec` (`2.0`), `go`, `host`, `pid`,
`started`, `maintenance`, plus `commit`, `build_date` and `connection`.

### Upgrading from 0.9.x

This is a clean break: no compatibility switch publishes the old topics.
Home Assistant users have nothing to do — the discovery documents re-point
every entity to the new topics, and every entity keeps its id, name, area,
history and automations. Anything that reads the raw topics (Node-RED flows,
dashboards, scripts) must move to the new ones and parse `val` out of the
status object; plain `1`/`0` and localised labels are gone from the wire.

On every start the daemon clears what the old layout left retained under the
**old root** — `MQTT_TOPIC` if you configured it, `zendure2mqtt` if you did
not: `<old>/bridge/status` and, for every device serial it knows, exactly the
old `<old>/<sn>/<group>/<key>/state` (and `…/set`) shapes. It never clears by
prefix, never touches another instance's devices, and never touches a topic
whose second level is a function name (`status`, `set`, …).

### Maintenance topics

On by default (`MQTT_MAINTENANCE: false` disables them):

| Topic | Effect |
|---|---|
| `<name>/maintenance/set/loglevel` | `error`, `warn`, `info` or `debug`; changes the daemon's log level until the next start |
| `<name>/maintenance/set/restart` | graceful shutdown (`connected` → `0`), exit 0 — only when a supervisor restarts the process; refused at `warn` otherwise |
| `<name>/maintenance/stats` | retained process statistics every `MQTT_STATS_INTERVAL` seconds |

Whether a supervisor restarts the daemon is answered by
`ZENDURE_SUPERVISED` (`1`/`true` or `0`/`false`) when set, otherwise detected
(systemd, Kubernetes, or a container). A container started without a restart
policy is detected as supervised and would stop for good on a restart — set
`ZENDURE_SUPERVISED=0` there. The Home Assistant add-on sets it to `0`.

> **Security.** Anyone who may publish on the broker can restart the daemon or
> raise its log level through these topics. Give the bridge its own broker
> user with ACLs limited to `<name>/#` and the discovery prefix, and keep
> other clients off `<name>/maintenance/#`; on a broker you cannot secure, set
> `MQTT_MAINTENANCE: false`.

## Home Assistant discovery, and downgrading

### Home Assistant identities

Every entity is available while `<name>/connected` is `2` **and** its unit's
`<name>/status/<sn>/online` is `true` (`availability_mode: all`); a battery
pack follows its unit.

The `unique_id`s, device identifiers and node ids are namespaced with the
**identity root**: the topic root every release before 0.10.0 used —
`MQTT_TOPIC` if you set it, `zendure2mqtt` if you did not. 0.10.0 changed the
*default name* to `zendure` and that moved the topics only, so an instance that
never set `MQTT_TOPIC` publishes under `zendure/…` and keeps its identities
`zendure2mqtt_<sn>_…`. Changing `MQTT_TOPIC` deliberately still re-keys every
entity, as it always has.

### Discovery documents

Discovery uses the device-based format: **one retained document per device**,
at `<HASS_BASE_TOPIC>/device/<node-id>/config`, where the node id is the
device identifier — `<identity-root>_<sn>`, and
`<identity-root>_<sn>_pack_<packSn>` for each battery pack, with the serial in
its original case. Releases up to
0.7.x published one retained config per entity at
`<HASS_BASE_TOPIC>/<platform>/<unique_id>/config`; the first start after
upgrading retracts those and *then* publishes the documents, in that order,
and your entities keep their ids, names, areas, history and automations
because `unique_id` is unchanged.

> **Downgrading to 0.7.x or earlier needs one manual step.** The documents
> stay retained on the broker, and an older release republishing per-entity
> configs is refused by Home Assistant for exactly the same reason,
> symmetrically: one
> `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line
> in *its* log, nothing on the wire, and no entities. Clear every document
> first:
>
> ```bash
> mosquitto_pub -h <broker> -u <user> -P <password> -t <topic> -r -n
> ```
>
> **Take `<topic>` verbatim from the daemon's own `hass.bundle_published`
> log line**, one per device and per pack, rather than composing it — the
> prefix is `HASS_BASE_TOPIC` (default `homeassistant`, but an operator
> setting) and the node id carries the identity root and the serial as the
> device reports it. `-h`/`-u`/`-P` are required on an authenticated broker, which
> the Home Assistant Mosquitto add-on is; drop them only for an anonymous
> broker on localhost. Because nothing was re-keyed, the old release then
> re-adopts the same entities with their history intact.
>
> Downgrading from 0.10.x to 0.9.x needs no discovery step — both publish the
> same documents at the same topics — but the old release publishes the old
> topics again, and the new layout's retained `zendure/…` topics stay until
> cleared (`mosquitto_sub -t 'zendure/#' -v --retained-only` lists them).

See [`changelog.md`](changelog.md) for the full migration note, and
[`addon/DOCS.md`](addon/DOCS.md) for the add-on's copy.

## Development

```bash
go build ./...
go test ./...
make check            # vet + fmt-check + lint + test (needs dev tools, see `make setup`)
```

See [docs/konzept.md](docs/konzept.md) for the architecture and design notes.

Parts of go-zendure2mqtt are developed with agentic AI assistance, primarily
[Claude Code](https://www.anthropic.com/claude-code). Incoming issues are
likewise triaged and analysed with AI help. Every change is still reviewed by
a human maintainer and has to pass the project's test suite before it lands —
AI accelerates the work, it does not replace the review gate. If you want to
contribute — with or without AI assistance — please read the
[AI contribution policy](AI_POLICY.md) first.

## Acknowledgements

This project is primarily based on the official
[**Zendure zenSDK**](https://github.com/Zendure/zenSDK) — Zendure's own
documentation of the local device control protocol (`properties/report` /
`properties/write` and the device API used by the local backend).

The [`Zendure/zendure-ha`](https://github.com/Zendure/zendure-ha) Home Assistant
integration by **peteS-UK** (MIT licensed) was additionally used as a supporting
reference, mainly for the cloud-side details — the signed `deviceList` login
(`HAKEY`, the `SHA1` signature scheme) and the `zenHa` client id. Many thanks to
peteS-UK and the contributors of that project.

The Home Assistant add-on icon (`addon/icon.png`) is the Zendure brand logo,
taken from the [`home-assistant/brands`](https://github.com/home-assistant/brands)
repository (`custom_integrations/zendure_ha/icon.png`). It is a Zendure trademark
and remains the property of Zendure; it is used here only to identify the
supported hardware and is not covered by this project's MIT license.

## License

MIT — see [LICENSE](LICENSE). This project follows the official Zendure
[zenSDK](https://github.com/Zendure/zenSDK) protocol and additionally used
[`Zendure/zendure-ha`](https://github.com/Zendure/zendure-ha) (MIT, Copyright (c)
2024 peteS-UK) as a supporting reference; see [LICENSE](LICENSE) for the upstream
attribution.

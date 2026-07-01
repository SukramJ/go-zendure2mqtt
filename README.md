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
- **Bidirectional** — Home Assistant `…/set` commands are written back to the
  device, with an immediate re-read so the state reflects the change sub-second.
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
AI accelerates the work, it does not replace the review gate.

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

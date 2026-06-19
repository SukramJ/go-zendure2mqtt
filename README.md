# go-zendure2mqtt

A small, dependency-light Go bridge that connects **Zendure** devices
(e.g. the SolarFlow 2400 AC) to a local **MQTT** broker, with optional
**Home Assistant** auto-discovery.

It speaks the device's local HTTP API (the [zenSDK](https://github.com/Zendure/zenSDK)
protocol: `GET /properties/report`, `POST /properties/write`) and — in a later
milestone — the Zendure cloud (REST login + cloud MQTT). Either transport feeds
the same normalised MQTT topic tree.

> **Status: M0 scaffold.** The local HTTP backend, catalog, MQTT publish/command
> path and HA discovery are wired; the cloud transport implements login/device
> discovery but not yet the live MQTT stream (M3). See [docs/konzept.md](docs/konzept.md).

Twin of [`go-daikin2mqtt`](https://github.com/SukramJ/go-daikin2mqtt) and
[`go-mtec2mqtt`](https://github.com/SukramJ/go-mtec2mqtt); shares their project
setup (pure Go, custom MQTT client, declarative catalog, distroless image).

## Features

- **Local transport** — polls each device's HTTP API and publishes normalised state.
- **Bidirectional** — Home Assistant `…/set` commands are written back to the device.
- **Declarative catalog** — [`zendure.yaml`](zendure.yaml) maps raw properties to
  topics/units/HA entities; adding a property is a data change.
- **Home Assistant discovery** — sensors, numbers, selects and switches.
- **Cloud login** — decodes the app token and authenticates against the Zendure
  cloud (device list + broker credentials); telemetry stream lands in M3.
- **Diagnostic CLI** — `zendure2mqtt-util` for one-off report/set/login/catalog checks.
- **Pure Go** — only `gopkg.in/yaml.v3` and `golang.org/x/sync`; `CGO_ENABLED=0`.

## Quickstart

```bash
# build
make build              # → bin/zendure2mqtt, bin/zendure2mqtt-util

# configure
cp config-template.yaml config.yaml   # set MQTT_SERVER and LOCAL_DEVICES

# run
./bin/zendure2mqtt --config ./config.yaml
```

## Diagnostic CLI

```bash
zendure2mqtt-util report       --host 192.168.1.50          # dump /properties/report
zendure2mqtt-util set          --host 192.168.1.50 --sn SF... --prop acMode --value 2
zendure2mqtt-util cloud-login  --token <app-token>          # test the cloud login
zendure2mqtt-util catalog-check --catalog zendure.yaml      # validate the catalog
```

## Configuration

All scalar keys in [`config-template.yaml`](config-template.yaml) can be
overridden via `ZENDURE_*` environment variables (e.g.
`ZENDURE_MQTT_PASSWORD`). Booleans accept `true`/`false`.

## Development

```bash
go build ./...
go test ./...
make check            # vet + fmt-check + lint + test (needs dev tools, see `make setup`)
```

## License

MIT — see [LICENSE](LICENSE).

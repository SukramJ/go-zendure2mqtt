# go-zendure2mqtt — Home Assistant Add-on

This add-on runs the [go-zendure2mqtt](https://github.com/SukramJ/go-zendure2mqtt)
daemon inside Home Assistant. It bridges Zendure devices (e.g. the SolarFlow
2400 AC) to MQTT — locally over the device's on-board HTTP API (zenSDK) or via
the Zendure cloud — with Home Assistant MQTT discovery and a read-only
diagnostic web UI.

## Installation

1. In Home Assistant go to **Settings → Add-ons → Add-on Store**.
2. Click the **⋮** menu (top right) → **Repositories** and add:
   `https://github.com/SukramJ/go-zendure2mqtt`
3. The **go-zendure2mqtt** add-on now appears in the store. Open it and click
   **Install**.
4. Open the **Configuration** tab:
   - Local mode (recommended): set `connection: local` and add your device(s)
     under `local_devices` (`sn` + `host`).
   - Cloud mode: set `connection: cloud` and paste your `cloud_app_token`.
   - Leave `mqtt_server` **empty** to auto-use the Home Assistant MQTT broker;
     set it only to target a different broker.
5. **Start** the add-on. Entities appear automatically via MQTT discovery
   (`hass_enable` is on by default).

## Diagnostic web UI

After starting, open the add-on's **Web UI** (the side-panel icon, or the
**Open Web UI** button). It is served through Home Assistant Ingress — no port
needs to be exposed — and shows a live, read-only snapshot of every device's
values plus bridge/MQTT health.

## Images

The add-on uses pre-built multi-arch images
(`ghcr.io/sukramj/go-zendure2mqtt-addon-{arch}`, built by
`.github/workflows/addon-image.yml`). These are HA base images carrying the
compiled binary + `script/run.sh`, which maps the add-on options onto the
daemon's `ZENDURE_*` configuration. They are distinct from the distroless
daemon image (`ghcr.io/sukramj/go-zendure2mqtt`), which runs the binary
directly and is not a valid add-on image. To build locally from
`addon/Dockerfile` instead, remove the `image:` key from `addon/config.yaml`.

See [DOCS.md](DOCS.md) for the full options reference.

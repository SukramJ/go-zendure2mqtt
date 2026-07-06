# go-zendure2mqtt add-on

## Quickstart

For a standard Home Assistant install with the Mosquitto broker:

1. Choose the transport with **`connection`**:
   - **`local`** (recommended) — add your device(s) under **`local_devices`**
     with their serial (`sn`) and IP/host (`host`). The add-on polls each device
     over its on-board HTTP API (zenSDK). Enable local control in the Zendure app
     if needed.
   - **`cloud`** — paste the base64 **`cloud_app_token`** from the Zendure app.
     (Note: the Zendure cloud drops the session frequently, so telemetry is
     intermittent — local is preferred.)
2. Leave **`mqtt_server` empty** — the add-on auto-connects to the Home
   Assistant MQTT broker (like zigbee2mqtt), and `hass_enable` is on by default,
   so entities appear automatically via MQTT discovery.
3. **Start** the add-on, then open its **Web UI** (side-panel icon) to see the
   live diagnostic snapshot.

## Options reference

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `connection` | list(local\|cloud) | `local` | Transport: poll devices locally over HTTP, or stream from the Zendure cloud. |
| `refresh` | int | `15` | Local HTTP poll interval (seconds). |
| `local_devices` | list | `[]` | Devices to poll in local mode. Each entry: `sn` (serial), `host` (IP/hostname), optional `device_name` (friendly name that replaces the serial in Home Assistant device names and entity_ids), optional `model`. |
| `cloud_app_token` | password | `""` | Base64 app token from the Zendure app (cloud mode). Decodes to `<api_url>.<appKey>`. |
| `cloud_tls_verify` | bool | `false` | Enforce strict TLS certificate verification for the cloud broker. The Zendure cloud cert is non-standard, so this is off by default (the connection stays TLS-encrypted). |
| `mqtt_server` | str | `""` | MQTT broker host. **Leave empty** to auto-use the Home Assistant MQTT broker. Set only to target a different broker. |
| `mqtt_port` | int | `1883` | MQTT broker port. Only used when `mqtt_server` is set. |
| `mqtt_login` | str | `""` | MQTT username. Only used when `mqtt_server` is set. |
| `mqtt_password` | password | `""` | MQTT password. Only used when `mqtt_server` is set. |
| `mqtt_topic` | str | `zendure2mqtt` | Base MQTT topic for published device state. |
| `hass_enable` | bool | `true` | Publish Home Assistant MQTT discovery so entities appear automatically. |
| `language` | list(en\|de) | `en` | Display-name language (topics/entity_ids stay language-independent). |
| `web_enable` | bool | `true` | Enable the read-only diagnostic web UI (served via Ingress). |
| `charge_active_value` | int | `1200` | AC charge power limit (W) written when the "Charge active" switch is turned on. |
| `discharge_active_value` | int | `1200` | AC discharge power limit (W) written when the "Discharge active" switch is turned on. |
| `debug` | bool | `false` | Verbose logging. |

## Topics

State is published under `<mqtt_topic>/<sn>/<group>/<key>/state`, battery packs
under `<mqtt_topic>/<sn>/battery/<packSn>/<key>/state`, and writable entities
listen on `…/set`. Home Assistant discovery configs are published under
`homeassistant/<platform>/<unique_id>/config`.

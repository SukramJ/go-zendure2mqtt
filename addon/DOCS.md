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
listen on `…/set`. Home Assistant discovery uses the device-based format: one
retained document per device under `<hass_base>/device/<node_id>/config`, where
`<hass_base>` is the discovery prefix (`homeassistant` for the add-on, which
does not expose it as an option) and the node id is the device identifier
(`<mqtt_topic>_<sn>`, and `<mqtt_topic>_<sn>_pack_<packSn>` for each battery
pack) — the serial in its original case.

Earlier releases published one retained config per entity under
`<hass_base>/<platform>/<unique_id>/config`. The first start after
upgrading retracts those and then publishes the documents, in that order.
Your entities keep their entity ids, names, icons, areas, history and
automations, because `unique_id` is unchanged.

### Downgrading to 0.7.x or earlier

The retained device documents stay on the broker, and an older release
republishing per-entity configs is refused by Home Assistant for exactly the
same reason, symmetrically — one
`WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line in
Home Assistant's log and no entities. Clear every document first, one per
device and per battery pack:

```bash
mosquitto_pub -h <broker> -u <user> -P <password> -t <topic> -r -n
```

**Take `<topic>` verbatim from the add-on log's `hass.bundle_published`
lines** rather than composing it. `-h`, `-u` and `-P` are not optional here:
the add-on publishes to the Supervisor's MQTT broker, which is authenticated,
and `mosquitto_pub` without credentials is simply refused. The broker host,
username and password are the ones the *Mosquitto broker* add-on shows, or the
`mqtt_server`/`mqtt_login`/`mqtt_password` options if you set them.

Because nothing was re-keyed, the old release then re-adopts the same entities
with their history intact. See the changelog for the full note.

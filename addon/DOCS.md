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
| `mqtt_topic` | str | *(empty)* | The instance name, the first level of every topic. Empty means `zendure`. **It is the only thing that keeps two instances apart**: two bridges on one broker need different names. Changing it later re-keys every Home Assistant entity. Installs from before 0.10.0 keep their saved `zendure2mqtt`. |
| `mqtt_maintenance` | bool | `true` | Enable the maintenance topics (`<name>/maintenance/…`: log level, stats). See the security note below. |
| `mqtt_stats_interval` | int | `60` | Seconds between `<name>/maintenance/stats` publishes; `0` switches them off. |
| `hass_enable` | bool | `true` | Publish Home Assistant MQTT discovery so entities appear automatically. |
| `language` | list(en\|de) | `en` | Display-name language (topics/entity_ids stay language-independent). |
| `web_enable` | bool | `true` | Enable the read-only diagnostic web UI (served via Ingress). |
| `charge_active_value` | int | `1200` | AC charge power limit (W) written when the "Charge active" switch is turned on. |
| `discharge_active_value` | int | `1200` | AC discharge power limit (W) written when the "Discharge active" switch is turned on. |
| `debug` | bool | `false` | Verbose logging. |

## Topics

Since 0.10.0 the topics follow the mqtt-smarthome 2.0 convention,
`<name>/<function>/<item…>`, where `<name>` is `mqtt_topic` (`zendure` when
empty):

| Before 0.10.0 | Since 0.10.0 |
|---|---|
| `zendure2mqtt/<sn>/<group>/<key>/state` | `<name>/status/<sn>/<group>/<key>` |
| `zendure2mqtt/<sn>/battery/<packSn>/<key>/state` | `<name>/status/<sn>/battery/<packSn>/<key>` |
| `zendure2mqtt/<sn>/<group>/<key>/set` | `<name>/set/<sn>/<group>/<key>` |
| `zendure2mqtt/bridge/status` (`online`/`offline`) | `<name>/connected` (`0` stopped, `1` no device reachable, `2` operational) |
| — | `<name>/status/<sn>/online` (`true`/`false`), `<name>/info` |

Every status value is a JSON object, `{"val": 50.39, "ts": …, "lc": …}`:
numbers as numbers, the switches as `true`/`false`, and the AC and smart modes
as their English token (`charge`, `volatile`, …) whatever `language` says —
Home Assistant shows the German label itself. `set` takes a plain value or
`{"val": …}`; empty and retained messages are ignored.

**Home Assistant needs nothing from you**: the discovery documents point every
entity at the new topics and the entities keep their ids, names, areas and
history. An entity is available while `<name>/connected` is `2` and its
device's `online` item is `true`. Automations or dashboards that read the raw
MQTT topics must move to the new ones. On start the add-on clears the old
layout's retained topics under the old root (`mqtt_topic` if set, otherwise
`zendure2mqtt`), only for the devices it knows and only in their exact old
shape.

### Maintenance topics

`<name>/maintenance/set/loglevel` (`error`/`warn`/`info`/`debug`) changes the
log level until the next start, and `<name>/maintenance/stats` carries process
statistics. `<name>/maintenance/set/restart` is refused in the add-on — the
Supervisor does not restart an add-on that exits cleanly; restart it from Home
Assistant instead.

> **Security:** anyone who may publish on your broker can change the log
> level. Use the broker's ACLs to keep other clients off
> `<name>/maintenance/#`, or switch `mqtt_maintenance` off.

### Discovery

Home Assistant discovery uses the device-based format: one
retained document per device under `<hass_base>/device/<node_id>/config`, where
`<hass_base>` is the discovery prefix (`homeassistant` for the add-on, which
does not expose it as an option) and the node id is the device identifier
(`zendure2mqtt_<sn>`, and `zendure2mqtt_<sn>_pack_<packSn>` for each battery
pack, or your `mqtt_topic` in place of `zendure2mqtt` if you set one) — the
serial in its original case.

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

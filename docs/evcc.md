# evcc

go-zendure2mqtt exposes the **shared evcc profile** — one MQTT surface that
go-zendure2mqtt, [go-keba2mqtt](https://github.com/SukramJ/go-keba2mqtt) and
[go-mtec2mqtt](https://github.com/SukramJ/go-mtec2mqtt) implement identically,
so an `evcc.yaml` reads the same for every device. evcc reads a Zendure unit as
a `custom` battery meter (and PV meter) over MQTT and controls the battery
through evcc's battery modes; the bridge stays the unit's only client.

Enable it with `EVCC_ENABLE: true` (env `ZENDURE_EVCC_ENABLE`, add-on option
`evcc_enable`). Works the same for the `local` and the `cloud` transport.

## Topics

```
<name>/status/<sn>/evcc/<role>/<key>    {"val": …, "ts": …, "lc": …}   retained
<name>/set/<sn>/evcc/<role>/<key>       plain value (or {"val": …})
```

`<name>` is `MQTT_TOPIC` (`zendure`), `<sn>` the unit's serial number (the
`SN` of `LOCAL_DEVICES`, also on the Home Assistant device page). Every unit
has its own tree, so several SolarFlows are several evcc batteries. evcc reads
every status value with `jq: .val`.

| Status item | Unit | Meaning |
| --- | --- | --- |
| `battery/power` | W | `packInputPower − outputPackPower`: positive while discharging, negative while charging |
| `battery/soc` | % | `electricLevel` |
| `battery/batterymode` | — | `normal` · `hold` · `charge`, the mode the bridge last applied (`normal` at start) |
| `pv/power` | W | `solarInputPower`, only on units that report solar inputs |

Zendure units report no energy counters, so the profile's optional `energy`
and `returnenergy` are not published.

| Command item | Payload | Effect |
| --- | --- | --- |
| `battery/batterymode` | `1` / `2` / `3` (what evcc sends) or `normal` / `hold` / `charge` | see below |

### Battery modes

| Mode | Writes |
| --- | --- |
| `hold` | `outputLimit` 0 — no discharge; charging stays as it was |
| `charge` | `acMode` 1 (input), `inputLimit` = the unit's `chargeMaxLimit` (or `CHARGE_ACTIVE_VALUE` when it reports none), `outputLimit` 0 |
| `normal` | `acMode`, `inputLimit`, `outputLimit` and `smartMode` exactly as they were before the first `hold`/`charge` |

`hold` and `charge` also write `smartMode` 1, so evcc's frequent changes are
not written to the unit's flash; `normal` restores the previous `smartMode`
with the rest. evcc's `holdcharge` (4) and `discharge` (5) are refused with a
warning.

Only what differs is written: a mode evcc re-sends unchanged costs no write,
and a unit that drifted (a change in the Zendure app) is put back on the next
re-send.

**Restart caveat:** the settings `normal` restores are kept in memory. If the
bridge restarts while `hold` or `charge` is active, it starts in `normal`
without them, and a later `normal` from evcc writes nothing — set the unit's
limits once by hand (Home Assistant or the Zendure app) in that case.

`EVCC_BATTERYMODE_TIMEOUT` (seconds, default 0 = off; add-on option
`evcc_batterymode_timeout`) returns the unit to `normal` when a `hold` or
`charge` stands that long without evcc sending a battery mode again, so an
evcc that stopped never leaves the battery locked. evcc sends a mode only when
it changes; wrap the setter in evcc's `watchdog` (below) so it repeats, and set
the timeout to at least twice the watchdog's, e.g. `120`.

## evcc.yaml

Replace `HOA1NAH9KT2N0J` with your unit's serial and `zendure` with your
`MQTT_TOPIC` if you changed it. Leave out the `pv` meter on units without
solar inputs.

```yaml
meters:
  - name: zendure_battery
    type: custom
    power:
      source: mqtt
      topic: zendure/status/HOA1NAH9KT2N0J/evcc/battery/power
      jq: .val
    soc:
      source: mqtt
      topic: zendure/status/HOA1NAH9KT2N0J/evcc/battery/soc
      jq: .val
    batterymode:
      source: watchdog
      timeout: 60s
      reset: ["1"]
      set:
        source: mqtt
        topic: zendure/set/HOA1NAH9KT2N0J/evcc/battery/batterymode
    capacity: 2.88 # kWh, the capacity of your packs
  - name: zendure_pv
    type: custom
    power:
      source: mqtt
      topic: zendure/status/HOA1NAH9KT2N0J/evcc/pv/power
      jq: .val

site:
  title: Home
  meters:
    battery:
      - zendure_battery
    pv:
      - zendure_pv
```

## Freshness and failure

- The bridge publishes a value only when it changes, so a getter without new
  messages is not stale — do **not** put a `timeout:` on the getters, or evcc
  reports the meter as outdated while the battery sits idle.
- Reachability is `zendure/connected`: `2` while the bridge reaches the units,
  `1` when it does not, `0` when the bridge is down; each unit's own
  `zendure/status/<sn>/online` is `true` while it answers.

## The shared profile

The same contract holds for all three bridges (SukramJ/go-mtec2mqtt#75,
SukramJ/go-zendure2mqtt#66, go-keba2mqtt as the charger reference): the `evcc`
item group, evcc's units and signs (power positive = import / production /
discharge / consumption), `EVCC_ENABLE` off by default, idempotent commands,
and a `docs/evcc.md` whose snippet is checked against the topics the bridge
really publishes (`internal/evcc/docs_test.go`). Devices with several roles
nest them one level below `evcc` (`evcc/battery/soc`, `evcc/pv/power`).

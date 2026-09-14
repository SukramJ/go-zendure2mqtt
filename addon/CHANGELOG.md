# Changelog

Changes to the **go-zendure2mqtt** Home Assistant add-on. The add-on version
tracks the project release; see the project
[changelog.md](https://github.com/SukramJ/go-zendure2mqtt/blob/main/changelog.md)
for the full daemon details.

## 0.8.0

- **Breaking (discovery format): Home Assistant discovery is now one retained
  *device document* per device instead of 29 per-entity configs.** The
  add-on retracts the old configs on first start and then publishes one
  document per unit and per battery pack. Nothing for you to do, and nothing
  is lost: `unique_id` is unchanged, so every entity keeps its id, name,
  area, icon, history and automations. **Home Assistant 2024.11 or newer is
  required from this release on.**
- **Rolling back to 0.7.x needs one manual step.** The retained device
  documents stay on the broker and would make an older add-on's per-entity
  configs be refused by Home Assistant — one
  `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line
  in its log and no entities. Clear each document first with
  `mosquitto_pub -h <broker> -u <user> -P <password> -t <topic> -r -n`,
  taking `<topic>` **verbatim from the add-on log's new
  `hass.bundle_published` lines** rather than composing it: the prefix is
  your `HASS_BASE_TOPIC` (default `homeassistant`, but an option), not a
  fixed `homeassistant/`. See DOCS.md.
- **Fixed: after an MQTT reconnect most entities could silently fail to
  appear.** The retractions were remembered as done per process rather than
  per connection, so a reconnect re-sent only 7 of 29 of them and published
  the device documents anyway — leaving 22 of 29 entities missing until the
  add-on was restarted. Fixed; no option changes.
- New `hass.bundle_published` log line, one per retained device document, so
  the exact topic can be copied rather than composed.

## 0.7.0

- Dependency update: MQTT client library bumped to v1.3.0 (upstream audit
  release — 42 fixes across concurrency, decoder robustness, and spec
  conformance). The output broker's reconnect now waits briefly before
  retrying if the connection drops right after connecting, instead of
  hammering the broker immediately. No add-on option changes; no action
  required.

## 0.6.1

- Dependency update: MQTT client library bumped to v1.2.0 (upstream hardening
  release — more robust reconnect handling, stricter protocol parsing). No
  add-on option changes; no action required.

## 0.6.0

- Codebase hardening pass covering untrusted-input parsing, HTTP client/server
  robustness, and concurrency. Highlights: the mDNS discovery parser can no
  longer be hung by a crafted LAN packet; device/cloud HTTP responses are now
  size-capped so a misbehaving peer cannot exhaust memory; the diagnostic web
  UI gained read/write/idle connection timeouts; a fatal map-race in cloud mode
  is fixed; inbound `/set` writes no longer block the MQTT read loop; and `/set`
  numeric commands are clamped to their advertised min/max (`NaN`/`Inf` and
  out-of-range values are rejected instead of reaching the hardware).
- Transient cloud-login and startup-subscription failures now retry instead of
  leaving the add-on running but idle until a manual restart.
- Home Assistant discovery no longer permanently deletes live entities when a
  device sends a transiently incomplete report.
- **Breaking (config):** `charge_active_value` / `discharge_active_value` no
  longer accept `0` (a `0` W limit was a silent no-op that got rewritten to the
  1200 W default). The valid range is now `1..2400`; leave the option unset for
  the 1200 W default.

## 0.4.0

- MQTT publishes to the output broker are now circuit-protected: when the
  broker stops acknowledging (link up, acks missing), publishes fail fast and
  a periodic probe tests recovery instead of every publish stalling on the
  ack timeout. State transitions appear as `zendure2mqtt.mqtt_breaker_state`
  warnings in the add-on log. Command subscriptions are unaffected.

## 0.1.4

- MQTT half-open connections are now detected and recovered. A broker or network
  drop without a TCP FIN/RST (e.g. a Mosquitto or Home Assistant restart) used to
  leave the read loop blocked in `ReadFrame` forever with no reconnect, and
  publishes timed out with `context deadline exceeded` until a manual restart. A
  PINGRESP watchdog now declares the connection lost when a keep-alive ping goes
  unanswered, so the existing reconnect logic re-dials automatically.

## 0.1.3

- Home Assistant discovery orphan cleanup: entities a newer release no longer
  publishes are cleared from the broker instead of lingering as "unavailable".
  Other integrations' and other devices' configs are never touched.

## 0.1.2

- Internal code-quality cleanup only (linter findings). No functional changes to
  the add-on.

## 0.1.1

- Add an add-on icon (the Zendure brand logo, sourced from `home-assistant/brands`)
  shown in the add-on store and the sidebar.
- CI supply-chain hardening and dependency bumps. No functional changes to the
  add-on itself.

## 0.1.0

- Initial release. Run the go-zendure2mqtt bridge as a Home Assistant add-on:
  bridges Zendure devices (e.g. the SolarFlow 2400 AC) to MQTT with Home
  Assistant auto-discovery — locally over the device's on-board HTTP API (zenSDK)
  or via the Zendure cloud — with an optional read-only diagnostic Web UI.

# Changelog

Changes to the **go-zendure2mqtt** Home Assistant add-on. The add-on version
tracks the project release; see the project
[changelog.md](https://github.com/SukramJ/go-zendure2mqtt/blob/main/changelog.md)
for the full daemon details.

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

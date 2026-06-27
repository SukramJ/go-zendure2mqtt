# Changelog

Changes to the **go-zendure2mqtt** Home Assistant add-on. The add-on version
tracks the project release; see the project
[changelog.md](https://github.com/SukramJ/go-zendure2mqtt/blob/main/changelog.md)
for the full daemon details.

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

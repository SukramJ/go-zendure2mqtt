// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package virtual defines synthetic on/off switches that have no single
// backing property. Toggling one writes a whole set of device properties
// (mode + power limit), and its state is derived from the latest report —
// the same pattern as go-mtec2mqtt's charge/discharge "active" switches.
package virtual

import "github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"

// Switch is a synthetic HA switch backed by a property-set write.
type Switch struct {
	Topic    string // MQTT topic leaf, e.g. "charge_active"
	Name     string
	NameDE   string
	onProps  map[string]any
	offProps map[string]any
	active   func(*model.Report) bool
}

// FriendlyName returns the localised display name.
func (s Switch) FriendlyName(lang string) string {
	if lang == "de" && s.NameDE != "" {
		return s.NameDE
	}
	return s.Name
}

// State returns "1"/"0" derived from the report (HA switch payload_on/off).
func (s Switch) State(r *model.Report) string {
	if s.active(r) {
		return "1"
	}
	return "0"
}

// WriteProps returns the property set to write for the requested state.
func (s Switch) WriteProps(on bool) map[string]any {
	if on {
		return s.onProps
	}
	return s.offProps
}

// Switches builds the charge/discharge switches. chargeW/dischargeW are the
// AC power limits (W) written when the respective switch is turned on.
func Switches(chargeW, dischargeW int) []Switch {
	return []Switch{
		{
			Topic: "charge_active", Name: "Charge active", NameDE: "Laden aktiv",
			onProps:  map[string]any{"smartMode": 1, "acMode": 1, "inputLimit": chargeW, "outputLimit": 0},
			offProps: map[string]any{"smartMode": 0, "acMode": 1, "inputLimit": 0, "outputLimit": 0},
			active:   func(r *model.Report) bool { return propInt(r, "acMode") == 1 && propInt(r, "inputLimit") > 0 },
		},
		{
			Topic: "discharge_active", Name: "Discharge active", NameDE: "Entladen aktiv",
			onProps:  map[string]any{"smartMode": 1, "acMode": 2, "outputLimit": dischargeW, "inputLimit": 0},
			offProps: map[string]any{"smartMode": 0, "acMode": 2, "outputLimit": 0, "inputLimit": 0},
			active:   func(r *model.Report) bool { return propInt(r, "acMode") == 2 && propInt(r, "outputLimit") > 0 },
		},
	}
}

// propInt reads an integer property from a report (0 when absent).
func propInt(r *model.Report, key string) int {
	if r == nil {
		return 0
	}
	if v, ok := r.Properties[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return 0
}

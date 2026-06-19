// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package virtual_test

import (
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/virtual"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

func find(t *testing.T, sws []virtual.Switch, topic string) virtual.Switch {
	t.Helper()
	for _, s := range sws {
		if s.Topic == topic {
			return s
		}
	}
	t.Fatalf("switch %q not found", topic)
	return virtual.Switch{}
}

func TestSwitchStateDerivation(t *testing.T) {
	sws := virtual.Switches(1500, 800)
	charge := find(t, sws, "charge_active")
	discharge := find(t, sws, "discharge_active")

	charging := &model.Report{Properties: map[string]any{
		"acMode": float64(1), "inputLimit": float64(2200), "outputLimit": float64(0),
	}}
	if charge.State(charging) != "1" {
		t.Errorf("charge state while charging = %q, want 1", charge.State(charging))
	}
	if discharge.State(charging) != "0" {
		t.Errorf("discharge state while charging = %q, want 0", discharge.State(charging))
	}

	discharging := &model.Report{Properties: map[string]any{
		"acMode": float64(2), "inputLimit": float64(0), "outputLimit": float64(900),
	}}
	if discharge.State(discharging) != "1" {
		t.Errorf("discharge state while discharging = %q, want 1", discharge.State(discharging))
	}
	if charge.State(discharging) != "0" {
		t.Errorf("charge state while discharging = %q, want 0", charge.State(discharging))
	}
}

func TestSwitchWriteProps(t *testing.T) {
	sws := virtual.Switches(1500, 800)
	charge := find(t, sws, "charge_active")
	discharge := find(t, sws, "discharge_active")

	on := charge.WriteProps(true)
	if on["acMode"] != 1 || on["inputLimit"] != 1500 || on["outputLimit"] != 0 {
		t.Errorf("charge ON props = %v", on)
	}
	if off := charge.WriteProps(false); off["inputLimit"] != 0 {
		t.Errorf("charge OFF inputLimit = %v, want 0", off["inputLimit"])
	}
	don := discharge.WriteProps(true)
	if don["acMode"] != 2 || don["outputLimit"] != 800 || don["inputLimit"] != 0 {
		t.Errorf("discharge ON props = %v", don)
	}
}

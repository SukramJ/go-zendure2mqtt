// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package evcc_test

import (
	"errors"
	"maps"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/evcc"
)

func values(vs []evcc.Value) map[evcc.Item]any {
	out := map[evcc.Item]any{}
	for _, v := range vs {
		out[v.Item] = v.Value
	}
	return out
}

// Battery power is evcc's: positive while discharging, negative while
// charging.
func TestDeriveSigns(t *testing.T) {
	t.Parallel()
	got := values(evcc.Derive(map[string]any{
		"packInputPower": float64(800), "outputPackPower": float64(0),
		"electricLevel": float64(57), "solarInputPower": float64(320),
	}))
	want := map[evcc.Item]any{
		{Role: evcc.RoleBattery, Key: evcc.KeyPower}: float64(800),
		{Role: evcc.RoleBattery, Key: evcc.KeySoC}:   float64(57),
		{Role: evcc.RolePV, Key: evcc.KeyPower}:      float64(320),
	}
	if !maps.Equal(got, want) {
		t.Fatalf("discharging: %v, want %v", got, want)
	}

	got = values(evcc.Derive(map[string]any{"packInputPower": float64(0), "outputPackPower": float64(1200)}))
	if p := got[evcc.Item{Role: evcc.RoleBattery, Key: evcc.KeyPower}]; p != float64(-1200) {
		t.Fatalf("charging: power %v, want -1200", p)
	}
}

// A partial report publishes nothing it would have to guess.
func TestDerivePartial(t *testing.T) {
	t.Parallel()
	got := values(evcc.Derive(map[string]any{"packInputPower": float64(100)}))
	if len(got) != 0 {
		t.Fatalf("partial report derived %v", got)
	}
}

func TestParseMode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]evcc.Mode{
		"1": evcc.ModeNormal, "2": evcc.ModeHold, "3": evcc.ModeCharge,
		"normal": evcc.ModeNormal, "HOLD": evcc.ModeHold, "\t2\n": evcc.ModeHold, "Charge": evcc.ModeCharge,
	} {
		if got, err := evcc.ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"4", "5", "holdcharge", "discharge", "", "on"} {
		if _, err := evcc.ParseMode(in); !errors.Is(err, evcc.ErrMode) {
			t.Errorf("ParseMode(%q) err = %v, want ErrMode", in, err)
		}
	}
}

func TestTargets(t *testing.T) {
	t.Parallel()
	props := map[string]any{"chargeMaxLimit": float64(2400)}
	if got, want := evcc.Target(evcc.ModeHold, props, 1200), map[string]any{"smartMode": 1, "outputLimit": 0}; !maps.Equal(got, want) {
		t.Errorf("hold = %v, want %v", got, want)
	}
	want := map[string]any{"smartMode": 1, "acMode": 1, "inputLimit": 2400, "outputLimit": 0}
	if got := evcc.Target(evcc.ModeCharge, props, 1200); !maps.Equal(got, want) {
		t.Errorf("charge = %v, want %v", got, want)
	}
	// Without chargeMaxLimit, the configured charge power.
	if got := evcc.Target(evcc.ModeCharge, nil, 1200); got["inputLimit"] != 1200 {
		t.Errorf("charge fallback inputLimit = %v, want 1200", got["inputLimit"])
	}
	if got := evcc.Target(evcc.ModeNormal, props, 1200); got != nil {
		t.Errorf("normal has a target: %v", got)
	}
}

func TestSnapshotAndDiff(t *testing.T) {
	t.Parallel()
	props := map[string]any{
		"acMode": float64(2), "inputLimit": float64(0), "outputLimit": float64(800),
		"smartMode": float64(0), "electricLevel": float64(40),
	}
	snap := evcc.Snapshot(props)
	if want := map[string]any{"acMode": 2, "inputLimit": 0, "outputLimit": 800, "smartMode": 0}; !maps.Equal(snap, want) {
		t.Fatalf("snapshot %v, want %v", snap, want)
	}
	if d := evcc.Diff(snap, props); d != nil {
		t.Fatalf("diff of a state against itself = %v", d)
	}
	hold := evcc.Target(evcc.ModeHold, props, 1200)
	if d, want := evcc.Diff(hold, props), map[string]any{"smartMode": 1, "outputLimit": 0}; !maps.Equal(d, want) {
		t.Fatalf("hold diff %v, want %v", d, want)
	}
	// A property the unit never reported is not part of the snapshot.
	if snap := evcc.Snapshot(map[string]any{"acMode": float64(1)}); len(snap) != 1 {
		t.Fatalf("snapshot of a partial report: %v", snap)
	}
}

// Every status item has a display name in both languages for the web UI.
func TestItemNames(t *testing.T) {
	for _, it := range evcc.StatusItems() {
		en, de := it.Name("en"), it.Name("de")
		if en == it.String() || de == it.String() || en == de {
			t.Errorf("%s: names en=%q de=%q", it, en, de)
		}
	}
	if got := (evcc.Item{Role: evcc.RoleBattery, Key: evcc.KeyPower}).Unit(); got != "W" {
		t.Errorf("battery/power unit = %q, want W", got)
	}
}

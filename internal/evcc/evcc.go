// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package evcc is this bridge's side of the shared evcc profile — the one
// MQTT surface go-keba2mqtt, go-mtec2mqtt and go-zendure2mqtt expose for
// evcc (https://evcc.io), so an evcc.yaml reads the same for each:
//
//	<name>/status/<sn>/evcc/<role>/<key>   {"val": …}  (evcc: `jq: .val`)
//	<name>/set/<sn>/evcc/<role>/<key>      plain value or {"val": …}
//
// A Zendure unit has two roles: `battery` (power, soc, batterymode) and `pv`
// (power, on units with solar inputs). Units and signs are evcc's: W and %,
// battery power positive while discharging, PV power positive while
// producing. The units report no energy counters, so the profile's optional
// `energy` / `returnenergy` keys are not published.
//
// The package is pure: it derives the profile values from a unit's raw
// properties and plans the property writes of a battery mode. The
// coordinator owns the I/O.
package evcc

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Group is the item group of the profile, the level below the serial.
const Group = "evcc"

// Roles.
const (
	RoleBattery = "battery"
	RolePV      = "pv"
)

// Keys.
const (
	KeyPower       = "power"
	KeySoC         = "soc"
	KeyBatteryMode = "batterymode"
)

// Item is one profile item, `<role>/<key>`.
type Item struct {
	Role, Key string
}

// labels are the items' display names (English, German) and units, for the
// diagnostic web UI.
var labels = map[Item][3]string{
	{RoleBattery, KeyPower}:       {"Battery power", "Batterieleistung", "W"},
	{RoleBattery, KeySoC}:         {"Battery state of charge", "Batterie-Ladestand", "%"},
	{RoleBattery, KeyBatteryMode}: {"Battery mode", "Batteriemodus", ""},
	{RolePV, KeyPower}:            {"PV power", "PV-Leistung", "W"},
}

// String is the item's path below [Group], `<role>/<key>`.
func (i Item) String() string { return i.Role + "/" + i.Key }

// Name is the item's display name in lang ("de" or anything else →
// English); the path for an item without one.
func (i Item) Name(lang string) string {
	l, ok := labels[i]
	switch {
	case !ok:
		return i.String()
	case lang == "de":
		return l[1]
	}
	return l[0]
}

// Unit is the item's unit, "" for none.
func (i Item) Unit() string { return labels[i][2] }

// StatusItems lists every item the profile can publish, for the self-echo
// guard and the documentation contract test.
func StatusItems() []Item {
	return []Item{
		{RoleBattery, KeyPower},
		{RoleBattery, KeySoC},
		{RoleBattery, KeyBatteryMode},
		{RolePV, KeyPower},
	}
}

// Commands lists every routed command item.
func Commands() []Item {
	return []Item{{RoleBattery, KeyBatteryMode}}
}

// Raw property names the profile reads and writes.
const (
	propPackInput   = "packInputPower"  // battery → unit, W: discharging
	propOutputPack  = "outputPackPower" // unit → battery, W: charging
	propLevel       = "electricLevel"   // %
	propSolarInput  = "solarInputPower" // W
	propChargeLimit = "chargeMaxLimit"  // W
	propACMode      = "acMode"          // 1 input (charge), 2 output (discharge)
	propInputLimit  = "inputLimit"      // W
	propOutputLimit = "outputLimit"     // W
	propSmartMode   = "smartMode"       // 1: not persisted to flash
)

// Value is one derived profile value.
type Value struct {
	Item
	Value any
}

// Derive computes the profile values present in props, a unit's raw
// properties. A value is returned only when every input it needs is present,
// so a partial report never publishes a guess.
func Derive(props map[string]any) []Value {
	var out []Value
	in, okIn := number(props, propPackInput)
	charge, okCharge := number(props, propOutputPack)
	if okIn && okCharge {
		out = append(out, Value{Item{RoleBattery, KeyPower}, in - charge})
	}
	if v, ok := number(props, propLevel); ok {
		out = append(out, Value{Item{RoleBattery, KeySoC}, v})
	}
	if v, ok := number(props, propSolarInput); ok {
		out = append(out, Value{Item{RolePV, KeyPower}, v})
	}
	return out
}

// Mode is an evcc battery mode.
type Mode string

// The battery modes the profile supports.
const (
	ModeNormal Mode = "normal"
	ModeHold   Mode = "hold"
	ModeCharge Mode = "charge"
)

// ErrMode is returned for a batterymode payload the profile does not take.
var ErrMode = errors.New("evcc: unsupported batterymode")

// ParseMode reads a batterymode command: evcc's own integers (1 normal,
// 2 hold, 3 charge — what its custom meter sends) or the tokens, in any case.
// evcc's holdcharge (4) and discharge (5) are refused.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", string(ModeNormal):
		return ModeNormal, nil
	case "2", string(ModeHold):
		return ModeHold, nil
	case "3", string(ModeCharge):
		return ModeCharge, nil
	}
	return "", fmt.Errorf("%w: %q (want 1/normal, 2/hold or 3/charge)", ErrMode, s)
}

// controlled are the properties a battery mode may write; their values before
// the first hold/charge are what `normal` restores.
var controlled = []string{propACMode, propInputLimit, propOutputLimit, propSmartMode}

// Target is the property set a mode asks for. chargeW is the input limit
// `charge` writes when the unit does not report chargeMaxLimit.
//
//   - hold: outputLimit 0 — the battery no longer discharges, charging from
//     surplus stays as it was.
//   - charge: acMode 1 (input) at the unit's chargeMaxLimit, outputLimit 0.
//
// Both write smartMode 1 so frequent evcc changes are not persisted to flash;
// `normal` has no target of its own, it restores what was saved.
func Target(m Mode, props map[string]any, chargeW int) map[string]any {
	switch m {
	case ModeHold:
		return map[string]any{propSmartMode: 1, propOutputLimit: 0}
	case ModeCharge:
		limit := chargeW
		if v, ok := number(props, propChargeLimit); ok && v > 0 {
			limit = int(math.Round(v))
		}
		return map[string]any{propSmartMode: 1, propACMode: 1, propInputLimit: limit, propOutputLimit: 0}
	case ModeNormal:
	}
	return nil
}

// Snapshot returns the controlled properties as props reports them, the
// state `normal` restores. A property the unit has not reported is left out:
// it cannot be restored to a value nobody saw.
func Snapshot(props map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range controlled {
		if v, ok := number(props, k); ok {
			out[k] = int(math.Round(v))
		}
	}
	return out
}

// Diff returns the entries of want whose value props does not already
// report, so a value the unit already has costs no write. Nil when nothing
// differs.
func Diff(want, props map[string]any) map[string]any {
	var out map[string]any
	for _, k := range slices.Sorted(maps.Keys(want)) {
		w, okW := toFloat(want[k])
		have, okHave := number(props, k)
		if okW && okHave && w == have {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = want[k]
	}
	return out
}

// number reads a numeric property.
func number(props map[string]any, key string) (float64, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

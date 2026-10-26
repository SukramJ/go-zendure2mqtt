// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"errors"
	"maps"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/state"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// webRig is a commandRig with the web UI's store attached and Run's start
// marked, as the composition root has it once the daemon runs.
func webRig(t *testing.T, r *commandRig) (*commandRig, *state.Store) {
	t.Helper()
	st := state.New()
	r.coord.deps.State = st
	r.coord.running.Store(true)
	return r, st
}

// A write from the web UI goes through the same conversions and refusals as
// an MQTT `set`, and a refusal is classed for the HTTP status and recorded
// in the problem log.
func TestWebWriteSharesTheSetPath(t *testing.T) {
	r, st := webRig(t, newCommandRig(t))
	sn := goldenUnit().SN
	for _, c := range []struct {
		key, value string
		want       map[string]any // nil: refused
		err        error
	}{
		{"ac_mode", "discharge", map[string]any{"acMode": 2}, nil},
		{"input_limit", "1550", map[string]any{"inputLimit": 1550}, nil},
		{"charge_active", "1", map[string]any{"smartMode": 1, "acMode": 1, "inputLimit": 1200, "outputLimit": 0}, nil},
		{"ac_mode", "bogus", nil, state.ErrInvalidValue},
		{"input_limit", "{broken", nil, state.ErrInvalidValue},
		{"electric_level", "50", nil, state.ErrNotWritable},
		{"evcc/battery/batterymode", "hold", nil, state.ErrNotWritable}, // EVCC_ENABLE off
	} {
		err := r.coord.Write(t.Context(), sn, c.key, c.value)
		got := r.backend.take()
		switch {
		case c.err != nil && !errors.Is(err, c.err):
			t.Errorf("%s=%q: err = %v, want %v", c.key, c.value, err, c.err)
		case c.err != nil && len(got) != 0:
			t.Errorf("%s=%q: refused but wrote %v", c.key, c.value, got)
		case c.err == nil && err != nil:
			t.Errorf("%s=%q: err = %v", c.key, c.value, err)
		case c.err == nil && (len(got) != 1 || !maps.Equal(got[0], c.want)):
			t.Errorf("%s=%q: wrote %v, want %v", c.key, c.value, got, c.want)
		}
	}
	if err := r.coord.Write(t.Context(), "NOPE", "ac_mode", "charge"); !errors.Is(err, state.ErrUnknownDevice) {
		t.Errorf("unknown unit: err = %v", err)
	}
	if n := len(st.Problems()); n != 5 {
		t.Errorf("problems recorded = %d, want 5: %+v", n, st.Problems())
	}
	if p := st.Problems()[0]; p.Event != "coordinator.set_unknown_device" || p.SN != "NOPE" {
		t.Errorf("newest problem = %+v", p)
	}
}

// An MQTT `set` that is refused lands in the problem log as well.
func TestMQTTSetRefusalRecorded(t *testing.T) {
	r, st := webRig(t, newCommandRig(t))
	r.set(t, "config/ac_mode", "bogus")
	ps := st.Problems()
	if len(ps) != 1 || ps[0].Event != "coordinator.set_rejected" {
		t.Fatalf("problems = %+v", ps)
	}
}

// Before Run has captured its context there is nothing to write with.
func TestWebWriteBeforeRunIsNotReady(t *testing.T) {
	r := newCommandRig(t)
	if err := r.coord.Write(t.Context(), goldenUnit().SN, "ac_mode", "charge"); !errors.Is(err, state.ErrNotReady) {
		t.Errorf("err = %v, want ErrNotReady", err)
	}
	if got := r.backend.take(); len(got) != 0 {
		t.Errorf("wrote %v before Run", got)
	}
}

// The evcc battery mode is writable from the UI with the profile on, through
// the same state machine as evcc's own command.
func TestWebWriteBatteryMode(t *testing.T) {
	ev := newEVCCRig(t)
	r, _ := webRig(t, ev.commandRig)
	sn := goldenUnit().SN
	if err := r.coord.Write(t.Context(), sn, "evcc/battery/batterymode", "hold"); !errors.Is(err, state.ErrNotReady) {
		t.Errorf("hold without a report: err = %v, want ErrNotReady", err)
	}
	ev.report(t, discharging())
	if err := r.coord.Write(t.Context(), sn, "evcc/battery/batterymode", "hold"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if got := r.backend.take(); len(got) != 1 || got[0]["outputLimit"] != 0 {
		t.Errorf("hold wrote %v", got)
	}
	if err := r.coord.Write(t.Context(), sn, "evcc/battery/soc", "50"); !errors.Is(err, state.ErrNotWritable) {
		t.Errorf("status-only evcc item: err = %v", err)
	}
}

// Items lists the writable catalog properties with their bounds and
// localised options, the virtual switches, and the evcc battery mode only
// with the profile on; the operating-mode and flash-relevant ones ask first.
func TestItems(t *testing.T) {
	r := newCommandRig(t)
	byKey := map[string]state.ItemInfo{}
	for _, it := range r.coord.Items() {
		byKey[it.Key] = it
	}
	for _, k := range []string{"ac_mode", "smart_mode", "inverse_max_power", "charge_active", "discharge_active"} {
		if !byKey[k].Confirm {
			t.Errorf("%s: confirm = false, want true", k)
		}
	}
	for _, k := range []string{"input_limit", "output_limit", "soc_set", "min_soc"} {
		it, ok := byKey[k]
		if !ok || it.Confirm || it.Component != "number" || it.Min == nil || it.Max == nil {
			t.Errorf("%s = %+v, want an unconfirmed number with bounds", k, it)
		}
	}
	if o := byKey["ac_mode"].Options; len(o) != 2 || o[0] != (state.Option{Value: "charge", Label: "charge"}) {
		t.Errorf("ac_mode options = %+v", o)
	}
	if _, ok := byKey["electric_level"]; ok {
		t.Error("a read-only property is listed")
	}
	if _, ok := byKey["evcc/battery/batterymode"]; ok {
		t.Error("battery mode listed with EVCC_ENABLE off")
	}

	ev := newEVCCRig(t)
	ev.coord.deps.Cfg.Language = "de"
	var mode *state.ItemInfo
	for _, it := range ev.coord.Items() {
		if it.Key == "evcc/battery/batterymode" {
			mode = &it
		}
		if it.Key == "ac_mode" && it.Options[1].Label != "Entladen" {
			t.Errorf("ac_mode options in German = %+v", it.Options)
		}
	}
	if mode == nil || len(mode.Options) != 3 || mode.Options[1].Value != "hold" || mode.Name != "Batteriemodus" {
		t.Errorf("battery mode item = %+v", mode)
	}
}

// Health is "starting" until a unit has reported, "ok" with broker, backend
// and every unit reachable, and "degraded" as soon as one is not.
func TestHealthStatus(t *testing.T) {
	r, st := webRig(t, newCommandRig(t))
	connected := true
	r.coord.deps.MQTTConnected = func() bool { return connected }

	if h := r.coord.Health(); h.Status != "starting" {
		t.Errorf("before a report: status = %q", h.Status)
	}
	r.coord.publish(t.Context(), goldenUnit(), &model.Report{SN: goldenUnit().SN, Properties: discharging()})
	r.coord.UpstreamUsable(true)
	r.coord.DeviceReachable(goldenUnit(), true)
	h := r.coord.Health()
	if h.Status != "ok" || h.Devices != 1 || h.Upstream == nil || !*h.Upstream || !h.MQTTConnected {
		t.Errorf("all up: health = %+v", h)
	}

	r.coord.DeviceReachable(goldenUnit(), false)
	if h := r.coord.Health(); h.Status != "degraded" {
		t.Errorf("unit offline: status = %q", h.Status)
	}
	if d := st.Snapshot().Devices[0]; d.Online == nil || *d.Online {
		t.Errorf("snapshot online = %v, want false", d.Online)
	}
	r.coord.DeviceReachable(goldenUnit(), true)
	connected = false
	if h := r.coord.Health(); h.Status != "degraded" {
		t.Errorf("broker down: status = %q", h.Status)
	}
}

// The config projection says whether a cloud token is set, never what it is.
func TestConfigViewHidesSecrets(t *testing.T) {
	r := newCommandRig(t)
	r.coord.deps.Cfg.CloudAppToken = "s3cret-token"
	r.coord.deps.Cfg.WebPassword = "pw"
	v := r.coord.Config()
	if !v.CloudTokenSet || !v.WebAuth {
		t.Errorf("config view = %+v", v)
	}
}

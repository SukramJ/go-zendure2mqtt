// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-bridgeui"

	"github.com/SukramJ/go-zendure2mqtt/internal/state"
)

// fakeBackend is a deterministic [Backend]: two units, one with a pack.
type fakeBackend struct {
	lastSN, lastKey, lastVal string
}

func ptr[T any](v T) *T { return &v }

func (f *fakeBackend) Snapshot() state.Snapshot {
	return state.Snapshot{Devices: []*state.Device{
		{
			SN: "SN1", Product: "solarFlow2400AC", Model: "SolarFlow 2400 AC", Online: ptr(true),
			UpdatedAt: time.Now(),
			Entries: []state.Entry{
				{Group: "now", Topic: "electric_level", Name: "Battery level", Unit: "%", Value: 64.0},
				{Group: "config", Topic: "ac_mode", Name: "AC mode", Value: "charge"},
				{Group: "battery", Topic: "soc_level", PackSN: "PACK1", Value: 60.0},
			},
		},
		{SN: "SN2", Online: ptr(false), Entries: []state.Entry{}},
	}}
}

func (f *fakeBackend) Health() state.Health {
	return state.Health{
		Status: "degraded", Version: "0.14.0", Connection: "local", Upstream: ptr(true),
		MQTTServer: "broker", MQTTConnected: true, MQTTTopic: "zendure", Devices: 2,
		Problems: []state.Problem{{SN: "SN1", Event: "coordinator.set_rejected", Detail: "x"}},
	}
}

func (f *fakeBackend) Config() state.ConfigView {
	return state.ConfigView{
		Connection: "local", Refresh: 15, LocalDevices: []state.DeviceView{{SN: "SN1", Host: "h"}},
		MQTTServer: "broker", MQTTPort: 1883, MQTTTopic: "zendure", EVCCEnable: true, EVCCBatteryModeTimeout: 120,
	}
}

func (f *fakeBackend) Items() []state.ItemInfo {
	return []state.ItemInfo{
		{
			Key: "ac_mode", Name: "AC mode", Component: "select", Confirm: true,
			Options: []state.Option{{Value: "charge", Label: "Laden"}, {Value: "discharge", Label: "Entladen"}},
		},
		{Key: "output_limit", Name: "Discharge limit", Component: "number"},
	}
}

func (f *fakeBackend) Write(_ context.Context, sn, key, value string) error {
	f.lastSN, f.lastKey, f.lastVal = sn, key, value
	if sn != "SN1" {
		return state.ErrUnknownDevice
	}
	return nil
}

func (f *fakeBackend) Changes() (events <-chan struct{}, cancel func()) {
	return make(chan struct{}), func() {}
}

func newAdapter(lang string) *adapter {
	tr, err := bridgeui.LoadTranslations(i18nFS, "i18n")
	if err != nil {
		panic(err)
	}
	return &adapter{b: &fakeBackend{}, lang: lang, tr: tr}
}

// Units become devices, pack values sub-unit entries, topics keys.
func TestSnapshotMapping(t *testing.T) {
	snap := newAdapter("en").Snapshot()
	if len(snap.Devices) != 2 {
		t.Fatalf("devices = %d", len(snap.Devices))
	}
	d := snap.Devices[0]
	if d.ID != "SN1" || d.Title != "Zendure solarFlow2400AC" || d.Subtitle != "SN SN1 · SolarFlow 2400 AC" || d.Online == nil || !*d.Online {
		t.Errorf("device = %+v", d)
	}
	if e := d.Entries[2]; e.Sub != "PACK1" || e.Key != "soc_level" || e.Group != "battery" {
		t.Errorf("pack entry = %+v", e)
	}
	if d2 := snap.Devices[1]; d2.Title != "Zendure" || d2.Online == nil || *d2.Online {
		t.Errorf("unreported unit = %+v", d2)
	}
}

// A unit only gets controls for what it reports.
func TestItemsPerReportingUnit(t *testing.T) {
	items := newAdapter("en").Items()
	if len(items) != 1 || items[0].Key != "ac_mode" || items[0].Device != "SN1" || !items[0].Confirm || len(items[0].Options) != 2 {
		t.Errorf("items = %+v", items)
	}
}

func TestHealthAndConfigTiles(t *testing.T) {
	a := newAdapter("de")
	h := a.Health()
	if h.Status != "degraded" || len(h.Problems) != 1 || h.Problems[0].Device != "SN1" {
		t.Errorf("health = %+v", h)
	}
	if up := h.Tiles[0]; up.Label != "health.upstream" || up.Text != "val.connected" || up.Sub != "local" {
		t.Errorf("upstream tile = %+v", up)
	}
	tiles := map[string]bridgeui.Tile{}
	for _, tl := range a.Config() {
		tiles[tl.Label] = tl
	}
	if c := tiles["cfg.connection"]; c.Value != "local" || c.Sub != "1 Geräte" {
		t.Errorf("connection tile = %+v", c)
	}
	if e := tiles["cfg.evcc"]; e.Text != "val.active" || e.Sub != "Rückfall nach 120 s" {
		t.Errorf("evcc tile = %+v", e)
	}
	if w := tiles["cfg.webAuth"]; w.Text != "val.off" || w.Tone != "warn" {
		t.Errorf("web auth tile = %+v", w)
	}
}

// Every tile label and section title is in both bundles (the built-in
// bundle covers the val.* texts), so none shows as a raw key.
func TestLabelsTranslated(t *testing.T) {
	a := newAdapter("en")
	var keys []string
	for _, tl := range append(a.Health().Tiles, a.Config()...) {
		keys = append(keys, tl.Label)
	}
	for _, s := range layout.Sections {
		keys = append(keys, s.Title, s.Nav)
		if s.SubTitle != "" {
			keys = append(keys, s.SubTitle)
		}
	}
	for _, g := range []string{"now", "battery", "evcc", "config", "static", "misc"} {
		keys = append(keys, "group."+g)
	}
	for _, lang := range []string{"en", "de"} {
		for _, k := range keys {
			if _, ok := a.tr[lang][k]; !ok {
				t.Errorf("%s.json lacks %q", lang, k)
			}
		}
	}
	b, _ := fs.ReadFile(i18nFS, "i18n/en.json")
	if !strings.Contains(string(b), "{sub}") {
		t.Error("pack.title does not take {sub}")
	}
}

// The server writes through the backend, by unit, and maps its errors.
func TestWriteThroughServer(t *testing.T) {
	fb := &fakeBackend{}
	srv := New(Config{Language: "de"}, fb)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post := func(body string) int {
		res, err := http.Post(ts.URL+"/api/write", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if code := post(`{"device":"SN1","key":"output_limit","value":300}`); code != http.StatusOK || fb.lastVal != "300" {
		t.Errorf("write: %d, val %q", code, fb.lastVal)
	}
	if code := post(`{"device":"NOPE","key":"output_limit","value":300}`); code != http.StatusNotFound {
		t.Errorf("unknown unit: %d", code)
	}
	if code := post(`{"key":"output_limit","value":300}`); code != http.StatusNotFound {
		t.Errorf("no unit: %d", code)
	}

	res, err := http.Get(ts.URL + "/api/ui")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var ui struct {
		Language string          `json:"language"`
		Layout   bridgeui.Layout `json:"layout"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ui); err != nil {
		t.Fatal(err)
	}
	if ui.Language != "de" || ui.Layout.Brand != "zendure2mqtt" || len(ui.Layout.Sections) != 3 {
		t.Errorf("ui = %+v", ui)
	}
}

func TestErrorsAreBridgeUIs(t *testing.T) {
	if !errors.Is(state.ErrNotReady, bridgeui.ErrNotReady) {
		t.Error("state errors are not go-bridgeui's")
	}
}

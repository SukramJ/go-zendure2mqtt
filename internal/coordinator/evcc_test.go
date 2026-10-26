// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// evccRig is a commandRig with EVCC_ENABLE on and a settable clock.
type evccRig struct {
	*commandRig
	now time.Time
}

func newEVCCRig(t *testing.T) *evccRig {
	t.Helper()
	broker := newFakeBroker(nil)
	cfg := testConfig(t, "en")
	cfg.EVCCEnable = true
	backend := &writeRecorder{goldenBackend: goldenBackend{devices: []source.Device{goldenUnit()}}}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rt := newHARuntime(broker, cfg.MQTTTopic)
	c := New(Deps{
		Cfg:        cfg,
		Backend:    backend,
		MQTT:       broker,
		Catalog:    goldenCatalog(t),
		Logger:     logger,
		HARuntime:  rt,
		StatePlane: newStatePlane(broker, cfg.MQTTTopic),
	})
	c.runCtx = t.Context()
	t.Cleanup(rt.Close)
	r := &evccRig{commandRig: &commandRig{coord: c, broker: broker, backend: backend, logs: logs}, now: time.Unix(1_800_000_000, 0)}
	c.evcc.now = func() time.Time { return r.now }
	return r
}

// report publishes one report of the golden unit.
func (r *evccRig) report(t *testing.T, props map[string]any) {
	t.Helper()
	r.coord.publish(t.Context(), goldenUnit(), &model.Report{SN: goldenUnit().SN, Properties: props})
}

// mode sends one batterymode command through the router.
func (r *evccRig) mode(t *testing.T, payload string) {
	t.Helper()
	topic := testName + "/set/" + goldenUnit().SN + "/evcc/battery/batterymode"
	cmd, ok := r.coord.commands.Route(topic)
	if !ok {
		t.Fatalf("%s is not routed", topic)
	}
	cmd.Payload = []byte(payload)
	v, err := publisher.ParseSet(cmd.Payload)
	if err != nil {
		t.Fatalf("ParseSet(%q): %v", payload, err)
	}
	r.coord.handleEVCCSet(t.Context(), cmd, v)
}

// status returns the `val` of an evcc status item.
func (r *evccRig) status(t *testing.T, item string) any {
	t.Helper()
	b, ok := r.broker.retainedPayload(testName + "/status/" + goldenUnit().SN + "/evcc/" + item)
	if !ok {
		return nil
	}
	var obj struct{ Val any }
	if err := json.Unmarshal(b, &obj); err != nil {
		t.Fatalf("%s: %v", item, err)
	}
	return obj.Val
}

// discharging is a unit discharging 800 W under its own settings.
func discharging() map[string]any {
	return map[string]any{
		"packInputPower": float64(800), "outputPackPower": float64(0), "electricLevel": float64(64),
		"acMode": float64(2), "inputLimit": float64(0), "outputLimit": float64(800),
		"smartMode": float64(0), "chargeMaxLimit": float64(2400),
	}
}

func TestEVCCStatusItems(t *testing.T) {
	r := newEVCCRig(t)
	r.report(t, discharging())
	for item, want := range map[string]any{
		"battery/power": float64(800), "battery/soc": float64(64), "battery/batterymode": "normal",
	} {
		if got := r.status(t, item); got != want {
			t.Errorf("%s = %v, want %v", item, got, want)
		}
	}
	if got := r.status(t, "pv/power"); got != nil {
		t.Errorf("pv/power published without solarInputPower: %v", got)
	}
}

// hold → hold → normal: the first hold saves and writes, the re-send writes
// nothing, normal restores exactly what was there.
func TestEVCCBatteryModeRestores(t *testing.T) {
	r := newEVCCRig(t)
	r.report(t, discharging())

	r.mode(t, "2")
	writes := r.backend.take()
	if want := []map[string]any{{"smartMode": 1, "outputLimit": 0}}; !equalWrites(writes, want) {
		t.Fatalf("hold wrote %v, want %v", writes, want)
	}
	if got := r.status(t, "battery/batterymode"); got != "hold" {
		t.Fatalf("batterymode = %v, want hold", got)
	}

	r.mode(t, `{"val":"hold"}`)
	if writes := r.backend.take(); len(writes) != 0 {
		t.Fatalf("an unchanged hold wrote %v", writes)
	}

	r.mode(t, "3")
	if want := []map[string]any{{"acMode": 1, "inputLimit": 2400}}; !equalWrites(r.backend.take(), want) {
		t.Fatal("charge did not write acMode 1 at chargeMaxLimit")
	}

	r.mode(t, "1")
	want := []map[string]any{{"acMode": 2, "inputLimit": 0, "outputLimit": 800, "smartMode": 0}}
	if writes := r.backend.take(); !equalWrites(writes, want) {
		t.Fatalf("normal wrote %v, want %v", writes, want)
	}
	r.mode(t, "normal")
	if writes := r.backend.take(); len(writes) != 0 {
		t.Fatalf("an unchanged normal wrote %v", writes)
	}
}

// After a restart there is nothing saved: normal writes nothing.
func TestEVCCNormalWithoutSavedStateWritesNothing(t *testing.T) {
	r := newEVCCRig(t)
	props := discharging()
	props["outputLimit"] = float64(0)
	r.report(t, props)
	r.mode(t, "1")
	if writes := r.backend.take(); len(writes) != 0 {
		t.Fatalf("normal without saved state wrote %v", writes)
	}
}

func TestEVCCRejects(t *testing.T) {
	r := newEVCCRig(t)
	r.mode(t, "2") // no report yet: nothing to save
	r.report(t, discharging())
	r.mode(t, "4")
	r.mode(t, "discharge")
	if writes := r.backend.take(); len(writes) != 0 {
		t.Fatalf("rejected commands wrote %v", writes)
	}
	logs := r.logs.String()
	if strings.Count(logs, "coordinator.set_rejected") != 2 || !strings.Contains(logs, "not reported yet") {
		t.Fatalf("rejections not logged:\n%s", logs)
	}
	cmd, ok := r.coord.commands.Route(testName + "/set/" + goldenUnit().SN + "/evcc/battery/soc")
	if !ok {
		t.Fatal("the evcc route does not match a battery item")
	}
	r.coord.handleEVCCSet(t.Context(), cmd, publisher.SetValue{Text: "50"})
	if !strings.Contains(r.logs.String(), "coordinator.evcc_not_writable") {
		t.Fatal("a set on a status-only item was not refused")
	}
}

// EVCC_BATTERYMODE_TIMEOUT: a hold evcc stops re-sending falls back to normal.
func TestEVCCFallback(t *testing.T) {
	r := newEVCCRig(t)
	r.report(t, discharging())
	r.mode(t, "hold")
	r.backend.take()

	r.now = r.now.Add(90 * time.Second)
	r.coord.evccFallback(t.Context(), 2*time.Minute)
	if writes := r.backend.take(); len(writes) != 0 {
		t.Fatalf("fell back before the timeout: %v", writes)
	}
	r.now = r.now.Add(31 * time.Second)
	r.coord.evccFallback(t.Context(), 2*time.Minute)
	if writes := r.backend.take(); len(writes) != 1 || writes[0]["outputLimit"] != 800 {
		t.Fatalf("fallback wrote %v, want the saved settings", writes)
	}
	if got := r.status(t, "battery/batterymode"); got != "normal" {
		t.Fatalf("batterymode after fallback = %v", got)
	}
	if !strings.Contains(r.logs.String(), "coordinator.evcc_batterymode_timeout") {
		t.Fatal("fallback not logged")
	}
}

// Off by default: no route, no evcc items.
func TestEVCCOff(t *testing.T) {
	r := newCommandRig(t)
	if _, ok := r.coord.commands.Route(testName + "/set/" + goldenUnit().SN + "/evcc/battery/batterymode"); ok {
		t.Fatal("evcc route registered with EVCC_ENABLE off")
	}
	r.coord.publish(t.Context(), goldenUnit(), &model.Report{Properties: discharging()})
	for _, p := range r.broker.publishes() {
		if strings.Contains(p.Topic, "/evcc/") {
			t.Fatalf("published %s with EVCC_ENABLE off", p.Topic)
		}
	}
}

func equalWrites(got, want []map[string]any) bool {
	return slices.EqualFunc(got, want, maps.Equal)
}

// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// projectionRecorder is a [Projection] that records what it is handed.
type projectionRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (p *projectionRecorder) record(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, s)
}

func (p *projectionRecorder) Reading(_ context.Context, r source.Reading) {
	p.record("reading " + r.Device.SN)
}

func (p *projectionRecorder) DeviceReachable(dev source.Device, reachable bool) {
	if reachable {
		p.record("reachable " + dev.SN)
	} else {
		p.record("unreachable " + dev.SN)
	}
}

func (p *projectionRecorder) UpstreamUsable(usable bool) {
	if usable {
		p.record("upstream up")
	} else {
		p.record("upstream down")
	}
}

// TestProjectionSeesEveryReadingAndObservation: with a projection attached
// (MATTER_ENABLED), every reading the coordinator publishes and every
// observer call reaches it as well; the MQTT side is unchanged.
func TestProjectionSeesEveryReadingAndObservation(t *testing.T) {
	broker := newFakeBroker(nil)
	cfg := testConfig(t, "en")
	rt := newHARuntime(broker, cfg.MQTTTopic)
	t.Cleanup(rt.Close)
	rec := &projectionRecorder{}
	c := New(Deps{
		Cfg:     cfg,
		Backend: &goldenBackend{devices: []source.Device{goldenUnit()}},
		MQTT:    broker,
		Catalog: goldenCatalog(t),
		HASS: hass.New("homeassistant", cfg.MQTTTopic, cfg.IdentityRoot(),
			testRenderer(cfg.Language), rt, discardLogger()),
		Logger:     discardLogger(),
		HARuntime:  rt,
		StatePlane: newStatePlane(broker, cfg.MQTTTopic),
		Fabric:     rec,
	})
	c.runCtx = t.Context()
	c.migrateWindow = 50 * time.Millisecond

	unit := goldenUnit()
	c.onReading(t.Context(), source.Reading{Device: unit, Report: goldenReport()})
	c.DeviceReachable(unit, false)
	c.UpstreamUsable(false)
	c.UpstreamUsable(true)
	c.DeviceReachable(unit, true)

	want := []string{
		"reading " + unit.SN, "unreachable " + unit.SN, "upstream down", "upstream up", "reachable " + unit.SN,
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if !slices.Equal(rec.calls, want) {
		t.Errorf("projection calls %q, want %q", rec.calls, want)
	}
}

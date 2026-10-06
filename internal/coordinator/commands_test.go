// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// writeRecorder is a backend that records every write instead of making it.
type writeRecorder struct {
	goldenBackend
	mu     sync.Mutex
	writes []map[string]any
}

func (w *writeRecorder) Write(_ context.Context, _ source.Device, props map[string]any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, maps.Clone(props))
	return nil
}

func (w *writeRecorder) take() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.writes
	w.writes = nil
	return out
}

// commandRig is one coordinator over a fakeBroker with a recording backend,
// logging into a buffer so the warn lines can be asserted.
type commandRig struct {
	coord   *Coordinator
	broker  *fakeBroker
	backend *writeRecorder
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newCommandRig(t *testing.T) *commandRig {
	t.Helper()
	broker := newFakeBroker(nil)
	cfg := testConfig(t, "en")
	backend := &writeRecorder{goldenBackend: goldenBackend{devices: []source.Device{goldenUnit()}}}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	rt := newHARuntime(broker, cfg.MQTTTopic)
	c := New(Deps{
		Cfg:     cfg,
		Backend: backend,
		MQTT:    broker,
		Catalog: goldenCatalog(t),
		HASS: hass.New("homeassistant", cfg.MQTTTopic, cfg.IdentityRoot(),
			testRenderer(cfg.Language), rt, discardLogger()),
		Logger:     logger,
		HARuntime:  rt,
		StatePlane: newStatePlane(broker, cfg.MQTTTopic),
	})
	c.runCtx = t.Context()
	t.Cleanup(rt.Close)
	return &commandRig{coord: c, broker: broker, backend: backend, logs: logs}
}

// set hands one payload to the set handler the way the router does.
func (r *commandRig) set(t *testing.T, item, payload string) {
	t.Helper()
	topic := testName + "/set/" + goldenUnit().SN + "/" + item
	cmd, ok := r.coord.commands.Route(topic)
	if !ok {
		t.Fatalf("%s is not routed", topic)
	}
	cmd.Payload = []byte(payload)
	v, err := publisher.ParseSet(cmd.Payload)
	if err != nil {
		t.Fatalf("ParseSet(%q): %v", payload, err)
	}
	r.coord.handleSet(t.Context(), cmd, v)
}

// TestSetAppliesTheConventionsConversions pins spec §5.3 on this bridge's
// items: `{"val": …}` and plain values alike, an enum by token in any case or
// by either language's label (or its raw code, as before), numbers clamped
// and un-scaled to the device's raw units, a switch by any boolean spelling.
// Every rejected request writes nothing and is logged at warn with its topic
// and payload (§3.3).
func TestSetAppliesTheConventionsConversions(t *testing.T) {
	for _, c := range []struct {
		item, payload string
		want          map[string]any // nil: rejected
	}{
		{"config/ac_mode", `{"val":"discharge"}`, map[string]any{"acMode": 2}},
		{"config/ac_mode", "CHARGE", map[string]any{"acMode": 1}},
		{"config/ac_mode", "Entladen", map[string]any{"acMode": 2}},
		{"config/ac_mode", "2", map[string]any{"acMode": 2}},
		{"config/smart_mode", "flüchtig", map[string]any{"smartMode": 1}},
		{"config/input_limit", "1550", map[string]any{"inputLimit": 1550}},
		{"config/input_limit", `{"val": 9999}`, map[string]any{"inputLimit": 2400}},
		{"config/soc_set", "95", map[string]any{"socSet": 950}},
		{"config/charge_active", "ON", map[string]any{"smartMode": 1, "acMode": 1, "inputLimit": 1200, "outputLimit": 0}},
		{"config/discharge_active", "false", map[string]any{"smartMode": 0, "acMode": 2, "outputLimit": 0, "inputLimit": 0}},
		{"config/ac_mode", "bogus", nil},
		{"config/charge_active", "maybe", nil},
		{"config/input_limit", `{"watts": 5}`, nil},
		{"now/electric_level", "50", nil},
	} {
		rig := newCommandRig(t)
		rig.set(t, c.item, c.payload)
		got := rig.backend.take()
		switch {
		case c.want == nil && len(got) != 0:
			t.Errorf("%s %q: wrote %v, want it rejected", c.item, c.payload, got)
		case c.want == nil:
			if logs := rig.logs.String(); !strings.Contains(logs, "level=WARN") ||
				!strings.Contains(logs, c.item) || !strings.Contains(logs, "payload=") {
				t.Errorf("%s %q: rejection not logged at warn with topic and payload: %s", c.item, c.payload, logs)
			}
		case len(got) != 1 || !maps.Equal(got[0], c.want):
			t.Errorf("%s %q: wrote %v, want %v", c.item, c.payload, got, c.want)
		}
	}
}

// TestSetIsSubscribedAtQoS1AndIgnoresRetainedAndEmpty pins the router
// contract openccu-loom ADR 0083 sets for all six projects: the set filter at
// QoS 1 (every release before 0.10.0 subscribed it at QoS 0 with a raw
// Subscribe), a retained set never runs, and an empty payload is no request.
func TestSetIsSubscribedAtQoS1AndIgnoresRetainedAndEmpty(t *testing.T) {
	rig := newCommandRig(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- rig.coord.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	filter := CommandFilter(testName)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := rig.broker.qosOf(filter); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never subscribed", filter)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if q, _ := rig.broker.qosOf(filter); q != mqtt.QoS1 {
		t.Errorf("%s subscribed at QoS %d, want 1", filter, q)
	}

	topic := testName + "/set/" + goldenUnit().SN + "/config/input_limit"
	rig.broker.deliverMessage(t, &mqtt.Message{Topic: topic, Payload: []byte("800"), Retain: true})
	rig.broker.deliver(t, topic, nil)
	rig.broker.deliver(t, topic, []byte("1200"))

	deadline = time.Now().Add(5 * time.Second)
	var writes []map[string]any
	for len(writes) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		writes = append(writes, rig.backend.take()...)
	}
	rig.coord.commands.WaitIdle()
	writes = append(writes, rig.backend.take()...)
	if len(writes) != 1 || writes[0]["inputLimit"] != 1200 {
		t.Errorf("writes = %v, want exactly the one non-retained, non-empty request", writes)
	}
}

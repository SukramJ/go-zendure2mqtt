// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package hass

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// stubPub records every publish so tests can assert what discovery emitted.
type stubPub struct {
	mu     sync.Mutex
	topics map[string][]byte
	calls  int
}

func (s *stubPub) Publish(_ context.Context, topic string, payload []byte, _ mqtt.QoS, _ bool, _ ...mqtt.PublishOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.topics == nil {
		s.topics = map[string][]byte{}
	}
	s.topics[topic] = payload
	s.calls++
	return nil
}

func newDisc(pub mqtt.Publisher) *Discovery {
	return New("homeassistant", "zendure", "en", pub, nil)
}

func TestConfigFilter(t *testing.T) {
	if got := newDisc(&stubPub{}).ConfigFilter(); got != "homeassistant/+/+/config" {
		t.Errorf("ConfigFilter = %q", got)
	}
}

func TestIsOwnConfig(t *testing.T) {
	d := newDisc(&stubPub{})
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"ours", `{"unique_id":"zendure_HOA1_electric_level","state_topic":"zendure/HOA1/now/electric_level/state"}`, true},
		{"ours no state", `{"unique_id":"zendure_HOA1_btn"}`, true},
		{"foreign unique_id", `{"unique_id":"zigbee2mqtt_x","state_topic":"zigbee2mqtt/x"}`, false},
		{"foreign state root", `{"unique_id":"zendure_HOA1_x","state_topic":"other/HOA1/state"}`, false},
		{"not json", `not-json`, false},
	}
	for _, c := range cases {
		if got := d.IsOwnConfig([]byte(c.payload)); got != c.want {
			t.Errorf("%s: IsOwnConfig = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOrphanConfigs(t *testing.T) {
	d := newDisc(&stubPub{})
	const (
		live    = "homeassistant/sensor/zendure_HOA1_electric_level/config"
		orphan  = "homeassistant/sensor/zendure_HOA1_old_metric/config"
		other   = "homeassistant/sensor/zendure_HOA2_electric_level/config"
		foreign = "homeassistant/sensor/zigbee2mqtt_thing/config"
		cleared = "homeassistant/sensor/zendure_HOA1_cleared/config"
	)
	retained := map[string][]byte{
		live:    []byte(`{"unique_id":"zendure_HOA1_electric_level","state_topic":"zendure/HOA1/now/electric_level/state"}`),
		orphan:  []byte(`{"unique_id":"zendure_HOA1_old_metric","state_topic":"zendure/HOA1/now/old_metric/state"}`),
		other:   []byte(`{"unique_id":"zendure_HOA2_electric_level","state_topic":"zendure/HOA2/now/electric_level/state"}`),
		foreign: []byte(`{"unique_id":"zigbee2mqtt_thing","state_topic":"zigbee2mqtt/thing"}`),
		cleared: {}, // already cleared (empty retained payload)
	}
	published := map[string]bool{live: true}

	got := d.OrphanConfigs(retained, published, "HOA1")
	if len(got) != 1 || got[0] != orphan {
		t.Fatalf("OrphanConfigs = %v, want [%s]", got, orphan)
	}
}

// TestDeviceNameSeedsEntityID checks that a configured DeviceName replaces the
// serial number in both the HA device name and the (language-independent)
// default_entity_id, while an unset name keeps the "Zendure <SN>" default.
func TestDeviceNameSeedsEntityID(t *testing.T) {
	report := &model.Report{Product: "solarFlow2400AC"}
	point := process.Point{Group: "now", Topic: "electric_level", Value: 55, Entry: &catalog.Entry{
		Property: "electricLevel", Topic: "electric_level", Group: "now",
		Platform: "sensor", Unit: "%", Name: "Battery Level",
	}}
	const cfgTopic = "homeassistant/sensor/zendure_HOA1_electric_level/config"

	cases := []struct {
		name           string
		deviceName     string
		wantDeviceName string
		wantEntityID   string
	}{
		{"default", "", "Zendure HOA1", "sensor.zendure_hoa1_electric_level"},
		{"configured", "Balkon Speicher", "Balkon Speicher", "sensor.balkon_speicher_electric_level"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub := &stubPub{}
			d := newDisc(pub)
			dev := source.Device{SN: "HOA1", DeviceName: c.deviceName, Model: "SolarFlow 2400 AC"}
			d.Publish(context.Background(), dev, report, []process.Point{point})

			var cfg struct {
				DefaultEntityID string `json:"default_entity_id"`
				Device          struct {
					Name string `json:"name"`
				} `json:"device"`
			}
			if err := json.Unmarshal(pub.topics[cfgTopic], &cfg); err != nil {
				t.Fatalf("no/invalid config on %q: %v", cfgTopic, err)
			}
			if cfg.Device.Name != c.wantDeviceName {
				t.Errorf("device.name = %q, want %q", cfg.Device.Name, c.wantDeviceName)
			}
			if cfg.DefaultEntityID != c.wantEntityID {
				t.Errorf("default_entity_id = %q, want %q", cfg.DefaultEntityID, c.wantEntityID)
			}
		})
	}
}

func TestPublishReturnsTopicSet(t *testing.T) {
	pub := &stubPub{}
	d := newDisc(pub)
	dev := source.Device{SN: "HOA1", Model: "SolarFlow 2400 AC"}
	report := &model.Report{Product: "solarFlow2400AC"}
	points := []process.Point{
		{Group: "now", Topic: "electric_level", Value: 55, Entry: &catalog.Entry{
			Property: "electricLevel", Topic: "electric_level", Group: "now",
			Platform: "sensor", Unit: "%", Name: "Battery Level",
		}},
		{Group: "misc", Topic: "raw_unmapped", Value: 1, Entry: nil},                                 // skipped: no entry
		{Group: "misc", Topic: "no_platform", Value: 1, Entry: &catalog.Entry{Topic: "no_platform"}}, // skipped: no platform
	}

	const want = "homeassistant/sensor/zendure_HOA1_electric_level/config"

	published := d.Publish(context.Background(), dev, report, points)
	if !published[want] {
		t.Fatalf("published set missing %q: %v", want, published)
	}
	if len(published) != 1 {
		t.Errorf("published has %d topics, want 1 (entry-less / platform-less points skipped)", len(published))
	}
	if pub.calls != 1 {
		t.Errorf("publisher called %d times, want 1", pub.calls)
	}

	// Idempotent: the second call still reports the full current set but does
	// not re-send the (retained) config.
	published2 := d.Publish(context.Background(), dev, report, points)
	if !published2[want] {
		t.Errorf("second publish dropped %q from the set: %v", want, published2)
	}
	if pub.calls != 1 {
		t.Errorf("publisher re-sent retained config: calls = %d, want 1", pub.calls)
	}
}

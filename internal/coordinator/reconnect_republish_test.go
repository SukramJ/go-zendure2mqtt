// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// newReconnectRig is one coordinator over a brokerClient — the fake that
// keeps a retained store — with the orphan reconcile short-circuited.
func newReconnectRig(t *testing.T, logger *slog.Logger) (*Coordinator, *brokerClient) {
	t.Helper()
	pub := newBrokerClient()
	cfg := testConfig(t, "en")
	rt := newHARuntime(pub, cfg.MQTTTopic)
	t.Cleanup(rt.Close)
	if logger == nil {
		logger = discardLogger()
	}
	coord := New(Deps{
		Cfg:     cfg,
		Backend: &goldenBackend{devices: []source.Device{goldenUnit()}},
		MQTT:    pub,
		Catalog: goldenCatalog(t),
		HASS: hass.New("homeassistant", cfg.MQTTTopic, cfg.IdentityRoot(),
			testRenderer(cfg.Language), rt, discardLogger()),
		Logger:     logger,
		HARuntime:  rt,
		StatePlane: newStatePlane(pub, cfg.MQTTTopic),
	})
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	coord.runCtx = dead // reconcileOrphans returns at its first guard
	return coord, pub
}

// retainedDocuments lists the device documents the broker holds.
func (b *brokerClient) retainedDocuments() map[string][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string][]byte{}
	for topic, payload := range b.retained {
		if strings.HasPrefix(topic, "homeassistant/device/") {
			out[topic] = payload
		}
	}
	return out
}

// wipe is a broker restarted without persistence: its retained store is
// gone, every client's link drops and comes back.
func (b *brokerClient) wipe() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.retained = map[string][]byte{}
}

// TestDeviceDocumentsAreRepublishedAfterABrokerLostItsStore pins finding 3
// of the 0.10.0 review. The documents are retained and were written once per
// process: the sent gate in internal/hass reopened only on Home Assistant's
// birth message. A broker restarted without persistence comes back empty,
// Home Assistant — still running — keeps its entities and sends a birth this
// daemon may well miss (it races the resubscribe), and its next restart then
// finds no document at all.
//
// Every (re)connect now republishes them from each device's latest report,
// through PublishBundle — so the per-entity retractions still precede each
// document, which is asserted per document, not just counted.
//
// Mutation check: removing c.republishDiscovery from PublishOnline leaves the
// wiped broker without any document and fails the first assertion.
func TestDeviceDocumentsAreRepublishedAfterABrokerLostItsStore(t *testing.T) {
	coord, pub := newReconnectRig(t, nil)
	dev, report := goldenUnit(), goldenReport()

	coord.publish(context.Background(), dev, report)
	before := pub.retainedDocuments()
	if len(before) != 2 {
		t.Fatalf("the first report retained %d documents, want 2 (unit + pack)", len(before))
	}

	pub.wipe()
	mark := pub.mark()
	coord.PublishOnline(context.Background())

	after := pub.retainedDocuments()
	if len(after) != 2 {
		t.Fatalf("after a reconnect to a broker that lost its store it holds %d documents, want 2", len(after))
	}
	for topic, payload := range before {
		if string(after[topic]) != string(payload) {
			t.Errorf("%s: the republished document differs from the one first written", topic)
		}
	}

	// Ordering: every per-entity config a document supersedes is retracted
	// on this connection before that document is written.
	legacy := legacyAll(t)
	retractedAt := map[string]int{}
	records := pub.since(mark)
	for i, rec := range records {
		if len(rec.Payload) == 0 {
			retractedAt[rec.Topic] = i
		}
	}
	docs := 0
	for i, rec := range records {
		if !strings.HasPrefix(rec.Topic, "homeassistant/device/") || len(rec.Payload) == 0 {
			continue
		}
		docs++
		var doc struct {
			Components map[string]struct {
				UniqueID string `json:"unique_id"`
			} `json:"components"`
		}
		if err := json.Unmarshal(rec.Payload, &doc); err != nil {
			t.Fatalf("%s: %v", rec.Topic, err)
		}
		for key, comp := range doc.Components {
			old, ok := legacy[comp.UniqueID]
			if !ok {
				t.Fatalf("%s/%s: unique_id %q has no frozen per-entity config", rec.Topic, key, comp.UniqueID)
			}
			at, ok := retractedAt[old.Topic]
			if !ok || at > i {
				t.Errorf("%s: %s was not retracted before the document (at %d, document at %d)", rec.Topic, old.Topic, at, i)
			}
		}
	}
	if docs != 2 {
		t.Errorf("the reconnect wrote %d documents, want 2", docs)
	}
}

// TestReconnectPublishesTheCurrentOnlineNotTheCachedOne pins finding 5b. The
// state plane's cache holds the last value that reached the broker; a device
// that stopped answering while the broker was unreachable failed to publish
// `false`, so replaying the cache restored `true` for a device that was gone.
func TestReconnectPublishesTheCurrentOnlineNotTheCachedOne(t *testing.T) {
	coord, pub := newReconnectRig(t, nil)
	dev := goldenUnit()
	topic := coord.topics.Online(dev.SN)

	coord.DeviceReachable(dev, true)
	pub.mu.Lock()
	pub.down = true // the broker is gone
	pub.mu.Unlock()
	coord.DeviceReachable(dev, false) // fails to publish

	pub.reconnect() // the broker kept its store: it still holds `true`
	coord.PublishOnline(context.Background())

	pub.mu.Lock()
	raw := pub.retained[topic]
	pub.mu.Unlock()
	var item struct {
		Val any `json:"val"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("%s = %q: %v", topic, raw, err)
	}
	if item.Val != false {
		t.Errorf("%s after the reconnect = %v, want the current false", topic, item.Val)
	}
}

// TestUnmappedCodeIsLoggedOncePerValue pins the warn line that tells an
// operator which code to add to zendure.yaml: once per device, key and raw
// value, not once per poll.
func TestUnmappedCodeIsLoggedOncePerValue(t *testing.T) {
	logs := &syncBuffer{}
	coord, _ := newReconnectRig(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	dev := goldenUnit()

	for _, code := range []float64{7, 7, 7, 8} {
		report := goldenReport()
		report.Properties["acMode"] = code
		coord.publish(context.Background(), dev, report)
	}
	out := logs.String()
	if got := strings.Count(out, "coordinator.unmapped_value"); got != 2 {
		t.Errorf("unmapped_value logged %d times, want 2 (codes 7 and 8 once each):\n%s", got, out)
	}
	for _, want := range []string{"property=acMode", "raw=7", "raw=8", "sn=" + dev.SN} {
		if !strings.Contains(out, want) {
			t.Errorf("the warn lines lack %q:\n%s", want, out)
		}
	}
}

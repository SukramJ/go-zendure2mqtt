// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// brokerClient is the one fake in this package that distinguishes "the
// transport accepted the write" from "the broker holds the value".
//
// Every other fake here conflates them, and that is exactly the conflation
// under test. This bridge publishes its whole discovery plane at QoS 0, so
// Publish returning nil means Write and Flush returned — not that anything
// reached a broker. On a socket that is already going away those two answers
// differ for as long as the kernel buffer accepts bytes, which is long
// enough for all 29 retractions of a boot.
type brokerClient struct {
	mu sync.Mutex
	// retained is what the broker actually holds: the truth Home Assistant
	// reads, as opposed to what this process believes it wrote.
	retained map[string][]byte
	records  []wireRecord
	// lossy makes a publish return nil without reaching retained — QoS 0 on
	// a dying socket. down makes it fail outright, as the link does once the
	// stack has noticed.
	lossy bool
	down  bool
	// dieOnDocument turns the first device-document write into the moment
	// the stack notices, which is the ordering the defect needs: the
	// retractions are accepted and lost, the document that follows them is
	// refused.
	dieOnDocument bool
}

func newBrokerClient() *brokerClient {
	return &brokerClient{retained: map[string][]byte{}}
}

func (b *brokerClient) Publish(_ context.Context, topic string, payload []byte, qos mqtt.QoS, retain bool, _ ...mqtt.PublishOption) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		// A refused publish never reaches the wire, so it is not recorded.
		return mqtt.ErrNotConnected
	}
	if b.dieOnDocument && len(payload) > 0 && strings.HasPrefix(topic, "homeassistant/device/") {
		b.down = true
		return mqtt.ErrNotConnected
	}
	b.records = append(b.records, wireRecord{
		Topic:   topic,
		Payload: append([]byte(nil), payload...),
		QoS:     qos,
		Retain:  retain,
	})
	if b.lossy {
		return nil // accepted by the socket, lost before the broker
	}
	if retain {
		if len(payload) == 0 {
			delete(b.retained, topic)
		} else {
			b.retained[topic] = append([]byte(nil), payload...)
		}
	}
	return nil
}

func (b *brokerClient) Subscribe(context.Context, string, mqtt.QoS, mqtt.MessageHandler, ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	return mqtt.SubscribeResult{}, nil
}

func (b *brokerClient) Unsubscribe(context.Context, string) error { return nil }

// seedLegacy puts the frozen pre-migration fleet on the broker, which is the
// state every installation upgrading into the device-document form is in.
func (b *brokerClient) seedLegacy(t *testing.T) {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range legacyAll(t) {
		b.retained[e.Topic] = []byte("{}")
	}
}

// reconnect is the link coming back: a new socket to the same broker, whose
// retained store is exactly what actually reached it.
func (b *brokerClient) reconnect() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lossy, b.down, b.dieOnDocument = false, false, false
}

// mark returns the number of records written so far, so a later read can be
// scoped to "since the reconnect".
func (b *brokerClient) mark() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.records)
}

func (b *brokerClient) since(mark int) []wireRecord {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]wireRecord(nil), b.records[mark:]...)
}

// legacyStillRetained counts the frozen per-entity configs the broker still
// holds — the count Home Assistant's refusal is a function of.
func (b *brokerClient) legacyStillRetained(t *testing.T) int {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, e := range legacyAll(t) {
		if _, ok := b.retained[e.Topic]; ok {
			n++
		}
	}
	return n
}

// TestRetractionsAreReSentAfterAReconnect is the pin for the one window in
// this migration where every party reports success and the entities still do
// not appear.
//
// publisher.Runtime memoises a superseded topic as cleared the moment
// Transport.Publish returns nil. At QoS 0 — which is what this bridge
// publishes its whole discovery plane at, deliberately and pinned — that
// return says Write and Flush returned, and nothing about a broker. The memo
// is per *process*; the success it records is per *connection*. So:
//
//  1. the retractions are written to a socket that is already going away and
//     return nil, and the memo records all 29 as done;
//  2. the document that follows them fails, and is therefore correctly
//     withheld — internal/hass marks nothing sent and retries;
//  3. the link comes back;
//  4. the retry sends zero retractions, because every topic is memoised, and
//     publishes the device document into a tree that still holds all 29 old
//     per-entity configs.
//
// Home Assistant answers that with exactly one
// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message` line
// and nothing else. There is no error on the wire and none in this daemon's
// log — both publishes "succeeded" — and the entities do not appear.
//
// The same shape shipped in go-mtec2mqtt and was measured there as
// `retractions re-sent = 0, document published = true, configs still
// retained = 1`. The fix three sibling bridges reached is to rebuild the
// whole Runtime per connection; this one calls Runtime.Reset from
// PublishOnline instead, because Runtime.Declared() is load-bearing for
// sweepOrphans here — it is the claim set that keeps a second device's
// documents from being judged orphans — and a rebuilt runtime starts with it
// empty. Reset clears the per-connection half and keeps Declared.
//
// The reconnect is what makes this test the pin it has to be. mtec's own
// test drove only a process restart, which forgets the memo for free, and
// that is precisely why the gap survived a release there.
//
// Mutation check, each run five times and caught five times: removing
// c.deps.HARuntime.Reset() from PublishOnline, which restores exactly the
// four steps above and reproduces the measured numbers — 7 of 29 retractions
// re-sent, 2 documents published, 22 legacy configs still retained;
// substituting Runtime.Republish for Reset, which the library names as the
// answer that looks right and is not (it re-sends cached bytes and skips the
// supersede step entirely); and ignoring PublishBundle's error in
// hass.Discovery.Publish, which marks the document sent and leaves the retry
// with nothing to send.
func TestRetractionsAreReSentAfterAReconnect(t *testing.T) {
	pub := newBrokerClient()
	pub.seedLegacy(t)
	if got := pub.legacyStillRetained(t); got != 29 {
		t.Fatalf("the broker was seeded with %d legacy configs, want this fleet's 29", got)
	}
	pub.lossy, pub.dieOnDocument = true, true

	cfg := &config.Config{MQTTTopic: "zendure2mqtt", Language: "en"}
	rt := newHARuntime(pub, cfg.MQTTTopic)
	t.Cleanup(rt.Close)
	disc := hass.New("homeassistant", cfg.MQTTTopic,
		harender.Renderer{Root: cfg.MQTTTopic, Lang: cfg.Language}, rt, discardLogger())
	dev, report := goldenUnit(), goldenReport()
	points := resolvePoints(t, dev, report)

	// PublishOnline is reached through the Coordinator rather than by
	// calling the runtime directly, because the wiring is half the claim:
	// what has to run on every (re)connect is whatever main.go hands to
	// mqtt.Lifecycle.OnConnect, and that is this method.
	coord := New(Deps{
		Cfg:        cfg,
		Backend:    &goldenBackend{devices: []source.Device{dev}},
		MQTT:       pub,
		Catalog:    goldenCatalog(t),
		HASS:       disc,
		Logger:     discardLogger(),
		HARuntime:  rt,
		StatePlane: newStatePlane(pub, cfg.MQTTTopic),
	})

	// 1+2: the boot that performs the migration, onto a link that dies
	// between the retractions and the document.
	disc.Publish(context.Background(), dev, report, points)
	if got := pub.legacyStillRetained(t); got != 29 {
		t.Fatalf("after the lossy pass the broker holds %d legacy configs, want 29 — the fake did not model QoS 0 loss", got)
	}

	// 3: the link comes back.
	pub.reconnect()
	coord.PublishOnline(context.Background())
	mark := pub.mark()

	// 4: the retry.
	disc.Publish(context.Background(), dev, report, points)

	retractions, documents := 0, 0
	for _, rec := range pub.since(mark) {
		if !strings.HasPrefix(rec.Topic, "homeassistant/") {
			continue
		}
		if len(rec.Payload) == 0 {
			retractions++
			continue
		}
		if strings.HasPrefix(rec.Topic, "homeassistant/device/") {
			documents++
		}
	}

	if documents == 0 {
		t.Fatalf("the retry published no device document at all; the retry itself is broken, not the retraction memo")
	}
	if retractions != 29 {
		t.Errorf("the retry re-sent %d retractions, want this fleet's 29: the per-connection successes of a dead link were memoised as done, and Home Assistant will refuse every one of the %d documents with one WARNING line",
			retractions, documents)
	}
	if got := pub.legacyStillRetained(t); got != 0 {
		t.Errorf("after the retry the broker still holds %d of the 29 legacy per-entity configs while the device documents are published; that is the conflict Home Assistant reports once and then leaves the entities missing",
			got)
	}
}

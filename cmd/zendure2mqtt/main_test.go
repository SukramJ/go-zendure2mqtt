// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package main

import (
	"context"
	"errors"
	"testing"

	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"
)

// failingPublisher always reports a broker-side failure so the breaker
// counts every publish against its threshold.
type failingPublisher struct{ calls int }

func (p *failingPublisher) Publish(context.Context, string, []byte, mqtt.QoS, bool, ...mqtt.PublishOption) error {
	p.calls++
	return mqtt.ErrNotConnected
}

// recordingSubscriber captures Subscribe/Unsubscribe filters so the
// test can prove the split client delegates them to the raw client.
type recordingSubscriber struct {
	subscribed   []string
	unsubscribed []string
}

func (s *recordingSubscriber) Subscribe(_ context.Context, filter string, _ mqtt.QoS, _ mqtt.MessageHandler, _ ...mqtt.SubscribeOption) (mqtt.SubscribeResult, error) {
	s.subscribed = append(s.subscribed, filter)
	return mqtt.SubscribeResult{}, nil
}

func (s *recordingSubscriber) Unsubscribe(_ context.Context, filter string) error {
	s.unsubscribed = append(s.unsubscribed, filter)
	return nil
}

// TestMQTTSessionPublishIsCircuitGated proves the coordinator-facing
// session routes Publish through the breaker: once the failure
// threshold is reached, publishes fail fast with ErrCircuitOpen and no
// longer hit the underlying client.
//
// The session itself is now [mqtt.SplitClient] rather than a local
// struct, so what this pins is the wiring in main.go — that the
// breaker is on the publish half — not a type this repo owns.
func TestMQTTSessionPublishIsCircuitGated(t *testing.T) {
	t.Parallel()

	pub := &failingPublisher{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(pub, mqtt.BreakerConfig{FailureThreshold: 1}),
		&recordingSubscriber{},
	)

	err := session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrNotConnected) {
		t.Fatalf("first publish: got %v, want ErrNotConnected", err)
	}
	err = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	if !errors.Is(err, mqtt.ErrCircuitOpen) {
		t.Fatalf("second publish: got %v, want ErrCircuitOpen", err)
	}
	if pub.calls != 1 {
		t.Fatalf("underlying publisher saw %d calls, want 1 (open circuit must fail fast)", pub.calls)
	}
}

// TestMQTTSessionSubscribeBypassesBreaker proves subscriptions are not
// affected by the publish-side circuit state.
func TestMQTTSessionSubscribeBypassesBreaker(t *testing.T) {
	t.Parallel()

	sub := &recordingSubscriber{}
	session := mqtt.SplitClient(
		mqtt.NewBreaker(&failingPublisher{}, mqtt.BreakerConfig{FailureThreshold: 1}),
		sub,
	)

	// Trip the circuit open on the publish side.
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)
	_ = session.Publish(t.Context(), "t", nil, mqtt.QoS0, false)

	if _, err := session.Subscribe(t.Context(), "zendure/+/+/+/set", mqtt.QoS0, func(*mqtt.Message) {}); err != nil {
		t.Fatalf("subscribe with open circuit: %v", err)
	}
	if err := session.Unsubscribe(t.Context(), "zendure/+/+/+/set"); err != nil {
		t.Fatalf("unsubscribe with open circuit: %v", err)
	}
	if len(sub.subscribed) != 1 || sub.subscribed[0] != "zendure/+/+/+/set" {
		t.Fatalf("subscriber saw %v, want [zendure/+/+/+/set]", sub.subscribed)
	}
	if len(sub.unsubscribed) != 1 || sub.unsubscribed[0] != "zendure/+/+/+/set" {
		t.Fatalf("unsubscriber saw %v, want [zendure/+/+/+/set]", sub.unsubscribed)
	}
}

// TestDeferredTransportRefusesUseBeforeWiring covers the one type this
// composition root owns.
//
// It exists for an ordering constraint that is real and not incidental: the
// Last Will is part of CONNECT, so the MQTT client has to be constructed with
// it, while the will itself is publisher.Runtime's answer — Will() returns
// the same topic and the same payload AnnounceOnline and AnnounceOffline
// write, which is what keeps this bridge from configuring a will no published
// entity references. The runtime is therefore built first, over a transport
// whose client arrives a few lines later.
//
// What is deliberately NOT covered here: that run() actually uses Will()'s
// values rather than a literal of its own. run() is a composition root in a
// main package — it dials a broker and blocks — and no test can construct it.
// The guarantee is structural instead: there is no "offline" literal and no
// status-topic literal left in main.go, and coordinator.BridgeStatusTopic is
// pinned against harender.Layout.Bridge in
// TestBridgeStatusTopicIsOneString. Saying so is better than implying a
// coverage this file does not have.
func TestDeferredTransportRefusesUseBeforeWiring(t *testing.T) {
	t.Parallel()

	var link deferredTransport
	if err := link.Publish(t.Context(), "t", []byte("x"), 0, true); !errors.Is(err, errTransportNotWired) {
		t.Errorf("Publish before wiring = %v, want errTransportNotWired", err)
	}
	if err := link.Subscribe(t.Context(), "t", 0, func(string, []byte, bool) {}); !errors.Is(err, errTransportNotWired) {
		t.Errorf("Subscribe before wiring = %v, want errTransportNotWired", err)
	}
	if err := link.Unsubscribe(t.Context(), "t"); !errors.Is(err, errTransportNotWired) {
		t.Errorf("Unsubscribe before wiring = %v, want errTransportNotWired", err)
	}

	rec := &recordingSubscriber{}
	link.wire(hagomqtt.Split(&failingPublisher{}, rec))
	if err := link.Publish(t.Context(), "t", []byte("x"), 0, true); !errors.Is(err, mqtt.ErrNotConnected) {
		t.Errorf("Publish after wiring = %v, want the wrapped client's error", err)
	}
	if err := link.Subscribe(t.Context(), "homeassistant/status", 0, func(string, []byte, bool) {}); err != nil {
		t.Errorf("Subscribe after wiring: %v", err)
	}
	if len(rec.subscribed) != 1 || rec.subscribed[0] != "homeassistant/status" {
		t.Errorf("subscriber saw %v, want [homeassistant/status]", rec.subscribed)
	}
	if err := link.Unsubscribe(t.Context(), "homeassistant/status"); err != nil {
		t.Errorf("Unsubscribe after wiring: %v", err)
	}
}

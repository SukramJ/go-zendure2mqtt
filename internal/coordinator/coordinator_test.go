// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
)

func ptr(f float64) *float64 { return &f }

func TestDecodeCommand(t *testing.T) {
	bounded := catalog.Entry{Property: "inputLimit", Min: ptr(0), Max: ptr(2400)}

	tests := []struct {
		name    string
		entry   catalog.Entry
		payload string
		want    any
		wantOK  bool
	}{
		{"nan rejected", bounded, "NaN", nil, false},
		{"inf rejected", bounded, "Inf", nil, false},
		{"clamp above max", bounded, "1e300", 2400, true},
		{"clamp below min", bounded, "-500", 0, true},
		{"in range", bounded, "1200", 1200, true},
		{"rounds", catalog.Entry{Property: "x"}, "12.6", 13, true},
		{"unscales", catalog.Entry{Property: "t", Scale: 10}, "5", 50, true},
		{"non-numeric passes through", catalog.Entry{Property: "s"}, "auto", "auto", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeCommand(tc.entry, tc.payload)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("value = %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestDiscoverySignature(t *testing.T) {
	a := map[string]bool{"homeassistant/sensor/zendure_HOA1_a/config": true, "homeassistant/sensor/zendure_HOA1_b/config": true}
	// Same set, different iteration/insertion order must hash identically.
	b := map[string]bool{"homeassistant/sensor/zendure_HOA1_b/config": true, "homeassistant/sensor/zendure_HOA1_a/config": true}
	if discoverySignature(a) != discoverySignature(b) {
		t.Errorf("signature not order-independent: %q vs %q", discoverySignature(a), discoverySignature(b))
	}

	// A removed entity changes the signature, which is what gates a reconcile.
	c := map[string]bool{"homeassistant/sensor/zendure_HOA1_a/config": true}
	if discoverySignature(a) == discoverySignature(c) {
		t.Errorf("signature unchanged after entity removal: %q", discoverySignature(c))
	}
	if discoverySignature(nil) != "" {
		t.Errorf("empty signature = %q, want \"\"", discoverySignature(nil))
	}
}

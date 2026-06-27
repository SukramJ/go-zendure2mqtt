// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import "testing"

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

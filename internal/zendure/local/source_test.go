// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package local

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

type observed struct {
	mu        sync.Mutex
	usable    []bool
	reachable map[string][]bool
}

func (o *observed) UpstreamUsable(u bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.usable = append(o.usable, u)
}

func (o *observed) DeviceReachable(dev source.Device, r bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.reachable[dev.SN] = append(o.reachable[dev.SN], r)
}

// TestPollReportsReachability pins what feeds `<name>/connected` and
// `<name>/status/<sn>/online` in local mode: a successful poll reports its
// device reachable at once, a device becomes unreachable only at its second
// consecutive failed poll (a single miss reports nothing — 0.10.0 flapped on
// it), and the upstream is usable while at least one device is reachable.
func TestPollReportsReachability(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sn":"UP","properties":{"electricLevel":55}}`))
	}))
	defer up.Close()
	var flaky atomic.Bool
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if flaky.Load() {
			_, _ = w.Write([]byte(`{"sn":"DOWN","properties":{}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()

	b := New([]DeviceConfig{
		{SN: "UP", Host: strings.TrimPrefix(up.URL, "http://")},
		{SN: "DOWN", Host: strings.TrimPrefix(down.URL, "http://")},
	}, 0, nil)
	o := &observed{reachable: map[string][]bool{}}
	b.Observe(o)

	readings := 0
	onReading := func(source.Reading) { readings++ }
	devs := b.Devices()

	b.pollOnce(t.Context(), devs[1], onReading) // DOWN, 1st miss: nothing reported
	b.pollOnce(t.Context(), devs[1], onReading) // DOWN, 2nd miss: unreachable, nothing usable
	b.pollOnce(t.Context(), devs[0], onReading) // UP: usable
	b.pollOnce(t.Context(), devs[1], onReading) // DOWN again: UP still answers
	b.pollOnce(t.Context(), devs[0], onReading) // UP
	flaky.Store(true)
	b.pollOnce(t.Context(), devs[1], onReading) // DOWN answers: reachable at once
	flaky.Store(false)
	b.pollOnce(t.Context(), devs[1], onReading) // one miss: no flap

	if readings != 3 {
		t.Errorf("readings = %d, want 3", readings)
	}
	if got, want := fmt.Sprint(o.usable), "[false true true true true]"; got != want {
		t.Errorf("UpstreamUsable = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(o.reachable["UP"]), "[true true]"; got != want {
		t.Errorf("UP reachable = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(o.reachable["DOWN"]), "[false false true]"; got != want {
		t.Errorf("DOWN reachable = %s, want %s", got, want)
	}
}

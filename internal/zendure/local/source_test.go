// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package local

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
// `<name>/status/<sn>/online` in local mode: each poll reports its device, and
// the upstream is usable while at least one device answered its latest poll.
func TestPollReportsReachability(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sn":"UP","properties":{"electricLevel":55}}`))
	}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	b.pollOnce(t.Context(), devs[1], onReading) // DOWN first: nothing usable yet
	b.pollOnce(t.Context(), devs[0], onReading) // UP: usable
	b.pollOnce(t.Context(), devs[1], onReading) // DOWN again: UP still answered

	if readings != 1 {
		t.Errorf("readings = %d, want 1", readings)
	}
	if got := o.usable; len(got) != 3 || got[0] || !got[1] || !got[2] {
		t.Errorf("UpstreamUsable = %v, want [false true true]", got)
	}
	if got := o.reachable["UP"]; len(got) != 1 || !got[0] {
		t.Errorf("UP reachable = %v, want [true]", got)
	}
	if got := o.reachable["DOWN"]; len(got) != 2 || got[0] || got[1] {
		t.Errorf("DOWN reachable = %v, want [false false]", got)
	}
}

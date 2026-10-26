// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package state is a thread-safe cache of the latest resolved values per
// device, feeding the optional diagnostic web UI. It is only allocated when
// the web UI is enabled, so pure-MQTT deployments carry no overhead.
package state

import (
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Entry is one resolved value as surfaced to the UI.
type Entry struct {
	Group  string `json:"group"`
	Topic  string `json:"topic"`
	PackSN string `json:"pack_sn,omitempty"`
	Name   string `json:"name,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Value  any    `json:"value"`
}

// Device is the latest snapshot for a single device.
type Device struct {
	SN        string    `json:"sn"`
	Model     string    `json:"model,omitempty"`
	Product   string    `json:"product,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Online is the device's reachability as the backend last reported it,
	// nil until it has.
	Online  *bool   `json:"online,omitempty"`
	Entries []Entry `json:"entries"`
}

// Snapshot is a point-in-time copy of the whole cache.
type Snapshot struct {
	StartedAt time.Time `json:"started_at"`
	Devices   []*Device `json:"devices"`
}

// maxProblems bounds the problem log: the UI shows the recent ones, and a
// device rejecting every poll must not grow it without limit.
const maxProblems = 20

// Store holds the latest Device snapshot per serial.
type Store struct {
	mu      sync.RWMutex
	started time.Time
	devices map[string]*Device
	// evcc holds each device's evcc profile entries apart from devices:
	// [Store.Update] replaces a device wholesale on every report, while the
	// battery mode also changes between reports.
	evcc map[string][]Entry
	// online is each device's reachability, kept apart for the same reason:
	// it changes between reports, and a device may be unreachable before
	// its first one.
	online map[string]bool
	// upstream is whether the backend reaches what it bridges, nil until it
	// has said so.
	upstream *bool
	problems []Problem

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// New returns an empty store stamped with the current start time.
func New() *Store {
	return &Store{
		started: time.Now(),
		devices: map[string]*Device{},
		evcc:    map[string][]Entry{},
		online:  map[string]bool{},
		subs:    map[chan struct{}]struct{}{},
	}
}

// StartedAt returns when the store (≈ the daemon) started.
func (s *Store) StartedAt() time.Time { return s.started }

// Update records the latest resolved points for dev.
func (s *Store) Update(dev source.Device, report *model.Report, points []process.Point, lang string) {
	d := &Device{SN: dev.SN, Model: dev.Model, UpdatedAt: time.Now(), Entries: make([]Entry, 0, len(points))}
	if report != nil {
		d.Product = report.Product
	}
	for _, p := range points {
		e := Entry{Group: p.Group, Topic: p.Topic, PackSN: p.PackSN, Value: p.Value}
		if p.Entry != nil {
			e.Name = p.Entry.FriendlyName(lang)
			e.Unit = p.Entry.Unit
		}
		d.Entries = append(d.Entries, e)
	}
	s.mu.Lock()
	s.devices[dev.SN] = d
	s.mu.Unlock()
	s.notify()
}

// SetEVCC records the latest evcc profile entries of the device sn,
// replacing the previous set. Only called with EVCC_ENABLE on, so the UI
// shows the profile exactly when MQTT carries it.
func (s *Store) SetEVCC(sn string, entries []Entry) {
	s.mu.Lock()
	s.evcc[sn] = entries
	s.mu.Unlock()
	s.notify()
}

// SetOnline records whether the device sn answers.
func (s *Store) SetOnline(sn string, online bool) {
	s.mu.Lock()
	prev, known := s.online[sn]
	s.online[sn] = online
	s.mu.Unlock()
	if !known || prev != online {
		s.notify()
	}
}

// SetUpstream records whether the backend reaches what it bridges.
func (s *Store) SetUpstream(usable bool) {
	s.mu.Lock()
	changed := s.upstream == nil || *s.upstream != usable
	s.upstream = &usable
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// Upstream reports the backend's last reachability and whether it has
// reported one at all.
func (s *Store) Upstream() (usable, known bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.upstream == nil {
		return false, false
	}
	return *s.upstream, true
}

// AddProblem appends p to the problem log, dropping the oldest beyond
// [maxProblems].
func (s *Store) AddProblem(p Problem) {
	if p.At.IsZero() {
		p.At = time.Now()
	}
	s.mu.Lock()
	s.problems = append(s.problems, p)
	if n := len(s.problems) - maxProblems; n > 0 {
		s.problems = slices.Delete(s.problems, 0, n)
	}
	s.mu.Unlock()
	s.notify()
}

// Problems returns the problem log, newest first.
func (s *Store) Problems() []Problem {
	s.mu.RLock()
	out := make([]Problem, len(s.problems))
	copy(out, s.problems)
	s.mu.RUnlock()
	slices.Reverse(out)
	return out
}

// Snapshot returns a stable-ordered copy of the cache, each device's evcc
// entries appended to its own. A device known to the backend's reachability
// reports but without a report of its own is listed without entries.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devs := make([]*Device, 0, len(s.devices))
	for sn, d := range s.devices {
		cp := *d
		if extra := s.evcc[sn]; len(extra) > 0 {
			cp.Entries = append(slices.Clip(d.Entries), extra...)
		}
		if on, ok := s.online[sn]; ok {
			cp.Online = &on
		}
		devs = append(devs, &cp)
	}
	for sn, on := range s.online {
		if _, ok := s.devices[sn]; !ok {
			devs = append(devs, &Device{SN: sn, Online: &on, Entries: []Entry{}})
		}
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].SN < devs[j].SN })
	return Snapshot{StartedAt: s.started, Devices: devs}
}

// Changes returns a channel that receives a value whenever the cache
// changes, plus a func that releases it. Notifications coalesce: a slow
// reader sees one pending signal, never a backlog.
func (s *Store) Changes() (events <-chan struct{}, cancel func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.subMu.Lock()
			delete(s.subs, ch)
			s.subMu.Unlock()
		})
	}
}

// notify signals every subscriber without blocking.
func (s *Store) notify() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

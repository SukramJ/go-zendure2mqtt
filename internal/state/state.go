// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package state is a thread-safe cache of the latest resolved values per
// device, feeding the optional diagnostic web UI. It is only allocated when
// the web UI is enabled, so pure-MQTT deployments carry no overhead.
package state

import (
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
	Entries   []Entry   `json:"entries"`
}

// Snapshot is a point-in-time copy of the whole cache.
type Snapshot struct {
	StartedAt time.Time `json:"started_at"`
	Devices   []*Device `json:"devices"`
}

// Store holds the latest Device snapshot per serial.
type Store struct {
	mu      sync.RWMutex
	started time.Time
	devices map[string]*Device
}

// New returns an empty store stamped with the current start time.
func New() *Store {
	return &Store{started: time.Now(), devices: map[string]*Device{}}
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
}

// Snapshot returns a stable-ordered copy of the cache.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devs := make([]*Device, 0, len(s.devices))
	for _, d := range s.devices {
		devs = append(devs, d)
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].SN < devs[j].SN })
	return Snapshot{StartedAt: s.started, Devices: devs}
}

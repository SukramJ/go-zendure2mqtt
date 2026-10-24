// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package fabric

import (
	"sync"

	"github.com/SukramJ/go-fabric/contract"
)

// battery is a unit's state of charge as go-fabric v0.10.8 takes a bridged
// device's battery: a measurement source of class MeasurementBattery
// (device.Measurement), which the kit serves as the device's PowerSource —
// BatPercentRemaining in half-percent from the percentage, BatChargeLevel
// Ok, EndpointList the device's own endpoint. A typed
// powersourcedef.Declare on an ElectricalSensor needs the next go-fabric
// release; this source goes then.
//
// It is a [contract.ChangeNotifier] the device relays for its endpoint
// (device.WithChangeNotifier), so a controller's subscription sees a new level
// as soon as [battery.set] stores it: v0.10.8's battery server does not
// forward its source's notifications itself.
type battery struct {
	mu       sync.Mutex
	level    float64
	observed bool
	next     int
	notify   map[int]func()
}

// MatterMeasurementClass implements [contract.MeasurementSource].
func (*battery) MatterMeasurementClass() contract.MeasurementClass {
	return contract.MeasurementBattery
}

// MatterFloatValue implements [contract.FloatMeasurementSource]: the state of
// charge in percent; observed false until the first reading.
func (b *battery) MatterFloatValue() (value float64, observed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.level, b.observed
}

// OnMatterValueChanged implements [contract.ChangeNotifier].
func (b *battery) OnMatterValueChanged(cb func()) (unsubscribe func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.notify == nil {
		b.notify = map[int]func(){}
	}
	id := b.next
	b.next++
	b.notify[id] = cb
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.notify, id)
			b.mu.Unlock()
		})
	}
}

// set stores level and, when it changed, tells every subscriber. It reports
// whether the value changed.
func (b *battery) set(level float64) bool {
	b.mu.Lock()
	if b.observed && b.level == level {
		b.mu.Unlock()
		return false
	}
	b.level, b.observed = level, true
	cbs := make([]func(), 0, len(b.notify))
	for _, cb := range b.notify {
		cbs = append(cbs, cb)
	}
	b.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
	return true
}

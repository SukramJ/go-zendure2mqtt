// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package fabric

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	epmdef "github.com/SukramJ/go-fabric/cluster/spec/electricalpowermeasurement"
	ptdef "github.com/SukramJ/go-fabric/cluster/spec/powertopology"
	"github.com/SukramJ/go-fabric/device"
	"github.com/SukramJ/go-fabric/node"

	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// maxPowerMilliwatts bounds the unit's AC power both ways: the SolarFlow
// 2400 AC's 2400 W, the catalog's inverseMaxPower ceiling.
const maxPowerMilliwatts = 2_400_000

// powerResolutionMilliwatts is what one step of a reported power is: the
// device reports whole watts.
const powerResolutionMilliwatts = 1000

// projection is the daemon's devices on the node's Aggregator: one bridged
// ElectricalSensor per Zendure unit, keyed by its serial number, so a unit
// keeps its endpoint number across restarts.
type projection struct {
	agg     *node.Aggregator
	devices func() []source.Device
	logger  *slog.Logger

	mu       sync.Mutex
	units    map[string]*unit
	complete bool
}

// unit is one Zendure device as the node serves it.
type unit struct {
	dev     *device.ElectricalSensor
	battery *battery
}

func newProjection(agg *node.Aggregator, devices func() []source.Device, logger *slog.Logger) *projection {
	return &projection{agg: agg, devices: devices, logger: logger, units: map[string]*unit{}}
}

// stableKey is a unit's key on the node: its serial number, the identity the
// daemon keys every MQTT topic and Home Assistant unique_id on.
func stableKey(sn string) device.StringKey { return device.StringKey("zendure:" + sn) }

// newUnit declares one Zendure device: an ElectricalSensor (matter.js
// packages/node/src/endpoints/electrical-sensor.ts) with PowerTopology
// NodeTopology — the measurement applies to the endpoint itself —,
// ElectricalPowerMeasurement on AC with the active power, and the battery as
// its PowerSource.
func newUnit(dev source.Device) (*unit, error) {
	accuracy := []epmdef.MeasurementAccuracyStruct{{
		MeasurementType:  epmdef.MeasurementTypeActivePower,
		Measured:         true,
		MinMeasuredValue: -maxPowerMilliwatts,
		MaxMeasuredValue: maxPowerMilliwatts,
		AccuracyRanges: []epmdef.MeasurementAccuracyRangeStruct{{
			RangeMin: -maxPowerMilliwatts, RangeMax: maxPowerMilliwatts,
			FixedMax: new(uint64(powerResolutionMilliwatts)),
		}},
	}}
	bat := &battery{}
	d, err := device.NewElectricalSensor(device.Options{
		StableKey:     stableKey(dev.SN),
		Name:          hass.DeviceName(dev, ""),
		Vendor:        "Zendure",
		DeviceAddress: dev.SN,
	},
		ptdef.Declare{Features: ptdef.FeatureNodeTopology},
		epmdef.Declare{Features: epmdef.FeatureAlternatingCurrent, Initial: epmdef.Patch{
			PowerMode:                new(epmdef.PowerModeAc),
			NumberOfMeasurementTypes: new(uint8(1)), // the one entry of accuracy: ActivePower
			Accuracy:                 &accuracy,
			Null:                     []uint32{epmdef.AttrActivePower}, // unknown until the first reading
		}},
		device.Measurement(bat),
	)
	if err != nil {
		return nil, fmt.Errorf("matter: device %s: %w", dev.SN, err)
	}
	return &unit{dev: d, battery: bat}, nil
}

// ensure returns dev's unit, declaring it and adding it under the
// Aggregator the first time; a running node reassembles.
func (p *projection) ensure(ctx context.Context, dev source.Device) (*unit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ensureLocked(ctx, dev)
}

func (p *projection) ensureLocked(ctx context.Context, dev source.Device) (*unit, error) {
	if u, ok := p.units[dev.SN]; ok {
		return u, nil
	}
	u, err := newUnit(dev)
	if err != nil {
		return nil, err
	}
	if err := p.agg.Add(ctx, u.dev); err != nil {
		return nil, fmt.Errorf("matter: add device %s: %w", dev.SN, err)
	}
	p.units[dev.SN] = u
	p.logger.Info("zendure2mqtt.matter_device_added", slog.String("sn", dev.SN))
	return u, nil
}

// addKnown adds every device the backend lists now.
func (p *projection) addKnown(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, dev := range p.devices() {
		if _, err := p.ensureLocked(ctx, dev); err != nil {
			p.logger.Warn("zendure2mqtt.matter_device_failed", slog.String("sn", dev.SN), slog.String("err", err.Error()))
		}
	}
}

// markComplete adds what the backend lists and, the first time, tells the
// node the set is complete, so it may drop the stored endpoint numbers of
// units that are gone. It runs once the backend's upstream is usable: the
// local list is the configured one from the start, the cloud's is the
// login's, which is in place by then.
func (p *projection) markComplete(ctx context.Context) {
	p.addKnown(ctx)
	p.mu.Lock()
	done := p.complete
	p.complete = true
	p.mu.Unlock()
	if !done {
		p.agg.MarkComplete() //nolint:contextcheck // the node's own: every device is added
	}
}

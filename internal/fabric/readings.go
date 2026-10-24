// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package fabric

import (
	"context"
	"log/slog"
	"math"

	epmdef "github.com/SukramJ/go-fabric/cluster/spec/electricalpowermeasurement"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// The report properties the projection reads (zendure.yaml, group "now"):
// the state of charge in percent and the AC power in watts the unit takes
// from the grid and gives to the home.
const (
	propLevel     = "electricLevel"
	propGridInput = "gridInputPower"
	propHomeOut   = "outputHomePower"
)

// reading projects one report onto its unit, adding the unit first if the
// node does not serve it yet. Only what changed is set; the kit stores it
// and reports it to every subscribed controller.
func (p *projection) reading(ctx context.Context, r source.Reading) {
	if r.Report == nil {
		return
	}
	u, err := p.ensure(ctx, r.Device)
	if err != nil {
		p.logger.Warn("zendure2mqtt.matter_device_failed", slog.String("sn", r.Device.SN), slog.String("err", err.Error()))
		return
	}
	if err := u.apply(r.Report); err != nil {
		p.logger.Warn("zendure2mqtt.matter_set_failed", slog.String("sn", r.Device.SN), slog.String("err", err.Error()))
	}
}

// apply sets the unit's ActivePower and battery level from report.
//
// ActivePower is the power at the unit's AC side in milliwatts, positive
// imported and negative exported (matter.js
// packages/model/src/standard/resources/electrical-power-measurement.resource.ts:159-162
// at 862ad75f): the grid input less the home output. The level is the
// battery's percentage; the kit serves it as BatPercentRemaining in
// half-percent.
func (u *unit) apply(report *model.Report) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if mw, ok := activePower(report.Properties); ok && (!u.powerKnown || mw != u.power) {
		if err := u.dev.ElectricalPowerMeasurement().Set(epmdef.Patch{ActivePower: &mw}); err != nil {
			return err
		}
		u.power, u.powerKnown = mw, true
	}
	if level, ok := number(report.Properties, propLevel); ok {
		u.battery.set(min(max(level, 0), 100))
	}
	return nil
}

// activePower is the grid input less the home output, in milliwatts; false
// when the report carries neither.
func activePower(props map[string]any) (int64, bool) {
	in, okIn := number(props, propGridInput)
	out, okOut := number(props, propHomeOut)
	if !okIn && !okOut {
		return 0, false
	}
	return int64(math.Round((in - out) * 1000)), true
}

// number reads a numeric report property (JSON numbers decode as float64).
func number(props map[string]any, key string) (float64, bool) {
	switch v := props[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

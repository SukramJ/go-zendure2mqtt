// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/evcc"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// This file is the I/O half of the shared evcc profile (internal/evcc is the
// pure half, docs/evcc.md the contract): it publishes the `evcc` item group
// beside every report and turns a batterymode command into property writes.
//
// A battery mode is a state the bridge holds, not a property of the unit:
// `hold` and `charge` overwrite the unit's acMode/inputLimit/outputLimit/
// smartMode, and `normal` writes back exactly what they were before the first
// of them. That saved state lives in memory only — a restart while `hold` or
// `charge` is active forgets it, and a later `normal` then writes nothing
// (docs/evcc.md says so).

// evccPlane is the per-unit battery-mode state. Every mode change runs under
// mu, so two commands for one unit can never interleave their writes.
type evccPlane struct {
	mu    sync.Mutex
	units map[string]*evccUnit
	now   func() time.Time
}

// evccUnit is one unit's profile state.
type evccUnit struct {
	// props merges every report's raw properties, so a cloud report that
	// carries only the changed ones still leaves the full picture.
	props map[string]any
	// mode is the battery mode last applied, `normal` until a command says
	// otherwise.
	mode evcc.Mode
	// saved is the controlled properties as they were before the first
	// hold/charge; nil while `normal`.
	saved map[string]any
	// lastCmd is when evcc last sent a batterymode, for the fallback.
	lastCmd time.Time
}

func newEVCCPlane() *evccPlane {
	return &evccPlane{units: map[string]*evccUnit{}, now: time.Now}
}

// unit returns sn's state, creating it in `normal`. Callers hold mu.
func (p *evccPlane) unit(sn string) *evccUnit {
	u, ok := p.units[sn]
	if !ok {
		u = &evccUnit{props: map[string]any{}, mode: evcc.ModeNormal}
		p.units[sn] = u
	}
	return u
}

// publishEVCC publishes the profile values a report yields, and the unit's
// battery mode.
func (c *Coordinator) publishEVCC(ctx context.Context, dev source.Device, report *model.Report) {
	p := c.evcc
	p.mu.Lock()
	u := p.unit(dev.SN)
	if report != nil {
		maps.Copy(u.props, report.Properties)
	}
	values := evcc.Derive(u.props)
	mode := u.mode
	p.mu.Unlock()

	for _, v := range values {
		c.publishState(ctx, c.topics.EVCCStatus(dev.SN, v.Role, v.Key), v.Value)
	}
	c.publishState(ctx, c.topics.EVCCStatus(dev.SN, evcc.RoleBattery, evcc.KeyBatteryMode), string(mode))
}

// handleEVCCSet routes one evcc command. Route: `<name>/set/<sn>/evcc/<role>/<key>`,
// so the wildcards are the serial, the role and the key.
func (c *Coordinator) handleEVCCSet(ctx context.Context, cmd publisher.Command, v publisher.SetValue) {
	if len(cmd.Wildcards) != 3 {
		return
	}
	sn, role, key := cmd.Wildcards[0], cmd.Wildcards[1], cmd.Wildcards[2]
	reject := func(event string, attrs ...slog.Attr) {
		attrs = append([]slog.Attr{
			slog.String("topic", cmd.Topic), slog.String("payload", string(cmd.Payload)),
		}, attrs...)
		c.logger.LogAttrs(ctx, slog.LevelWarn, event, attrs...)
	}
	if role != evcc.RoleBattery || key != evcc.KeyBatteryMode {
		reject("coordinator.evcc_not_writable")
		return
	}
	c.snMu.RLock()
	dev, ok := c.bySN[sn]
	c.snMu.RUnlock()
	if !ok {
		reject("coordinator.set_unknown_device", slog.String("sn", sn))
		return
	}
	if v.Structured() {
		reject("coordinator.set_rejected", slog.String("evcc", key))
		return
	}
	mode, err := evcc.ParseMode(v.Text)
	if err != nil {
		reject("coordinator.set_rejected", slog.String("evcc", key), slog.String("err", err.Error()))
		return
	}
	if err := c.applyBatteryMode(ctx, dev, mode); err != nil {
		reject("coordinator.write_failed", slog.String("sn", sn), slog.String("err", err.Error()))
	}
}

// errNoReport refuses a hold/charge for a unit that has not reported yet:
// there is nothing to save, so `normal` could never restore it.
var errNoReport = errors.New("evcc: the unit has not reported yet, nothing to restore later")

// applyBatteryMode moves one unit to mode.
//
// The first hold/charge out of `normal` saves the controlled properties;
// `normal` writes them back and forgets them. Each step writes only what the
// unit does not already report ([evcc.Diff]), so evcc re-sending an unchanged
// mode costs no write — and a unit that drifted (a change in the Zendure app)
// is put back on the next re-send.
func (c *Coordinator) applyBatteryMode(ctx context.Context, dev source.Device, mode evcc.Mode) error {
	p := c.evcc
	p.mu.Lock()
	u := p.unit(dev.SN)
	u.lastCmd = p.now()

	var want map[string]any
	switch {
	case mode == evcc.ModeNormal && u.saved == nil:
		if u.mode != evcc.ModeNormal {
			c.logger.Info("coordinator.evcc_nothing_to_restore", slog.String("sn", dev.SN))
		}
	case mode == evcc.ModeNormal:
		want = u.saved
	default:
		if len(u.props) == 0 {
			p.mu.Unlock()
			return errNoReport
		}
		if u.saved == nil {
			u.saved = evcc.Snapshot(u.props)
		}
		want = evcc.Target(mode, u.props, c.deps.Cfg.ChargeActiveW())
	}

	write := evcc.Diff(want, u.props)
	if write != nil {
		wctx, cancel := context.WithTimeout(ctx, setTimeout)
		err := c.deps.Backend.Write(wctx, dev, write)
		cancel()
		if err != nil {
			p.mu.Unlock()
			return err
		}
		// Optimistic until the re-read: a re-send before it must not write
		// again.
		maps.Copy(u.props, write)
	}
	from := u.mode
	u.mode = mode
	if mode == evcc.ModeNormal {
		u.saved = nil
	}
	p.mu.Unlock()

	if from != mode || write != nil {
		c.logger.Info("coordinator.evcc_batterymode",
			slog.String("sn", dev.SN), slog.String("from", string(from)), slog.String("to", string(mode)),
			slog.Int("written", len(write)))
	}
	c.publishState(ctx, c.topics.EVCCStatus(dev.SN, evcc.RoleBattery, evcc.KeyBatteryMode), string(mode))
	if write != nil && c.runCtx != nil {
		c.reReadSoon(dev) //nolint:contextcheck // the re-read outlives this request; it is bounded by the daemon's lifetime
	}
	return nil
}

// evccFallback returns every unit whose hold/charge has stood longer than
// timeout without evcc sending it again to `normal` — EVCC_BATTERYMODE_TIMEOUT,
// so an evcc that stopped never leaves a battery locked.
func (c *Coordinator) evccFallback(ctx context.Context, timeout time.Duration) {
	p := c.evcc
	now := p.now()
	p.mu.Lock()
	var stale []string
	for sn, u := range p.units {
		if u.mode != evcc.ModeNormal && now.Sub(u.lastCmd) >= timeout {
			stale = append(stale, sn)
		}
	}
	p.mu.Unlock()
	sort.Strings(stale)
	for _, sn := range stale {
		c.snMu.RLock()
		dev, ok := c.bySN[sn]
		c.snMu.RUnlock()
		if !ok {
			continue
		}
		c.logger.Warn("coordinator.evcc_batterymode_timeout",
			slog.String("sn", sn), slog.Duration("timeout", timeout))
		if err := c.applyBatteryMode(ctx, dev, evcc.ModeNormal); err != nil {
			c.logger.Warn("coordinator.write_failed", slog.String("sn", sn), slog.String("err", err.Error()))
		}
	}
}

// runEVCCFallback checks [Coordinator.evccFallback] a few times per timeout
// until ctx ends.
func (c *Coordinator) runEVCCFallback(ctx context.Context, timeout time.Duration) {
	tick := min(max(timeout/4, time.Second), 5*time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.evccFallback(ctx, timeout)
		}
	}
}

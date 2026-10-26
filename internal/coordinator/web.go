// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/evcc"
	"github.com/SukramJ/go-zendure2mqtt/internal/state"
	"github.com/SukramJ/go-zendure2mqtt/internal/version"
)

// This file is the coordinator's side of the diagnostic web UI: it satisfies
// web.Backend structurally, so the web package never imports the
// coordinator. Every method reads [Deps.State], which the composition root
// allocates whenever the UI is on.

// Snapshot returns the latest values of every device.
func (c *Coordinator) Snapshot() state.Snapshot {
	if c.deps.State == nil {
		return state.Snapshot{}
	}
	return c.deps.State.Snapshot()
}

// Health returns the operational status surface.
func (c *Coordinator) Health() state.Health {
	cfg := c.deps.Cfg
	h := state.Health{
		Version:     version.Version,
		Connection:  cfg.Connection,
		MQTTServer:  cfg.MQTTServer,
		MQTTTopic:   cfg.MQTTTopic,
		HASSEnabled: cfg.HASSEnable,
		EVCCEnabled: cfg.EVCCEnable,
		Language:    cfg.Language,
	}
	if c.deps.MQTTConnected != nil {
		h.MQTTConnected = c.deps.MQTTConnected()
	}
	st := c.deps.State
	if st == nil {
		h.Status = "starting"
		return h
	}
	h.StartedAt = st.StartedAt()
	h.UptimeSeconds = int64(time.Since(h.StartedAt).Seconds())
	h.Problems = st.Problems()
	if usable, known := st.Upstream(); known {
		h.Upstream = &usable
	}

	snap := st.Snapshot()
	h.Devices = len(snap.Devices)
	reported, allOnline := false, true
	for _, d := range snap.Devices {
		if !d.UpdatedAt.IsZero() {
			reported = true
		}
		if d.Online != nil && !*d.Online {
			allOnline = false
		}
	}
	switch {
	case !reported:
		h.Status = "starting"
	case h.MQTTConnected && (h.Upstream == nil || *h.Upstream) && allOnline:
		h.Status = "ok"
	default:
		h.Status = "degraded"
	}
	return h
}

// Config returns the sanitised config projection: whether a cloud token is
// set, never the token.
func (c *Coordinator) Config() state.ConfigView {
	cfg := c.deps.Cfg
	v := state.ConfigView{
		Connection:             cfg.Connection,
		Refresh:                cfg.Refresh,
		LocalDevices:           make([]state.DeviceView, 0, len(cfg.LocalDevices)),
		CloudTokenSet:          cfg.CloudConfigured(),
		CloudTLSVerify:         cfg.CloudTLSVerify,
		MQTTServer:             cfg.MQTTServer,
		MQTTPort:               cfg.MQTTPort,
		MQTTTopic:              cfg.MQTTTopic,
		HASSEnable:             cfg.HASSEnable,
		HASSBaseTopic:          cfg.HASSBaseTopic,
		ChargeActiveW:          cfg.ChargeActiveW(),
		DischargeActiveW:       cfg.DischargeActiveW(),
		EVCCEnable:             cfg.EVCCEnable,
		EVCCBatteryModeTimeout: cfg.EVCCBatteryModeTimeout,
		WebAuth:                cfg.WebUser != "" || cfg.WebPassword != "",
		Language:               cfg.Language,
		Debug:                  cfg.Debug,
	}
	for _, d := range cfg.LocalDevices {
		v.LocalDevices = append(v.LocalDevices, state.DeviceView{SN: d.SN, Host: d.Host})
	}
	return v
}

// Items lists everything the UI can write: the writable catalog properties,
// the virtual switches and, with the evcc profile on, its battery mode.
func (c *Coordinator) Items() []state.ItemInfo {
	lang := c.deps.Cfg.Language
	var items []state.ItemInfo
	entries := c.deps.Catalog.Entries()
	for i := range entries {
		e := &entries[i]
		if !e.Writable {
			continue
		}
		it := state.ItemInfo{
			Key: e.TopicLeaf(), Group: e.Group, Name: e.FriendlyName(lang), Unit: e.Unit,
			Component: e.Platform, Min: e.Min, Max: e.Max, Step: e.Step, Confirm: e.Confirm,
		}
		for _, code := range e.Codes() {
			token, _ := e.Token(code)
			label, _ := e.Label(code, lang)
			it.Options = append(it.Options, state.Option{Value: token, Label: label})
		}
		items = append(items, it)
	}
	// A switch rewrites acMode and smartMode at once, so it is confirmed
	// like them.
	for _, sw := range c.switches {
		items = append(items, state.ItemInfo{
			Key: sw.Topic, Group: "config", Name: sw.FriendlyName(lang), Component: "switch", Confirm: true,
		})
	}
	if c.evcc != nil {
		mode := evcc.Item{Role: evcc.RoleBattery, Key: evcc.KeyBatteryMode}
		items = append(items, state.ItemInfo{
			Key: evcc.Group + "/" + mode.String(), Group: evcc.Group, Name: mode.Name(lang), Component: "select",
			Options: batteryModeOptions(lang),
		})
	}
	return items
}

// batteryModeOptions are the evcc battery modes as select options, labelled
// in lang; the value is the token evcc's own commands carry.
func batteryModeOptions(lang string) []state.Option {
	labels := map[evcc.Mode][2]string{
		evcc.ModeNormal: {"Normal", "Normal"},
		evcc.ModeHold:   {"Hold (no discharge)", "Halten (nicht entladen)"},
		evcc.ModeCharge: {"Charge from grid", "Aus dem Netz laden"},
	}
	out := make([]state.Option, 0, len(labels))
	for _, m := range []evcc.Mode{evcc.ModeNormal, evcc.ModeHold, evcc.ModeCharge} {
		label := labels[m][0]
		if lang == "de" {
			label = labels[m][1]
		}
		out = append(out, state.Option{Value: string(m), Label: label})
	}
	return out
}

// Write applies one write from the web UI through the same path as an MQTT
// `set`: key is an item's [state.ItemInfo.Key], value its payload as MQTT
// would carry it. A refusal or failure is logged and recorded like one from
// MQTT, and comes back classed by the state.Err* errors.
func (c *Coordinator) Write(ctx context.Context, sn, key, value string) error {
	if !c.running.Load() {
		return state.ErrNotReady
	}
	origin := "web " + sn + "/" + key
	v, err := publisher.ParseSet([]byte(value))
	if err != nil {
		err = fmt.Errorf("%w: %w", state.ErrInvalidValue, err)
	} else if rest, ok := strings.CutPrefix(key, evcc.Group+"/"); ok {
		role, k, _ := strings.Cut(rest, "/")
		if c.evcc == nil {
			err = &setError{event: "coordinator.evcc_not_writable", kind: state.ErrNotWritable}
		} else {
			err = c.applyEVCCSet(ctx, sn, role, k, v)
		}
	} else {
		err = c.applySet(ctx, sn, key, v, origin)
	}
	if err != nil {
		c.rejectSet(ctx, sn, origin, value, err)
	}
	return err
}

// Changes returns the store's coalesced change notifications.
func (c *Coordinator) Changes() (events <-chan struct{}, cancel func()) {
	if c.deps.State == nil {
		return make(chan struct{}), func() {}
	}
	return c.deps.State.Changes()
}

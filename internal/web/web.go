// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web

import (
	"context"
	"embed"
	"log/slog"
	"strconv"
	"strings"

	"github.com/SukramJ/go-bridgeui"

	"github.com/SukramJ/go-zendure2mqtt/internal/state"
)

// i18nFS holds this bridge's labels — sections, groups, status and
// configuration tiles — merged over go-bridgeui's built-in bundles.
//
//go:embed i18n
var i18nFS embed.FS

// Config parameterises the web server.
type Config struct {
	// Bind is the listen address "host:port".
	Bind string
	// User / Password enable HTTP Basic auth when either is non-empty.
	User     string
	Password string
	// Language is LANGUAGE: "de" or English.
	Language string
	// Logger is optional; nil → slog.Default().
	Logger *slog.Logger
}

// layout is the zendure page: live values, one card per battery pack, and
// the evcc profile while it is on; settings and device info under System.
var layout = bridgeui.Layout{
	Brand:   "zendure2mqtt",
	Product: "Zendure SolarFlow",
	Icon:    "🔋",
	Sections: []bridgeui.Section{
		{ID: "live", Title: "section.live", Nav: "nav.live", Groups: []string{"now"}},
		{ID: "packs", Title: "section.packs", Nav: "nav.packs", Groups: []string{"battery"}, Subs: true, SubTitle: "pack.title"},
		{ID: "evcc", Title: "section.evcc", Nav: "nav.evcc", Groups: []string{"evcc"}, Optional: true},
	},
	SystemGroups: []string{"config", "static", "misc"},
}

// New builds the dashboard over backend. It does not bind a socket — call
// Run on the result.
func New(cfg Config, backend Backend) *bridgeui.Server {
	tr, err := bridgeui.LoadTranslations(i18nFS, "i18n")
	if err != nil {
		// The bundles are embedded and covered by a test.
		panic("web: " + err.Error())
	}
	return bridgeui.New(bridgeui.Options{
		Bind:         cfg.Bind,
		User:         cfg.User,
		Password:     cfg.Password,
		Realm:        "zendure2mqtt",
		Logger:       cfg.Logger,
		Language:     cfg.Language,
		Layout:       layout,
		Translations: tr,
	}, &adapter{b: backend, lang: cfg.Language, tr: tr})
}

// adapter presents the coordinator's state as go-bridgeui's contract.
type adapter struct {
	b    Backend
	lang string
	tr   map[string]map[string]string
}

// t looks up one of this bridge's labels in the configured language, for
// the text a tile carries beyond its i18n keys.
func (a *adapter) t(key string) string {
	lang := "en"
	if a.lang == "de" {
		lang = "de"
	}
	if s, ok := a.tr[lang][key]; ok {
		return s
	}
	return key
}

func (a *adapter) Snapshot() bridgeui.Snapshot {
	snap := a.b.Snapshot()
	out := bridgeui.Snapshot{Devices: make([]bridgeui.Device, 0, len(snap.Devices))}
	for _, d := range snap.Devices {
		dev := bridgeui.Device{
			ID:        d.SN,
			Title:     strings.TrimSpace("Zendure " + firstNonEmpty(d.Product, d.Model)),
			Subtitle:  "SN " + d.SN,
			Online:    d.Online,
			UpdatedAt: d.UpdatedAt,
			Entries:   make([]bridgeui.Entry, 0, len(d.Entries)),
		}
		if d.Model != "" && d.Product != "" && d.Model != d.Product {
			dev.Subtitle += " · " + d.Model
		}
		for _, e := range d.Entries {
			dev.Entries = append(dev.Entries, bridgeui.Entry{
				Group: e.Group, Sub: e.PackSN, Key: e.Topic, Name: e.Name, Unit: e.Unit, Value: e.Value,
			})
		}
		out.Devices = append(out.Devices, dev)
	}
	return out
}

func (a *adapter) Health() bridgeui.Health {
	h := a.b.Health()
	upstream := bridgeui.Tile{Label: "health.upstream", Text: "val.unknown", Sub: h.Connection}
	if h.Upstream != nil {
		upstream.Text, upstream.Tone = connected(*h.Upstream)
	}
	mqtt := bridgeui.Tile{Label: "health.mqtt", Sub: h.MQTTServer}
	mqtt.Text, mqtt.Tone = connected(h.MQTTConnected)
	out := bridgeui.Health{
		Status:    h.Status,
		Version:   h.Version,
		StartedAt: h.StartedAt,
		Tiles: []bridgeui.Tile{
			upstream,
			mqtt,
			{Label: "health.topic", Value: h.MQTTTopic},
			active("health.hass", h.HASSEnabled),
			active("health.evcc", h.EVCCEnabled),
			{Label: "health.devices", Value: h.Devices},
		},
		Problems: make([]bridgeui.Problem, 0, len(h.Problems)),
	}
	for _, p := range h.Problems {
		out.Problems = append(out.Problems, bridgeui.Problem{At: p.At, Device: p.SN, Event: p.Event, Detail: p.Detail})
	}
	return out
}

func (a *adapter) Config() []bridgeui.Tile {
	c := a.b.Config()
	conn := bridgeui.Tile{Label: "cfg.connection", Value: c.Connection}
	switch {
	case c.Connection == "cloud":
		tls := "TLS ✗"
		if c.CloudTLSVerify {
			tls = "TLS ✓"
		}
		conn.Sub = a.t("cfg.tokenMissing") + " · " + tls
		if c.CloudTokenSet {
			conn.Sub = a.t("cfg.tokenSet") + " · " + tls
		}
	case len(c.LocalDevices) == 0:
		conn.Sub = a.t("cfg.noDevices")
	default:
		conn.Sub = strings.ReplaceAll(a.t("cfg.devices"), "{n}", strconv.Itoa(len(c.LocalDevices)))
	}
	evcc := active("cfg.evcc", c.EVCCEnable)
	evcc.Tone = ""
	if c.EVCCEnable && c.EVCCBatteryModeTimeout > 0 {
		evcc.Sub = strings.ReplaceAll(a.t("cfg.evccTimeout"), "{s}", strconv.Itoa(c.EVCCBatteryModeTimeout))
	}
	auth := bridgeui.Tile{Label: "cfg.webAuth", Text: "val.on"}
	if !c.WebAuth {
		auth = bridgeui.Tile{Label: "cfg.webAuth", Text: "val.off", Tone: "warn"}
	}
	hass := active("cfg.hass", c.HASSEnable)
	hass.Tone, hass.Sub = "", c.HASSBaseTopic
	return []bridgeui.Tile{
		conn,
		{Label: "cfg.refresh", Value: strconv.Itoa(c.Refresh) + " s"},
		{Label: "cfg.mqtt", Value: c.MQTTServer + ":" + strconv.Itoa(c.MQTTPort), Sub: c.MQTTTopic},
		hass,
		{Label: "cfg.chargeActive", Value: strconv.Itoa(c.ChargeActiveW) + " W"},
		{Label: "cfg.dischargeActive", Value: strconv.Itoa(c.DischargeActiveW) + " W"},
		evcc,
		auth,
		{Label: "cfg.language", Value: firstNonEmpty(c.Language, "en")},
		onOff("cfg.debug", c.Debug),
	}
}

// Items lists, per unit, the writable items that unit reports: a model
// without a property gets no control for it.
func (a *adapter) Items() []bridgeui.Item {
	all := a.b.Items()
	var out []bridgeui.Item
	for _, d := range a.b.Snapshot().Devices {
		has := make(map[string]bool, len(d.Entries))
		for _, e := range d.Entries {
			if e.PackSN == "" {
				has[e.Topic] = true
			}
		}
		for i := range all {
			it := &all[i]
			if !has[it.Key] {
				continue
			}
			item := bridgeui.Item{
				Key: it.Key, Device: d.SN, Name: it.Name, Unit: it.Unit, Component: it.Component,
				Min: it.Min, Max: it.Max, Step: it.Step, Confirm: it.Confirm,
			}
			for _, o := range it.Options {
				item.Options = append(item.Options, bridgeui.Option{Value: o.Value, Label: o.Label})
			}
			out = append(out, item)
		}
	}
	return out
}

func (a *adapter) Write(ctx context.Context, device, key, value string) error {
	if device == "" {
		return state.ErrUnknownDevice
	}
	return a.b.Write(ctx, device, key, value)
}

func (a *adapter) Changes() (events <-chan struct{}, cancel func()) { return a.b.Changes() }

// connected is the text and tone of a link's state.
func connected(up bool) (text, tone string) {
	if up {
		return "val.connected", "ok"
	}
	return "val.disconnected", "err"
}

// active is a feature tile: "active" in green, or "off".
func active(label string, on bool) bridgeui.Tile {
	if on {
		return bridgeui.Tile{Label: label, Text: "val.active", Tone: "ok"}
	}
	return bridgeui.Tile{Label: label, Text: "val.off"}
}

// onOff is a plain on/off tile.
func onOff(label string, on bool) bridgeui.Tile {
	if on {
		return bridgeui.Tile{Label: label, Text: "val.on"}
	}
	return bridgeui.Tile{Label: label, Text: "val.off"}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

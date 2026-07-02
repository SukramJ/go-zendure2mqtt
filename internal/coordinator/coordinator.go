// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package coordinator is the transport-neutral core: it consumes readings
// from a [source.Backend], resolves them through the catalog, publishes
// state (and Home Assistant discovery) to MQTT, and routes inbound /set
// commands back to the backend as writes.
package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/process"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/state"
	"github.com/SukramJ/go-zendure2mqtt/internal/virtual"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Deps are the coordinator's collaborators.
type Deps struct {
	Cfg     *config.Config
	Backend source.Backend
	MQTT    mqtt.Client
	Catalog *catalog.Catalog
	HASS    *hass.Discovery // nil when HA discovery is disabled
	State   *state.Store    // nil when the diagnostic web UI is disabled
	Logger  *slog.Logger
}

// Coordinator wires a backend to the MQTT broker.
type Coordinator struct {
	deps   Deps
	root   string
	logger *slog.Logger

	runCtx   context.Context //nolint:containedctx // captured for the subscription handler
	bySN     map[string]source.Device
	switches []virtual.Switch

	discMu      sync.Mutex        // guards lastDiscSig
	lastDiscSig map[string]string // sn -> signature of the last published config-topic set
	reconciling sync.Map          // sn -> struct{}; in-flight orphan-reconcile gate, one per device
}

// New constructs a Coordinator.
func New(deps Deps) *Coordinator {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	bySN := make(map[string]source.Device)
	for _, d := range deps.Backend.Devices() {
		bySN[d.SN] = d
	}
	return &Coordinator{
		deps:        deps,
		root:        deps.Cfg.MQTTTopic,
		logger:      logger,
		bySN:        bySN,
		switches:    virtual.Switches(deps.Cfg.ChargeActiveValue, deps.Cfg.DischargeActiveValue),
		lastDiscSig: map[string]string{},
	}
}

// Run subscribes to command topics and drives the backend until ctx ends.
func (c *Coordinator) Run(ctx context.Context) error {
	c.runCtx = ctx
	c.PublishOnline(ctx)

	setFilter := c.root + "/+/+/+/set"
	if err := c.deps.MQTT.Subscribe(ctx, setFilter, mqtt.QoS0, c.handleSet); err != nil {
		c.logger.Warn("coordinator.subscribe_failed", slog.String("filter", setFilter), slog.String("err", err.Error()))
	}

	return c.deps.Backend.Run(ctx, func(r source.Reading) {
		c.onReading(ctx, r)
	})
}

// PublishOnline (re)announces bridge availability. Wired to OnConnect.
func (c *Coordinator) PublishOnline(ctx context.Context) {
	topic := c.root + "/bridge/status"
	if err := c.deps.MQTT.Publish(ctx, topic, []byte("online"), mqtt.QoS0, true); err != nil {
		c.logger.Warn("coordinator.online_failed", slog.String("err", err.Error()))
	}
}

// PublishOffline marks the bridge offline on a graceful shutdown. The LWT
// only fires on an ungraceful disconnect (crash / network drop), so a clean
// stop must announce offline explicitly or the retained status stays online.
func (c *Coordinator) PublishOffline(ctx context.Context) {
	topic := c.root + "/bridge/status"
	if err := c.deps.MQTT.Publish(ctx, topic, []byte("offline"), mqtt.QoS0, true); err != nil {
		c.logger.Warn("coordinator.offline_failed", slog.String("err", err.Error()))
	}
}

// onReading resolves a report and publishes every point (plus discovery).
func (c *Coordinator) onReading(ctx context.Context, r source.Reading) {
	if _, ok := c.bySN[r.Device.SN]; !ok {
		c.bySN[r.Device.SN] = r.Device // learn devices discovered at runtime (cloud)
	}
	c.publish(ctx, r.Device, r.Report)
}

// publish resolves a report (catalogued points + virtual switches) and emits
// each point's state plus, on first sight, the HA discovery configs.
func (c *Coordinator) publish(ctx context.Context, dev source.Device, report *model.Report) {
	points := process.Resolve(report, c.deps.Catalog, c.deps.Cfg.Language)
	points = append(points, c.switchPoints(report)...)

	if c.deps.State != nil {
		c.deps.State.Update(dev, report, points, c.deps.Cfg.Language)
	}
	if c.deps.HASS != nil {
		published := c.deps.HASS.Publish(ctx, dev, report, points)
		// Clear any of our own retained discovery configs for this device that we
		// no longer publish (entities removed/renamed across versions), so they do
		// not linger as unavailable entities in Home Assistant.
		c.reconcileOrphans(ctx, dev.SN, published)
	}
	for _, p := range points {
		topic := process.StateTopic(c.root, dev.SN, p)
		if err := c.deps.MQTT.Publish(ctx, topic, formatValue(p.Value), mqtt.QoS0, true); err != nil {
			c.logger.Warn("coordinator.publish_failed", slog.String("topic", topic), slog.String("err", err.Error()))
		}
	}
	c.logger.Debug("coordinator.published", slog.String("sn", dev.SN), slog.Int("points", len(points)))
}

// reconcileCollectWindow is how long the orphan reconcile collects retained
// discovery configs after subscribing; the broker delivers them right after.
const reconcileCollectWindow = 2 * time.Second

// reconcileOrphans clears this daemon's retained discovery configs for one
// device that are no longer in the just-published set — entities removed,
// renamed or re-platformed across versions — so they do not linger as
// unavailable entities in Home Assistant.
//
// It runs only when the device's config-topic set changed (configs are
// retained, so an unchanged poll need not reconcile), runs asynchronously, and
// is gated per device: a re-entrant reconcile for the same serial is skipped.
// The subscribe spans the whole discovery prefix because the serial is not its
// own MQTT level; ownership and device scoping are enforced in code via
// [hass.Discovery.OrphanConfigs], so other integrations' and other devices'
// configs are never touched.
func (c *Coordinator) reconcileOrphans(ctx context.Context, sn string, published map[string]bool) {
	if c.deps.HASS == nil {
		return
	}
	sig := discoverySignature(published)
	c.discMu.Lock()
	changed := c.lastDiscSig[sn] != sig
	c.lastDiscSig[sn] = sig
	c.discMu.Unlock()
	if !changed {
		return
	}
	if _, busy := c.reconciling.LoadOrStore(sn, struct{}{}); busy {
		return // a reconcile for this device is already in flight
	}

	// The reconcile outlives this publish call (it collects for a few seconds),
	// so detach from the caller's cancellation/deadline — a re-read's short-lived
	// context must not abort it — while keeping the request's values. The
	// daemon-lifetime runCtx still bounds it (the select below).
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		defer c.reconciling.Delete(sn)
		filter := c.deps.HASS.ConfigFilter()
		var mu sync.Mutex
		retained := map[string][]byte{}
		handler := func(topic string, payload []byte) {
			mu.Lock()
			retained[topic] = append([]byte(nil), payload...)
			mu.Unlock()
		}
		if err := c.deps.MQTT.Subscribe(bgCtx, filter, mqtt.QoS0, handler); err != nil {
			c.logger.Warn("coordinator.reconcile_subscribe_failed",
				slog.String("sn", sn), slog.String("err", err.Error()))
			return
		}
		// Retained configs arrive right after subscribe; collect briefly, but
		// abandon early if the daemon is shutting down.
		select {
		case <-c.runCtx.Done():
			_ = c.deps.MQTT.Unsubscribe(bgCtx, filter)
			return
		case <-time.After(reconcileCollectWindow):
		}
		_ = c.deps.MQTT.Unsubscribe(bgCtx, filter)

		mu.Lock()
		orphans := c.deps.HASS.OrphanConfigs(retained, published, sn)
		mu.Unlock()

		cleared := c.clearOrphanConfigs(bgCtx, orphans)
		if cleared > 0 {
			c.logger.Info("coordinator.discovery_orphans_cleared",
				slog.String("sn", sn), slog.Int("count", cleared))
		}
	}()
}

// clearOrphanConfigs removes each retained orphan config by publishing an empty
// retained payload to its topic, returning how many were cleared.
func (c *Coordinator) clearOrphanConfigs(ctx context.Context, orphans []string) int {
	cleared := 0
	for _, topic := range orphans {
		if err := c.deps.MQTT.Publish(ctx, topic, nil, mqtt.QoS0, true); err != nil {
			c.logger.Warn("coordinator.reconcile_clear_failed",
				slog.String("topic", topic), slog.String("err", err.Error()))
			continue
		}
		cleared++
	}
	return cleared
}

// discoverySignature is a stable fingerprint of a device's published config
// topic set, so a reconcile runs only when the set actually changes.
func discoverySignature(published map[string]bool) string {
	topics := make([]string, 0, len(published))
	for t := range published {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	return strings.Join(topics, "\n")
}

// reReadDelay gives the device a moment to apply a write before the
// confirmation read.
const reReadDelay = 750 * time.Millisecond

// reReadSoon schedules a fresh read + republish shortly after a write so HA
// reflects the change immediately instead of waiting for the next poll. It
// runs in the background to keep the command handler responsive, and is
// best-effort: backends without a one-shot read (cloud) or a transient
// device error fall back to the periodic poll.
func (c *Coordinator) reReadSoon(dev source.Device) {
	go func() {
		timer := time.NewTimer(reReadDelay)
		defer timer.Stop()
		select {
		case <-c.runCtx.Done():
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(c.runCtx, 15*time.Second)
		defer cancel()
		report, err := c.deps.Backend.Read(ctx, dev)
		if err != nil || report == nil {
			c.logger.Debug("coordinator.reread_skipped", slog.String("sn", dev.SN))
			return
		}
		c.publish(ctx, dev, report)
	}()
}

// handleSet routes an inbound command topic to a backend write.
// Topic shape: <root>/<sn>/<group>/<topic>/set.
func (c *Coordinator) handleSet(topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != c.root || parts[4] != "set" {
		return
	}
	sn, leaf := parts[1], parts[3]
	dev, ok := c.bySN[sn]
	if !ok {
		c.logger.Warn("coordinator.set_unknown_device", slog.String("sn", sn))
		return
	}
	if c.handleSwitchSet(dev, leaf, string(payload)) {
		return // handled by a virtual switch
	}
	entry, ok := c.deps.Catalog.ByTopic(leaf)
	if !ok || !entry.Writable {
		c.logger.Warn("coordinator.set_not_writable", slog.String("topic", leaf))
		return
	}
	value := decodeCommand(entry, string(payload))
	ctx, cancel := context.WithTimeout(c.runCtx, 15*time.Second)
	defer cancel()
	if err := c.deps.Backend.Write(ctx, dev, map[string]any{entry.Property: value}); err != nil {
		c.logger.Warn("coordinator.write_failed",
			slog.String("sn", sn), slog.String("property", entry.Property), slog.String("err", err.Error()))
		return
	}
	c.logger.Info("coordinator.write", slog.String("sn", sn), slog.String("property", entry.Property))
	c.reReadSoon(dev)
}

// switchPoints builds synthetic switch points so the virtual switches flow
// through the same publish + HA-discovery path as catalogued points.
func (c *Coordinator) switchPoints(report *model.Report) []process.Point {
	if len(c.switches) == 0 {
		return nil
	}
	pts := make([]process.Point, 0, len(c.switches))
	for i := range c.switches {
		sw := c.switches[i]
		entry := catalog.Entry{
			Property: sw.Topic, Topic: sw.Topic, Group: "config",
			Platform: "switch", Writable: true, Name: sw.Name, NameDE: sw.NameDE,
		}
		pts = append(pts, process.Point{
			Group: "config", Topic: sw.Topic, Value: sw.State(report), Entry: &entry,
		})
	}
	return pts
}

// handleSwitchSet writes a virtual switch's property set and reports whether
// leaf matched one of them.
func (c *Coordinator) handleSwitchSet(dev source.Device, leaf, payload string) bool {
	for i := range c.switches {
		sw := c.switches[i]
		if sw.Topic != leaf {
			continue
		}
		on := isOn(payload)
		ctx, cancel := context.WithTimeout(c.runCtx, 15*time.Second)
		err := c.deps.Backend.Write(ctx, dev, sw.WriteProps(on))
		cancel()
		if err != nil {
			c.logger.Warn("coordinator.switch_write_failed",
				slog.String("sn", dev.SN), slog.String("switch", leaf), slog.String("err", err.Error()))
		} else {
			c.logger.Info("coordinator.switch_write",
				slog.String("sn", dev.SN), slog.String("switch", leaf), slog.Bool("on", on))
			c.reReadSoon(dev)
		}
		return true
	}
	return false
}

// isOn interprets an MQTT switch command payload.
func isOn(payload string) bool {
	switch strings.ToLower(strings.TrimSpace(payload)) {
	case "1", "on", "true":
		return true
	default:
		return false
	}
}

// decodeCommand turns an MQTT payload string into the value the device
// expects. A select label (English or German) maps back to its integer
// code. A numeric value is converted to the device's raw units by
// inverting the catalog's read scaling (read is (raw-offset)/scale, so
// write is value*scale+offset) and rounding to an integer — Zendure
// properties are integer-valued. Non-numeric payloads stay strings.
func decodeCommand(entry catalog.Entry, payload string) any {
	payload = strings.TrimSpace(payload)
	if code, ok := entry.CodeForLabel(payload); ok {
		if i, err := strconv.Atoi(code); err == nil {
			return i
		}
		return code
	}
	if f, err := strconv.ParseFloat(payload, 64); err == nil {
		if entry.Scale != 0 {
			f *= entry.Scale
		}
		f += entry.Offset
		return int(math.Round(f))
	}
	return payload
}

// formatValue renders a resolved value as an MQTT payload.
func formatValue(v any) []byte {
	switch n := v.(type) {
	case float64:
		return []byte(strconv.FormatFloat(n, 'f', -1, 64))
	case string:
		return []byte(n)
	case bool:
		if n {
			return []byte("1")
		}
		return []byte("0")
	default:
		return []byte(fmt.Sprintf("%v", n))
	}
}

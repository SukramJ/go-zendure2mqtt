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

	"github.com/SukramJ/go-hamqtt/publisher"
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

	// HARuntime owns this daemon's Home Assistant plane: the retained
	// discovery configs it has published, the bridge availability marker
	// its Last Will clears, the Home Assistant birth subscription and the
	// orphan sweep. Required.
	//
	// Built at the composition root for the same reason as [Deps.StatePlane]
	// — it states QoS 0 — and additionally because [publisher.Runtime.Will]
	// has to be read before the MQTT client is constructed: the will is part
	// of CONNECT.
	HARuntime *publisher.Runtime

	// StatePlane writes every point's retained state value. Required.
	//
	// It is built at the composition root (cmd/zendure2mqtt) rather than
	// here, because the one thing it has to be told is the quality of
	// service and that is a statement about an installed base rather than
	// about this package. The wiring states [publisher.QoSAtMostOnce] —
	// QoS 0 — because that is what every release of this bridge has
	// published at; the library's own default is QoS 1 and the zero value
	// of its config would take it silently.
	StatePlane *publisher.StatePublisher
}

// Coordinator wires a backend to the MQTT broker.
type Coordinator struct {
	deps   Deps
	root   string
	logger *slog.Logger

	runCtx   context.Context //nolint:containedctx // captured for the subscription handler
	switches []virtual.Switch

	snMu sync.RWMutex // guards bySN (written by onReading, read by handleSet — different goroutines)
	bySN map[string]source.Device

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
		switches:    virtual.Switches(deps.Cfg.ChargeActiveW(), deps.Cfg.DischargeActiveW()),
		lastDiscSig: map[string]string{},
	}
}

// CommandFilter is the MQTT filter this bridge subscribes for inbound
// commands, `<root>/+/+/+/set`.
//
// Exported because the composition root needs the same string for
// [publisher.StateConfig.CommandFilters], which refuses a state publish that
// would land inside this process's own command subscription and be echoed
// straight back into [Coordinator.handleSet]. One formula, two readers: the
// library's own guard exists because consumers wrote the filter twice and the
// copies drifted.
//
// Note what it does not cover, unchanged: a five-level pack command topic
// (`<root>/<sn>/battery/<packSN>/<leaf>/set`) matches neither this filter nor
// handleSet's five-part check, so a writable pack property would publish an
// unroutable command_topic — F3 of the ADR 0070 phase-5 measurement, latent
// because 0 of the 7 pack properties is writable, and deliberately left
// as-is here.
func CommandFilter(root string) string { return root + "/+/+/+/set" }

// BridgeStatusTopic is this daemon's own availability topic,
// `<root>/bridge/status`: the topic its Last Will clears, the topic
// PublishOnline and PublishOffline write, and the topic every published
// entity names as its availability_topic.
//
// Exported because those four readers used to be four string literals, and
// the library refuses a Config.StatusTopic that disagrees with its Layout's
// Bridge() precisely because a typo there greys out an entire fleet with
// nothing on the wire naming the cause. One formula, checked against
// harender.Layout.Bridge in TestBridgeStatusTopicIsOneString.
func BridgeStatusTopic(root string) string { return root + "/bridge/status" }

// Run subscribes to command topics and drives the backend until ctx ends.
func (c *Coordinator) Run(ctx context.Context) error {
	c.runCtx = ctx
	c.PublishOnline(ctx)

	setFilter := CommandFilter(c.root)
	if _, err := c.deps.MQTT.Subscribe(ctx, setFilter, mqtt.QoS0, c.handleSet); err != nil {
		// A failed initial subscribe is not replayed on later reconnects (the
		// client rolls back the registration), so without a retry every /set
		// command would be silently dropped until restart. Retry in the
		// background until it lands or the daemon stops.
		c.logger.Warn("coordinator.subscribe_failed", slog.String("filter", setFilter), slog.String("err", err.Error()))
		go c.retrySubscribe(ctx, "coordinator.subscribe", setFilter, c.subscribeCommands)
	}

	// Home Assistant's own birth message. Discovery configs are retained, so
	// an HA restart is survived without this — but a broker restarted without
	// persistence, or one whose retained store is cleared, leaves Home
	// Assistant with no entities at all until this daemon is restarted, and
	// nothing in the logs says so. That was F6 of the ADR 0070 phase-5
	// measurement: all four of this bridge's Subscribe sites were accounted
	// for and none was <hass_base>/status.
	//
	// WatchBirth replays the configs this runtime declared on the rising edge
	// of that message, off the read loop — the replay is one blocking
	// retained publish per config, each waiting on an acknowledgement only
	// the read loop could deliver, so doing it inline would self-deadlock.
	// Only with discovery enabled: there is nothing to replay otherwise.
	if c.deps.HASS != nil {
		if err := c.watchBirth(ctx); err != nil {
			c.logger.Warn("coordinator.birth_subscribe_failed", slog.String("err", err.Error()))
			go c.retrySubscribe(ctx, "coordinator.birth_subscribe", publisher.BirthTopic(c.deps.HARuntime.Prefix()), c.watchBirth)
		}
	}

	return c.deps.Backend.Run(ctx, func(r source.Reading) {
		c.onReading(ctx, r)
	})
}

// setSubscribeMaxBackoff caps the /set re-subscribe retry interval.
const setSubscribeMaxBackoff = 30 * time.Second

// retrySubscribe re-issues one subscription with capped backoff until it
// succeeds or ctx is cancelled. A duplicate success is idempotent (the client
// replaces the handler in place), and once registered go-mqtt replays it across
// all later reconnects.
//
// It takes the subscribe call rather than the filter because both of this
// daemon's Home Assistant subscriptions need it for the same reason and the
// birth one goes through the library: a failed initial subscribe is not
// replayed on a later reconnect, so without a retry the command plane would be
// silently dead — and the birth resync silently absent — until a restart.
func (c *Coordinator) retrySubscribe(ctx context.Context, event, filter string, subscribe func(context.Context) error) {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if err := subscribe(ctx); err == nil {
			c.logger.Info(event+"_recovered", slog.String("filter", filter))
			return
		}
		backoff = min(backoff*2, setSubscribeMaxBackoff)
	}
}

// subscribeCommands registers the /set handler.
func (c *Coordinator) subscribeCommands(ctx context.Context) error {
	_, err := c.deps.MQTT.Subscribe(ctx, CommandFilter(c.root), mqtt.QoS0, c.handleSet)
	return err
}

// watchBirth registers the Home Assistant birth subscription.
func (c *Coordinator) watchBirth(ctx context.Context) error {
	return c.deps.HARuntime.WatchBirth(ctx)
}

// PublishOnline (re)announces bridge availability. Wired to OnConnect.
func (c *Coordinator) PublishOnline(ctx context.Context) {
	// A (re)connect may be to a broker that came back without its retained
	// store, in which case the dedup gate would suppress every value it
	// believes is already there and leave every entity blank until its next
	// change — which on chargeMaxLimit or packNum is never. Reset opens the
	// gate without forgetting the index, so the next poll writes the fleet
	// once and is deduped again afterwards. The poll is the snapshot pass
	// the library's Reset documentation asks a consumer to pair it with.
	c.deps.StatePlane.Reset()

	// The same reopening on the discovery plane, and it closes a window
	// whose only symptom is silence. publisher.Runtime memoises a superseded
	// per-entity config as cleared the moment Transport.Publish returns nil
	// — and this bridge publishes its discovery plane at QoS 0, where that
	// return means Write and Flush returned and says nothing about a broker.
	// The memo is per process; the success it records is per connection. So
	// a retraction written to a socket that is already going away is
	// recorded as done, the document that follows it fails and is correctly
	// withheld, and after the link comes back the retry skips every
	// memoised retraction and publishes the document into a tree that still
	// holds the old per-entity configs. Home Assistant answers that with one
	// `WARNING [mqtt.entity] Received a conflicting MQTT discovery message`
	// line and nothing else: no error on the wire, none in this log, and the
	// entities do not appear. Measured on this fleet at 22 of 29
	// (TestRetractionsAreReSentAfterAReconnect).
	//
	// Reset rather than a rebuilt Runtime — the shape three sibling bridges
	// adopted and the one the library recommends — because Declared() is
	// load-bearing here: sweepOrphans subtracts it from what a window
	// judged, and it is what keeps a second device's documents, and this
	// device's own while a report is transiently shrunken, from being
	// retracted as orphans. A rebuilt runtime starts with that set empty.
	// Reset clears exactly the per-connection half (the superseded memo and
	// the payload dedup gate) and keeps Declared and Claimed, which are
	// statements about the process rather than about a connection.
	c.deps.HARuntime.Reset()

	if err := c.deps.HARuntime.AnnounceOnline(ctx); err != nil {
		c.logger.Warn("coordinator.online_failed", slog.String("err", err.Error()))
	}
}

// PublishOffline marks the bridge offline on a graceful shutdown. The LWT
// only fires on an ungraceful disconnect (crash / network drop), so a clean
// stop must announce offline explicitly or the retained status stays online.
func (c *Coordinator) PublishOffline(ctx context.Context) {
	if err := c.deps.HARuntime.AnnounceOffline(ctx); err != nil {
		c.logger.Warn("coordinator.offline_failed", slog.String("err", err.Error()))
	}
}

// onReading resolves a report and publishes every point (plus discovery).
func (c *Coordinator) onReading(ctx context.Context, r source.Reading) {
	c.snMu.Lock()
	if _, ok := c.bySN[r.Device.SN]; !ok {
		c.bySN[r.Device.SN] = r.Device // learn devices discovered at runtime (cloud)
	}
	c.snMu.Unlock()
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
		// Clear any of our own retained discovery configs for this device
		// that we no longer publish, so they do not linger as unavailable
		// ghost entities in Home Assistant. Two kinds qualify since the
		// device-document migration: a document for a device that is gone,
		// and a per-entity config from a release before the migration whose
		// entity is no longer in the new document — PublishBundle retracts
		// the ones that are.
		c.reconcileOrphans(ctx, dev.SN, published)
	}
	written := 0
	for _, p := range points {
		if c.publishState(ctx, process.StateTopic(c.root, dev.SN, p), formatValue(p.Value)) {
			written++
		}
	}
	// written is reported beside points because the gap between them is the
	// measurable effect of the dedup gate: this bridge polls every 15s and
	// re-reported nearly every value unchanged, so a steady-state device
	// should show written=0 and an operator should be able to see that.
	c.logger.Debug("coordinator.published",
		slog.String("sn", dev.SN), slog.Int("points", len(points)), slog.Int("written", written))
}

// publishState writes one point's value through the state plane and reports
// whether it actually reached the broker.
//
// The dedup gate inside [publisher.StatePublisher.Publish] is the whole point
// of routing through it: a value byte-identical to the one the broker already
// retains is not written again. Nothing about the topic, the payload, the
// retain flag or the QoS changes — the payload is still [formatValue]'s
// bytes, deliberately, because the library's own [publisher.RenderRawValue]
// renders a Go bool as "true"/"false" where this bridge has always published
// "1"/"0", and a state plane migration is not the place to change a payload.
//
// An empty payload is routed to Evict rather than to Publish. [formatValue]
// renders an empty string value as zero bytes, and an empty retained payload
// is MQTT's retraction rather than a state — which is what this bridge has
// always done with it, by accident. Evict is the same three wire values
// (empty payload, retained, QoS 0) said on purpose, and it drops the topic
// from the dedup index so the next real value is not compared against a
// retraction.
func (c *Coordinator) publishState(ctx context.Context, topic string, payload []byte) bool {
	if len(payload) == 0 {
		if err := c.deps.StatePlane.Evict(ctx, topic); err != nil {
			c.logger.Warn("coordinator.state_evict_failed",
				slog.String("topic", topic), slog.String("err", err.Error()))
			return false
		}
		return true
	}
	written, err := c.deps.StatePlane.Publish(ctx, topic, payload)
	if err != nil {
		c.logger.Warn("coordinator.publish_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return false
	}
	return written
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
// The snapshot spans the whole discovery prefix because the serial is not its
// own MQTT level; ownership and device scoping are enforced in code, twice —
// on the topic by [hass.OwnsDeviceConfigTopic] and on the payload by
// [hass.Discovery.IsOwnConfig] — so other integrations' and other devices'
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
	// daemon-lifetime runCtx still bounds it (the guard below, and the sweep's
	// own window).
	bgCtx := context.WithoutCancel(ctx)
	// The daemon-lifetime context is read here and handed to the goroutine
	// rather than read inside it: runCtx is written once, by Run, before the
	// backend that calls this exists, so a synchronous read is safe while a
	// concurrent one is a race waiting for someone to reassign it.
	runCtx := c.runCtx
	go func() {
		defer c.reconciling.Delete(sn)
		if runCtx.Err() != nil {
			return
		}
		c.sweepOrphans(bgCtx, sn, published)
	}()
}

// sweepOrphans runs one report-only snapshot of the discovery tree for a
// single device and retracts whatever it owns and no longer publishes.
//
// Report-only, and then retracted by this caller, deliberately. The library's
// retracting pass judges a topic on [publisher.SweepRequest.Owns] alone, which
// sees the parsed topic and nothing else — and this daemon's ownership rule
// has always been the stronger one: the retained *payload* must carry a
// unique_id in this bridge's namespace and a state topic under its MQTT root.
// A pass that retracted on the topic namespace alone would be a widening of
// what this daemon is willing to delete from a shared discovery tree, inside
// a step whose whole claim is that nothing changed.
//
// What the library does own here is everything that was hard: one snapshot
// window at a time per runtime (two windows on one filter leave the second
// handler installed over the first and the first teardown unsubscribes for
// both, after which both report nothing), the parsing of all three config
// topic forms, and the claim check that keeps a config still inside its own
// publish call from being judged an orphan.
//
// [publisher.Runtime.Retract] also gives the Forget behaviour for free on the
// runtime's side: a retracted topic leaves the declared set, so a wrongly
// swept entity — a transiently shrunken report — is published again on the
// next report rather than staying deleted for the process lifetime. The
// matching half of internal/hass's own sent-set is still cleared by hand.
func (c *Coordinator) sweepOrphans(ctx context.Context, sn string, published map[string]bool) {
	prefix := c.deps.HARuntime.Prefix()
	var (
		mu    sync.Mutex
		owned []string
	)
	_, err := c.deps.HARuntime.Sweep(ctx, publisher.SweepRequest{
		ReportOnly: true,
		Window:     reconcileCollectWindow,
		Owns: func(t publisher.ConfigTopic) bool {
			return hass.OwnsDeviceConfigTopic(c.root, sn, t)
		},
		Inspect: func(t publisher.ConfigTopic, body []byte) {
			// Called on the transport's read loop: cheap, and it publishes
			// nothing. The retraction happens after Sweep returns.
			if !c.deps.HASS.IsOwnConfig(body) {
				return // another writer's config in the same namespace
			}
			// Rebuilt through the library's own renderers rather than by
			// string concatenation, for both forms this daemon owns. The
			// per-entity one is the same function PublishBundle retracts
			// with — 29 of 29 of this bridge's pre-migration configs,
			// measured — so a topic the sweep judges and a topic the
			// migration clears cannot be formatted differently.
			topic := publisher.BundleConfigTopic(prefix, t.NodeID)
			if !t.Bundle {
				topic = publisher.LegacyTopicByUniqueID(publisher.LegacyEntity{
					Prefix:   prefix,
					Platform: t.Platform,
					UniqueID: t.ObjectID,
				})
			}
			if topic == "" {
				return
			}
			mu.Lock()
			owned = append(owned, topic)
			mu.Unlock()
		},
	})
	if err != nil {
		c.logger.Warn("coordinator.reconcile_sweep_failed",
			slog.String("sn", sn), slog.String("err", err.Error()))
		return
	}

	// Two claim sets, and both are needed. published is what this device's
	// current report minted, which is the question an orphan actually
	// answers. declared is what the runtime has written for the whole
	// process — a second device's configs, and this device's own while a
	// report is transiently shrunken — and subtracting it is the safety net
	// the library's own retracting pass applies and a report-only pass does
	// not: Owned lists every topic the window judged, claimed or not.
	claimed := make(map[string]bool, len(published))
	for topic := range published {
		claimed[topic] = true
	}
	for _, topic := range c.deps.HARuntime.Declared() {
		claimed[topic] = true
	}
	mu.Lock()
	orphans := make([]string, 0, len(owned))
	for _, topic := range owned {
		if !claimed[topic] {
			orphans = append(orphans, topic)
		}
	}
	mu.Unlock()
	if len(orphans) == 0 {
		return
	}

	if err := c.deps.HARuntime.Retract(ctx, orphans...); err != nil {
		c.logger.Warn("coordinator.reconcile_clear_failed",
			slog.String("sn", sn), slog.String("err", err.Error()))
	}
	// Invalidate the cleared configs in discovery so a still-live entity
	// wrongly cleared by a transiently shrunken report is republished on the
	// next report instead of staying deleted until restart. Unconditional,
	// including after a partial failure: republishing a config the broker
	// still holds costs one deduped write, while leaving it forgotten by
	// neither side costs the entity.
	c.deps.HASS.Forget(orphans)
	c.logger.Info("coordinator.discovery_orphans_cleared",
		slog.String("sn", sn), slog.Int("count", len(orphans)))
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
func (c *Coordinator) handleSet(msg *mqtt.Message) {
	parts := strings.Split(msg.Topic, "/")
	if len(parts) != 5 || parts[0] != c.root || parts[4] != "set" {
		return
	}
	sn, leaf := parts[1], parts[3]
	c.snMu.RLock()
	dev, ok := c.bySN[sn]
	c.snMu.RUnlock()
	if !ok {
		c.logger.Warn("coordinator.set_unknown_device", slog.String("sn", sn))
		return
	}
	if c.handleSwitchSet(dev, leaf, string(msg.Payload)) {
		return // handled by a virtual switch
	}
	entry, ok := c.deps.Catalog.ByTopic(leaf)
	if !ok || !entry.Writable {
		c.logger.Warn("coordinator.set_not_writable", slog.String("topic", leaf))
		return
	}
	value, ok := decodeCommand(entry, string(msg.Payload))
	if !ok {
		c.logger.Warn("coordinator.set_rejected",
			slog.String("sn", sn), slog.String("property", entry.Property), slog.String("payload", string(msg.Payload)))
		return
	}
	// Write off the read loop: go-mqtt dispatches handlers synchronously, so a
	// slow/unreachable device would otherwise stall all inbound dispatch and can
	// trip the keep-alive watchdog.
	go func() {
		ctx, cancel := context.WithTimeout(c.runCtx, 15*time.Second)
		defer cancel()
		if err := c.deps.Backend.Write(ctx, dev, map[string]any{entry.Property: value}); err != nil {
			c.logger.Warn("coordinator.write_failed",
				slog.String("sn", sn), slog.String("property", entry.Property), slog.String("err", err.Error()))
			return
		}
		c.logger.Info("coordinator.write", slog.String("sn", sn), slog.String("property", entry.Property))
		c.reReadSoon(dev)
	}()
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
		// Off the read loop — see handleSet.
		go func() {
			ctx, cancel := context.WithTimeout(c.runCtx, 15*time.Second)
			defer cancel()
			if err := c.deps.Backend.Write(ctx, dev, sw.WriteProps(on)); err != nil {
				c.logger.Warn("coordinator.switch_write_failed",
					slog.String("sn", dev.SN), slog.String("switch", leaf), slog.String("err", err.Error()))
				return
			}
			c.logger.Info("coordinator.switch_write",
				slog.String("sn", dev.SN), slog.String("switch", leaf), slog.Bool("on", on))
			c.reReadSoon(dev)
		}()
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
// expects, returning ok=false when the payload must be rejected. A select
// label (English or German) maps back to its integer code. A numeric value is
// clamped to the catalog's advertised min/max (HA enforces those only in its
// own UI, not for other publishers), then converted to the device's raw units
// by inverting the read scaling (read is (raw-offset)/scale, so write is
// value*scale+offset) and rounded to an integer — Zendure properties are
// integer-valued. NaN/Inf and out-of-int64-range values are rejected so garbage
// never reaches the hardware. Non-numeric, non-label payloads stay strings.
func decodeCommand(entry catalog.Entry, payload string) (any, bool) {
	payload = strings.TrimSpace(payload)
	if code, ok := entry.CodeForLabel(payload); ok {
		if i, err := strconv.Atoi(code); err == nil {
			return i, true
		}
		return code, true
	}
	if f, err := strconv.ParseFloat(payload, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		// Bounds are expressed in display units, so clamp before un-scaling.
		if entry.Min != nil && f < *entry.Min {
			f = *entry.Min
		}
		if entry.Max != nil && f > *entry.Max {
			f = *entry.Max
		}
		if entry.Scale != 0 {
			f *= entry.Scale
		}
		f += entry.Offset
		if f < math.MinInt64 || f > math.MaxInt64 {
			return nil, false
		}
		return int(math.Round(f)), true
	}
	return payload, true
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

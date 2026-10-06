// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SukramJ/go-hamqtt/topic"
)

// The retained sweep of openccu-loom ADR 0083 ("Migration"): 0.10.0 moved
// every topic, and the retained values the old layout left on the broker
// would otherwise stand there forever. It runs on every start for the life of
// the 0.x line that introduced the new layout — it is idempotent, and it also
// cleans up after a rollback and re-upgrade — and goes with the next breaking
// release.
//
// What it clears, and the rules that keep it from clearing anything else:
//
//   - It looks only under the old root, [config.Config.IdentityRoot]: the
//     configured MQTT_TOPIC, or "zendure2mqtt" when the key is unset — which
//     is not the new default name.
//   - It listens per identifier it owns: `<old>/bridge/status` for the
//     instance, `<old>/<sn>/#` for each device serial it knows. A sibling
//     instance's devices are never subscribed, so never seen.
//   - It clears only retained messages, and only the exact old shapes
//     ([oldLayoutTopic]); never a prefix match. Anything else under the
//     device's tree stays.
//   - A topic whose second level is a function name (`status`, `set`, …) is
//     new by definition and never touched. Serials cannot spell one, and a
//     serial that did would be skipped whole.

// migrateWindow bounds how long one sweep listens for retained messages; the
// broker delivers them right after the subscription is acknowledged.
const migrateWindow = 2 * time.Second

// oldShapeSuffixes are the leaves the old layout put after an item.
var oldShapeSuffixes = map[string]bool{"state": true, "set": true}

// oldLayoutTopic reports whether t is a topic of the pre-0.10.0 layout for
// device sn under root:
//
//	<root>/<sn>/<group>/<topic>/state             (and …/set)
//	<root>/<sn>/battery/<packSN>/<topic>/state    (and …/set)
func oldLayoutTopic(root, sn, t string) bool {
	if topic.IsFunction(sn) {
		return false
	}
	rest, ok := strings.CutPrefix(t, root+"/"+sn+"/")
	if !ok {
		return false
	}
	levels := strings.Split(rest, "/")
	for _, l := range levels {
		if l == "" {
			return false
		}
	}
	switch len(levels) {
	case 3:
		return oldShapeSuffixes[levels[2]]
	case 4:
		return levels[0] == "battery" && oldShapeSuffixes[levels[3]]
	default:
		return false
	}
}

// oldBridgeStatus is the pre-0.10.0 Last Will topic, replaced by
// `<name>/connected`.
func oldBridgeStatus(root string) string { return root + "/bridge/status" }

// migrateBridge clears the old `<old>/bridge/status`.
func (c *Coordinator) migrateBridge(ctx context.Context) {
	exact := oldBridgeStatus(c.identity)
	n, err := c.sweepRetained(ctx, exact, func(t string) bool { return t == exact })
	c.logMigration(exact, n, err)
}

// migrateDevice sweeps the old layout of one device, once per process. A
// failed sweep is forgotten so the device's next report tries again.
func (c *Coordinator) migrateDevice(ctx context.Context, sn string) {
	if sn == "" || topic.IsFunction(sn) || strings.ContainsAny(sn, "/+#") {
		return
	}
	if _, done := c.migrated.LoadOrStore(sn, struct{}{}); done {
		return
	}
	go func() {
		if err := c.sweepDevice(ctx, sn); err != nil && ctx.Err() == nil {
			c.migrated.Delete(sn)
		}
	}()
}

// sweepDevice clears the old layout of one device.
func (c *Coordinator) sweepDevice(ctx context.Context, sn string) error {
	root := c.identity
	filter := root + "/" + sn + "/#"
	n, err := c.sweepRetained(ctx, filter, func(t string) bool { return oldLayoutTopic(root, sn, t) })
	c.logMigration(filter, n, err)
	return err
}

func (c *Coordinator) logMigration(filter string, cleared int, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// Shutting down inside the window; the next start sweeps again.
	case err != nil:
		c.logger.Warn("coordinator.migration_sweep_failed",
			slog.String("filter", filter), slog.String("err", err.Error()))
	case cleared > 0:
		c.logger.Info("coordinator.migration_sweep",
			slog.String("filter", filter), slog.Int("cleared", cleared))
	}
}

// sweepRetained subscribes filter for [migrateWindow] (a test may shorten
// it), collects the retained
// topics owned accepts, unsubscribes, and clears each with an empty retained
// payload. It returns how many it cleared.
func (c *Coordinator) sweepRetained(ctx context.Context, filter string, owned func(string) bool) (int, error) {
	var (
		mu     sync.Mutex
		found  []string
		seen   = map[string]bool{}
		closed atomic.Bool
	)
	collect := func(t string, payload []byte, retained bool) {
		// Called on the transport's read loop: cheap, and it publishes
		// nothing. An empty payload is a topic already cleared.
		if closed.Load() || !retained || len(payload) == 0 || !owned(t) {
			return
		}
		mu.Lock()
		if !seen[t] {
			seen[t] = true
			found = append(found, t)
		}
		mu.Unlock()
	}
	if err := c.tr.Subscribe(ctx, filter, 0, collect); err != nil {
		return 0, err
	}
	window := c.migrateWindow
	if window <= 0 {
		window = migrateWindow
	}
	timer := time.NewTimer(window)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
	}
	closed.Store(true)

	// Its own context: the caller's may be cancelled, which is exactly when
	// leaving a wildcard subscription installed would do the most harm.
	teardown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	if err := c.tr.Unsubscribe(teardown, filter); err != nil {
		c.logger.Warn("coordinator.migration_unsubscribe_failed",
			slog.String("filter", filter), slog.String("err", err.Error()))
	}
	cancel()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	mu.Lock()
	targets := append([]string(nil), found...)
	mu.Unlock()
	cleared := 0
	for _, t := range targets {
		if err := c.tr.Publish(ctx, t, nil, 0, true); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

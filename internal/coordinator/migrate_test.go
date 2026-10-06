// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestOldLayoutTopicIsExact pins the shape test of the migration sweep: the
// two pre-0.10.0 item shapes with their state/set leaf, for one serial under
// one root, and nothing else — never a prefix.
func TestOldLayoutTopicIsExact(t *testing.T) {
	const root, sn = "zendure2mqtt", "SF1"
	for _, c := range []struct {
		topic string
		want  bool
	}{
		{"zendure2mqtt/SF1/now/electric_level/state", true},
		{"zendure2mqtt/SF1/config/ac_mode/set", true},
		{"zendure2mqtt/SF1/battery/PACK1/soc_level/state", true},
		{"zendure2mqtt/SF1/misc/someProp/state", true},
		{"zendure2mqtt/SF1/now/electric_level", false},              // no leaf
		{"zendure2mqtt/SF1/now/electric_level/state/x", false},      // deeper
		{"zendure2mqtt/SF1/now/x/attributes", false},                // another leaf
		{"zendure2mqtt/SF1/config/PACK1/soc_level/state", false},    // four levels, not battery
		{"zendure2mqtt/SF1//x/state", false},                        // empty level
		{"zendure2mqtt/SF12/now/electric_level/state", false},       // a serial we merely prefix
		{"zendure2mqtt/SF2/now/electric_level/state", false},        // another device
		{"zendure2mqtt/status/SF1/now/electric_level", false},       // the new layout
		{"zendure2mqtt-garage/SF1/now/electric_level/state", false}, // another root
		{"zendure2mqtt/garage/SF1/now/x/state", false},              // a nested sibling root
	} {
		if got := oldLayoutTopic(root, sn, c.topic); got != c.want {
			t.Errorf("oldLayoutTopic(%s) = %v, want %v", c.topic, got, c.want)
		}
	}
	// A serial that spells a function name is never swept: its tree would
	// be the new layout's.
	if oldLayoutTopic(root, "status", "zendure2mqtt/status/now/x/state") {
		t.Error("a function-name serial was judged an old topic")
	}
}

// TestMigrationSweepClearsOnlyThisInstancesOldTopics drives the retained
// sweep of openccu-loom ADR 0083 against a broker holding this instance's
// old tree beside the topics that must survive it: a sibling instance's
// device under the same old root, a sibling on another root, topics of no
// old shape under this device, and the new layout. It runs twice, because the
// sweep runs on every start and must find nothing the second time.
func TestMigrationSweepClearsOnlyThisInstancesOldTopics(t *testing.T) {
	sn := goldenUnit().SN
	old := func(rest string) string { return testIdentity + "/" + sn + "/" + rest }
	ours := []string{
		testIdentity + "/bridge/status",
		old("now/electric_level/state"),
		old("config/ac_mode/state"),
		old("config/ac_mode/set"), // somebody's `mosquitto_pub -r`
		old("battery/AO4H2301X01/soc_level/state"),
		old("misc/someRawProp/state"),
	}
	survivors := []string{
		testIdentity + "/SF2400AC0099999/now/electric_level/state", // a sibling's device, same old root
		testIdentity + "-garage/" + sn + "/now/electric_level/state",
		testIdentity + "/garage/bridge/status",
		old("now/electric_level"),
		old("notes"),
		old("now/electric_level/state/history"),
		testIdentity + "/status/" + sn + "/now/electric_level", // a name equal to the old root
		testName + "/status/" + sn + "/now/electric_level",
		testName + "/connected",
		testName + "/bridge/status", // not the old root: never touched
	}
	retained := map[string][]byte{}
	for _, topic := range append(append([]string(nil), ours...), survivors...) {
		retained[topic] = []byte("x")
	}
	rig := newBrokerRig(t, retained)

	sweep := func() {
		var wg sync.WaitGroup
		wg.Go(func() { rig.coord.migrateBridge(t.Context()) })
		wg.Go(func() {
			if err := rig.coord.sweepDevice(t.Context(), sn); err != nil {
				t.Errorf("sweepDevice: %v", err)
			}
		})
		wg.Wait()
	}

	sweep()
	cleared := clearedTopics(rig.broker)
	want := append([]string(nil), ours...)
	sort.Strings(want)
	if strings.Join(cleared, "\n") != strings.Join(want, "\n") {
		t.Errorf("cleared\n %v\nwant\n %v", cleared, want)
	}
	for _, topic := range survivors {
		if _, ok := rig.broker.retainedPayload(topic); !ok {
			t.Errorf("%s was cleared; it is not this instance's old layout", topic)
		}
	}
	if f := rig.broker.filters(); len(f) != 0 {
		t.Errorf("the sweep left subscriptions installed: %v", f)
	}

	before := len(rig.broker.publishes())
	sweep()
	if after := len(rig.broker.publishes()); after != before {
		t.Errorf("the second sweep published %d messages, want none", after-before)
	}
}

// TestMigrationRunsOncePerDevice pins the once-per-process guard, and that a
// serial which cannot be a topic level or spells a function name is never
// swept at all.
func TestMigrationRunsOncePerDevice(t *testing.T) {
	rig := newBrokerRig(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // the sweep goroutine returns at once; only the guard is under test

	rig.coord.migrateDevice(ctx, "SF1")
	if _, ok := rig.coord.migrated.Load("SF1"); !ok {
		t.Fatal("migrateDevice did not record SF1")
	}
	for _, sn := range []string{"", "status", "a/b", "a+b"} {
		rig.coord.migrateDevice(ctx, sn)
		if _, ok := rig.coord.migrated.Load(sn); ok {
			t.Errorf("migrateDevice accepted serial %q", sn)
		}
	}
}

// clearedTopics lists the empty retained publishes a broker saw, sorted.
func clearedTopics(b *fakeBroker) []string {
	var out []string
	for _, w := range b.publishes() {
		if w.Retain && len(w.Payload) == 0 {
			out = append(out, w.Topic)
		}
	}
	sort.Strings(out)
	return out
}

// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package fabric

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	bdbidef "github.com/SukramJ/go-fabric/cluster/spec/bridgeddevicebasicinformation"
	descriptordef "github.com/SukramJ/go-fabric/cluster/spec/descriptor"
	epmdef "github.com/SukramJ/go-fabric/cluster/spec/electricalpowermeasurement"
	psdef "github.com/SukramJ/go-fabric/cluster/spec/powersource"
	ptdef "github.com/SukramJ/go-fabric/cluster/spec/powertopology"
	"github.com/SukramJ/go-fabric/controller/client"
	"github.com/SukramJ/go-fabric/device"
	"github.com/SukramJ/go-fabric/fabrictest"
	"github.com/SukramJ/go-fabric/node"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// fakeDevices is a backend's device list a test changes.
type fakeDevices struct {
	mu   sync.Mutex
	devs []source.Device
}

func (f *fakeDevices) list() []source.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.devs)
}

func (f *fakeDevices) add(d source.Device) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs = append(f.devs, d)
}

// site is a commissioned pair whose device node serves the projection, the
// way [Bridge] serves it: the backend's devices added before the node starts.
type site struct {
	*fabrictest.Site
	pair *fabrictest.Pair
	p    *projection
	ctx  context.Context
}

func newSite(t *testing.T, devs *fakeDevices) *site {
	t.Helper()
	s := &site{Site: fabrictest.NewSite(t, fabrictest.Options{})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s.ctx = ctx
	s.pair = s.CommissionedPair(fabrictest.PairOptions{Device: fabrictest.Topology{
		Prepare: func(n *node.Node) {
			s.p = newProjection(n.Aggregator(), devs.list, slog.Default())
			s.p.addKnown(ctx)
		},
	}})
	return s
}

// endpoint is the endpoint the node gave sn's unit.
func (s *site) endpoint(t *testing.T, sn string) uint16 {
	t.Helper()
	s.p.mu.Lock()
	u := s.p.units[sn]
	s.p.mu.Unlock()
	if u == nil {
		t.Fatalf("no unit for %s", sn)
	}
	ep, ok := u.dev.EndpointNumber()
	if !ok {
		t.Fatalf("unit %s has no endpoint", sn)
	}
	return ep
}

// report is a unit's telemetry with the three properties the projection
// reads.
func report(sn string, level, gridIn, homeOut float64) *model.Report {
	return &model.Report{SN: sn, Properties: map[string]any{
		"electricLevel": level, "gridInputPower": gridIn, "outputHomePower": homeOut,
	}}
}

// act runs a reading on the site, as the backend's poll would.
func (s *site) act(t *testing.T, r source.Reading) {
	t.Helper()
	if err := s.pair.Device.Act(func(*node.Node) error { s.p.reading(s.ctx, r); return nil }); err != nil {
		t.Fatal(err)
	}
}

var unitA = source.Device{SN: "HOA1", DeviceName: "Garage store", Model: "solarflow 2400 ac", Address: "192.0.2.10"}

// TestUnitIsABridgedElectricalSensor: a configured unit is a bridged
// ElectricalSensor (0x0510) under the Aggregator, named as Home Assistant
// names it, serving PowerTopology, ElectricalPowerMeasurement and the
// battery's PowerSource.
func TestUnitIsABridgedElectricalSensor(t *testing.T) {
	s := newSite(t, &fakeDevices{devs: []source.Device{unitA}})
	ep := s.endpoint(t, unitA.SN)

	values, err := fabrictest.ReadCluster(s.ctx, s.pair, ep, descriptordef.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := descriptordef.StateFrom(values)
	types := make([]uint32, 0, len(desc.DeviceTypeList))
	for _, dt := range desc.DeviceTypeList {
		types = append(types, dt.DeviceType)
	}
	if !slices.Equal(types, []uint32{uint32(device.ElectricalSensorID), uint32(device.BridgedNodeID)}) {
		t.Errorf("DeviceTypeList %#04x, want ElectricalSensor and BridgedNode", types)
	}

	values, err = fabrictest.ReadCluster(s.ctx, s.pair, ep, bdbidef.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := bdbidef.StateFrom(values); info.NodeLabel != "Garage store" || !info.Reachable {
		t.Errorf("BridgedDeviceBasicInformation NodeLabel %q, Reachable %v", info.NodeLabel, info.Reachable)
	}
	for _, id := range []uint32{ptdef.ClusterID, epmdef.ClusterID, psdef.ClusterID} {
		if !slices.Contains(desc.ServerList, id) {
			t.Errorf("ServerList %#04x lacks %#04x", desc.ServerList, id)
		}
	}
}

// TestLateUnitJoinsTheRunningNode: a unit the backend learns after the start
// (a cloud login) is added to the running node once its upstream is usable,
// and the set is then marked complete.
func TestLateUnitJoinsTheRunningNode(t *testing.T) {
	devs := &fakeDevices{devs: []source.Device{unitA}}
	s := newSite(t, devs)
	late := source.Device{SN: "HOA2"}
	devs.add(late)
	if err := s.pair.Device.Act(func(*node.Node) error { s.p.markComplete(s.ctx); return nil }); err != nil {
		t.Fatal(err)
	}
	ep := s.endpoint(t, late.SN)
	values, err := fabrictest.ReadCluster(s.ctx, s.pair, ep, bdbidef.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := bdbidef.StateFrom(values); info.NodeLabel != "Zendure HOA2" {
		t.Errorf("NodeLabel %q, want the Home Assistant name", info.NodeLabel)
	}
	if !s.p.complete {
		t.Error("the set is not marked complete")
	}
}

// TestReadingsReachTheController: a reading sets BatPercentRemaining (in
// half-percent) and ActivePower (grid input less home output, in mW); a
// second reading reaches a controller's subscription.
func TestReadingsReachTheController(t *testing.T) {
	s := newSite(t, &fakeDevices{devs: []source.Device{unitA}})
	ep := s.endpoint(t, unitA.SN)
	s.act(t, source.Reading{Device: unitA, Report: report(unitA.SN, 80, 0, 600)})

	values, err := fabrictest.ReadCluster(s.ctx, s.pair, ep, psdef.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if ps, _ := psdef.StateFrom(values); ps.BatPercentRemaining == nil || *ps.BatPercentRemaining != 160 {
		t.Fatalf("BatPercentRemaining %v, want 160 (80 %%)", ps.BatPercentRemaining)
	}
	values, err = fabrictest.ReadCluster(s.ctx, s.pair, ep, epmdef.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if pm, _ := epmdef.StateFrom(values); pm.ActivePower == nil || *pm.ActivePower != -600_000 {
		t.Fatalf("ActivePower %v, want -600000 mW (600 W exported)", pm.ActivePower)
	}

	reports := make(chan *fabrictest.Report, 16)
	sub, err := fabrictest.Subscribe(s.ctx, s.pair, fabrictest.SubscribeRequest{
		Attributes: []fabrictest.AttributePath{
			client.AttributeAt(ep, psdef.ClusterID, psdef.AttrBatPercentRemaining),
			client.AttributeAt(ep, epmdef.ClusterID, epmdef.AttrActivePower),
		},
		Updated: func(r *fabrictest.Report) { reports <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	s.act(t, source.Reading{Device: unitA, Report: report(unitA.SN, 81, 1200, 0)})
	var level, power bool
	seen := func() bool {
		for {
			select {
			case r := <-reports:
				if ps, _ := psdef.StateFrom(r.Values(ep, psdef.ClusterID)); ps.BatPercentRemaining != nil && *ps.BatPercentRemaining == 162 {
					level = true
				}
				if pm, _ := epmdef.StateFrom(r.Values(ep, epmdef.ClusterID)); pm.ActivePower != nil && *pm.ActivePower == 1_200_000 {
					power = true
				}
			default:
				return level && power
			}
		}
	}
	if err := s.Eventually(s.ctx, "the second reading", seen); err != nil {
		t.Fatalf("%v (level %v, power %v)", err, level, power)
	}
}

func TestActivePower(t *testing.T) {
	for _, c := range []struct {
		props map[string]any
		want  int64
		ok    bool
	}{
		{map[string]any{"gridInputPower": 1200.0, "outputHomePower": 0.0}, 1_200_000, true},
		{map[string]any{"outputHomePower": 800.0}, -800_000, true},
		{map[string]any{"electricLevel": 50.0}, 0, false},
		{map[string]any{"gridInputPower": "n/a"}, 0, false},
	} {
		got, ok := activePower(c.props)
		if got != c.want || ok != c.ok {
			t.Errorf("activePower(%v) = %d, %v; want %d, %v", c.props, got, ok, c.want, c.ok)
		}
	}
}

// TestReachabilityFlips: DeviceReachable(false) sets the unit's Reachable
// false, and a controller's subscription sees it; an unusable upstream makes
// every unit unreachable, a usable one restores each unit's own word.
func TestReachabilityFlips(t *testing.T) {
	s := newSite(t, &fakeDevices{devs: []source.Device{unitA}})
	ep := s.endpoint(t, unitA.SN)
	reports := make(chan *fabrictest.Report, 16)
	sub, err := fabrictest.Subscribe(s.ctx, s.pair, fabrictest.SubscribeRequest{
		Attributes: []fabrictest.AttributePath{client.AttributeAt(ep, bdbidef.ClusterID, bdbidef.AttrReachable)},
		Updated:    func(r *fabrictest.Report) { reports <- r },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sees := func(want bool) func() bool {
		return func() bool {
			for {
				select {
				case r := <-reports:
					if _, ok := r.Value(ep, bdbidef.ClusterID, bdbidef.AttrReachable); !ok {
						continue
					}
					if info, _ := bdbidef.StateFrom(r.Values(ep, bdbidef.ClusterID)); info.Reachable == want {
						return true
					}
				default:
					return false
				}
			}
		}
	}
	reachable := func() bool {
		t.Helper()
		values, err := fabrictest.ReadCluster(s.ctx, s.pair, ep, bdbidef.ClusterID)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := bdbidef.StateFrom(values)
		return info.Reachable
	}
	act := func(fn func()) {
		t.Helper()
		if err := s.pair.Device.Act(func(*node.Node) error { fn(); return nil }); err != nil {
			t.Fatal(err)
		}
	}

	act(func() { s.p.deviceReachable(s.ctx, unitA, false) })
	if err := s.Eventually(s.ctx, "Reachable false", sees(false)); err != nil {
		t.Fatal(err)
	}
	act(func() { s.p.deviceReachable(s.ctx, unitA, true) })
	if !reachable() {
		t.Fatal("unit unreachable after DeviceReachable(true)")
	}
	act(func() { s.p.upstreamUsable(s.ctx, false) })
	if reachable() {
		t.Fatal("unit reachable while the upstream is down")
	}
	act(func() { s.p.deviceReachable(s.ctx, unitA, true) })
	if reachable() {
		t.Fatal("a device report made the unit reachable while the upstream is down")
	}
	act(func() { s.p.upstreamUsable(s.ctx, true) })
	if !reachable() {
		t.Fatal("unit unreachable after the upstream is back")
	}
}

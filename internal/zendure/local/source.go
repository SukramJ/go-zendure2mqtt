// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package local

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// DeviceConfig is one statically configured local device. (mDNS discovery
// will populate the same shape automatically in a later milestone.)
type DeviceConfig struct {
	SN         string
	Host       string
	DeviceName string
	Model      string
}

// Backend polls one or more local Zendure devices over HTTP and writes
// properties back via the same API. It satisfies [source.Backend].
type Backend struct {
	devices  []source.Device
	bySN     map[string]source.Device
	interval time.Duration
	http     *http.Client
	logger   *slog.Logger

	observer  source.Observer
	reachMu   sync.Mutex
	reachable map[string]bool // sn -> answered its latest poll
}

// New builds a local backend for the configured devices.
func New(cfgs []DeviceConfig, interval time.Duration, logger *slog.Logger) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	b := &Backend{
		interval:  interval,
		http:      &http.Client{Timeout: DefaultHTTPTimeout},
		logger:    logger,
		bySN:      make(map[string]source.Device, len(cfgs)),
		reachable: make(map[string]bool, len(cfgs)),
	}
	for _, c := range cfgs {
		dev := source.Device{SN: c.SN, DeviceID: c.SN, DeviceName: c.DeviceName, Model: c.Model, Address: c.Host}
		b.devices = append(b.devices, dev)
		b.bySN[c.SN] = dev
	}
	return b
}

// Observe implements [source.Observable].
func (b *Backend) Observe(o source.Observer) { b.observer = o }

// observe records one poll outcome and tells the observer. The upstream of
// the local transport is the devices themselves, so it is usable while at
// least one of them answered its latest poll.
func (b *Backend) observe(dev source.Device, ok bool) {
	if b.observer == nil {
		return
	}
	b.reachMu.Lock()
	b.reachable[dev.SN] = ok
	usable := false
	for _, r := range b.reachable {
		usable = usable || r
	}
	b.reachMu.Unlock()
	b.observer.DeviceReachable(dev, ok)
	b.observer.UpstreamUsable(usable)
}

// Devices implements [source.Source].
func (b *Backend) Devices() []source.Device { return b.devices }

// Run polls every configured device on the interval until ctx is
// cancelled. Each device gets its own goroutine; a failed poll is logged
// and retried on the next tick (publish-what-you-can resilience).
func (b *Backend) Run(ctx context.Context, onReading source.Handler) error {
	if len(b.devices) == 0 {
		b.logger.Warn("local.no_devices",
			slog.String("hint", "configure LOCAL_DEVICES in config.yaml (SN + HOST)"))
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	for _, dev := range b.devices {
		wg.Add(1)
		go func(dev source.Device) {
			defer wg.Done()
			b.pollLoop(ctx, dev, onReading)
		}(dev)
	}
	wg.Wait()
	return nil
}

// pollLoop fetches one device immediately, then every interval.
func (b *Backend) pollLoop(ctx context.Context, dev source.Device, onReading source.Handler) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	b.pollOnce(ctx, dev, onReading)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.pollOnce(ctx, dev, onReading)
		}
	}
}

// pollOnce fetches a single report and forwards it.
func (b *Backend) pollOnce(ctx context.Context, dev source.Device, onReading source.Handler) {
	report, err := FetchReport(ctx, b.http, dev.Address)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down; not a statement about the device
		}
		b.logger.Warn("local.poll_failed", slog.String("sn", dev.SN), slog.String("err", err.Error()))
		b.observe(dev, false)
		return
	}
	if report.SN == "" {
		report.SN = dev.SN
	}
	b.observe(dev, true)
	onReading(source.Reading{Device: dev, Report: report})
}

// Write implements [source.Controller].
func (b *Backend) Write(ctx context.Context, dev source.Device, props map[string]any) error {
	return WriteProperties(ctx, b.http, dev.Address, dev.SN, props)
}

// Read implements [source.Source]: a one-shot HTTP fetch for write feedback.
func (b *Backend) Read(ctx context.Context, dev source.Device) (*model.Report, error) {
	report, err := FetchReport(ctx, b.http, dev.Address)
	if err != nil {
		return nil, err
	}
	if report.SN == "" {
		report.SN = dev.SN
	}
	return report, nil
}

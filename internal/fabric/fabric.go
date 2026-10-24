// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package fabric projects the daemon's Zendure devices onto Matter: an
// in-process go-fabric node whose Aggregator serves one bridged endpoint per
// device, so a Matter controller (Apple Home, Home Assistant's Matter
// integration, …) shows the device's charge level and power. It is built
// only with MATTER_ENABLED; the MQTT side does not depend on it.
//
// It imports go-fabric's facade alone — node, device, contract and the
// typed cluster packages — and nothing below it; TestFabricImportsOnlyTheFacade
// holds that. The bridge is an uncertified test device: it carries the CSA
// test vendor and go-fabric's test attestation chain.
package fabric

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/SukramJ/go-fabric/node"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/version"
)

// Product identity the node reports in BasicInformation.
const (
	vendorName  = "go-zendure2mqtt"
	productName = "zendure2mqtt bridge"
)

// spake2Salt is the SPAKE2+ salt of the node's passcode verifier (16 to 32
// bytes). The salt is not a secret; the passcode is.
var spake2Salt = []byte("go-zendure2mqtt-spake2p")

// spake2Iterations is the verifier's PBKDF2 iteration count, the value
// go-fabric's reference bridge uses.
const spake2Iterations = 1000

// Bridge is the daemon's Matter node: built by [New] from the config, started
// with the daemon and stopped with it, with the backend's devices under its
// Aggregator.
type Bridge struct {
	n      *node.Node
	p      *projection
	logger *slog.Logger
}

// New builds the node from the MATTER_* block. devices is the backend's
// device list ([source.Source.Devices]), read at [Bridge.Start] and once the
// backend's upstream is usable. It does not bind a socket yet;
// [Bridge.Start] does. An error is a startup error of the daemon.
func New(cfg *config.Config, devices func() []source.Device, logger *slog.Logger) (*Bridge, error) {
	if logger == nil {
		logger = slog.Default()
	}
	n, err := node.New(NodeConfig(cfg, logger)) //nolint:contextcheck // the node's constructor takes no context
	if err != nil {
		return nil, fmt.Errorf("matter: %w", err)
	}
	return &Bridge{n: n, p: newProjection(n.Aggregator(), devices, logger), logger: logger}, nil
}

// NodeConfig is the node's configuration from the MATTER_* block: the listen
// address, the commissioning values, the identity, the store file and the
// daemon's logger, so the node logs through the daemon's handler and level.
func NodeConfig(cfg *config.Config, logger *slog.Logger) node.Config {
	return node.Config{
		Network: node.Network{Listen: cfg.MatterListen},
		Commissioning: node.Commissioning{
			Passcode:        uint32(cfg.MatterPasscode),             //nolint:gosec // validated 1..99999998
			Discriminator:   uint16(cfg.MatterDiscriminatorValue()), //nolint:gosec // validated 0..4095
			PBKDFIterations: spake2Iterations,
			Salt:            spake2Salt,
		},
		ProductDescription: node.ProductDescription{
			VendorID:  uint16(cfg.MatterVendorID),  //nolint:gosec // validated 1..65535
			ProductID: uint16(cfg.MatterProductID), //nolint:gosec // validated 1..65535
		},
		BasicInformation: node.BasicInformation{
			VendorName:            vendorName,
			ProductName:           productName,
			NodeLabel:             cfg.MatterLabel,
			SoftwareVersion:       softwareVersion(version.Version),
			SoftwareVersionString: version.Version,
		},
		Store:  node.Store{Path: cfg.MatterStorePath},
		Logger: logger,
	}
}

// softwareVersion encodes "X.Y.Z" as X·10⁶ + Y·10³ + Z, monotonic across
// releases; anything else (a development build) is 0.
func softwareVersion(v string) uint32 {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return 0
	}
	var out uint32
	for _, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil || n > 999 {
			return 0
		}
		out = out*1000 + uint32(n)
	}
	return out
}

// Start adds every device the backend lists now and starts the node: it
// binds MATTER_LISTEN, advertises over mDNS and, while no controller has
// paired, opens the commissioning window.
func (b *Bridge) Start(ctx context.Context) error {
	b.p.addKnown(ctx)
	if err := b.n.Start(ctx); err != nil {
		return fmt.Errorf("matter: %w", err)
	}
	return nil
}

// Stop stops the node and closes its store.
func (b *Bridge) Stop(ctx context.Context) error {
	return b.n.Stop(ctx)
}

// Reading implements the coordinator's projection: the reading's unit, added
// on first sight, takes the report's power and charge level.
func (b *Bridge) Reading(ctx context.Context, r source.Reading) { b.p.reading(ctx, r) }

// UpstreamUsable implements the coordinator's projection: once the backend's
// upstream is usable its device list is complete, and the node is told so.
func (b *Bridge) UpstreamUsable(usable bool) {
	if usable {
		b.p.markComplete(context.Background())
	}
}

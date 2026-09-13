// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command zendure2mqtt is the standalone daemon that bridges Zendure
// devices (locally via the on-board HTTP API / zenSDK, or via the Zendure
// cloud) to MQTT, including optional Home Assistant discovery.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"
	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/coordinator"
	"github.com/SukramJ/go-zendure2mqtt/internal/harender"
	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/state"
	"github.com/SukramJ/go-zendure2mqtt/internal/version"
	"github.com/SukramJ/go-zendure2mqtt/internal/web"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/cloud"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/local"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (default: search standard locations)")
	catalogPath := flag.String("catalog", "zendure.yaml", "path to the property catalog")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(*configPath, *catalogPath, logger); err != nil {
		logger.Error("zendure2mqtt.fatal", slog.String("err", err.Error()))
		os.Exit(1)
	}
}

// run wires dependencies and blocks until the context is cancelled
// (SIGINT/SIGTERM) or a component fails.
func run(configPath, catalogPath string, logger *slog.Logger) error {
	logger.Info("zendure2mqtt.boot", slog.String("build", version.String()))

	cfg, err := loadConfig(configPath, logger)
	if err != nil {
		return err
	}
	if cfg.Debug {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
		slog.SetDefault(logger)
	}

	cat, err := catalog.LoadFile(catalogPath)
	if err != nil {
		return err
	}
	logger.Info("zendure2mqtt.catalog_loaded",
		slog.String("path", catalogPath), slog.Int("entries", len(cat.Entries())))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// --- Backend (local HTTP polling or cloud) ---
	backend := buildBackend(cfg, logger)

	// --- Home Assistant runtime (LWT, birth, retained configs, sweep) ---
	//
	// Built before the MQTT client, because the Last Will is part of CONNECT
	// and the will is this runtime's statement: Will() returns the same topic
	// and the same payload AnnounceOnline and AnnounceOffline write, so the
	// bridge structurally cannot configure a will no published entity
	// references — the measured defect of two sibling bridges, where a hard
	// crash writes "offline" where nothing reads it and every entity stays
	// available forever. The client it publishes through does not exist yet,
	// so the transport is wired in below, before anything connects.
	//
	// Every field of its config — the discovery prefix, this bridge's own
	// status topic, QoS 0, and the per-entity topic form the migration
	// retracts — is stated once, in coordinator.HARuntimeConfig, and the
	// coordinator's constructor refuses a runtime that was not built from it.
	// It used to be spelled here and again in the test fixtures, with nothing
	// comparing the two; see that function for what the divergence cost.
	haLink := &deferredTransport{}
	haRuntime := publisher.New(haLink, coordinator.HARuntimeConfig(cfg, logger))
	will, err := haRuntime.Will()
	if err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}

	// --- MQTT (output broker) ---
	mqttClient := mqtt.NewTCPClient(mqtt.TCPConfig{
		BrokerURL: fmt.Sprintf("tcp://%s:%d", cfg.MQTTServer, cfg.MQTTPort),
		ClientID:  config.MQTTClientID,
		Username:  cfg.MQTTLogin,
		Password:  cfg.MQTTPassword,
		Will: &mqtt.Will{
			Topic:   will.Topic,
			Payload: will.Payload,
			QoS:     mqtt.QoS(will.QoS),
			Retain:  will.Retain,
		},
		CleanStart: true,
		Logger:     logger,
	})
	lifecycle := mqtt.NewLifecycle(mqtt.LifecycleConfig{Logger: logger}, mqttClient)
	// Circuit breaker between the bridge and the output broker: during a
	// degraded-broker phase (TCP link up, acks missing) publishes fail
	// fast with mqtt.ErrCircuitOpen instead of each stalling on the ack
	// timeout, and bounded half-open probes test recovery. Defaults: 5
	// consecutive broker-side failures open the circuit, recovery is
	// probed after 30s. The lifecycle's reconnect loop stays in charge
	// of the link itself.
	breaker := mqtt.NewBreaker(mqttClient, mqtt.BreakerConfig{
		OnStateChange: func(from, to mqtt.BreakerState) {
			logger.Warn("zendure2mqtt.mqtt_breaker_state",
				slog.String("from", from.String()),
				slog.String("to", to.String()))
		},
	})
	// The runtime publishes through the breaker and subscribes around it, for
	// the same reason the coordinator's client is split: breaking the
	// subscribe path would only delay resubscription after a reconnect.
	haLink.wire(hagomqtt.Split(breaker, mqttClient))

	if err := lifecycle.Start(ctx); err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = lifecycle.Stop(stopCtx)
	}()

	// --- State plane ---
	//
	// Both QoS fields, the payload encoding and the command-collision filter
	// are stated once, in coordinator.HAStateConfig; see it for why each is a
	// statement and not a default. As with the runtime above, this used to be
	// spelled here and again in the test fixture with nothing comparing them.
	statePlane := publisher.NewStatePublisher(hagomqtt.Split(breaker, mqttClient),
		coordinator.HAStateConfig(cfg.MQTTTopic, logger))

	// --- HA discovery (optional) ---
	var hassDiscovery *hass.Discovery
	if cfg.HASSEnable {
		// Through the runtime rather than straight to the client: the runtime
		// claims each config topic, and that claim is what the orphan sweep
		// compares against and what the birth resync replays.
		hassDiscovery = hass.New(cfg.HASSBaseTopic, cfg.MQTTTopic,
			harender.Renderer{Root: cfg.MQTTTopic, Lang: cfg.Language}, haRuntime, logger)
	}

	// --- Diagnostic web UI state cache (only when the web UI is enabled) ---
	var store *state.Store
	if cfg.WebEnable {
		store = state.New()
	}

	// --- Coordinator ---
	coord := coordinator.New(coordinator.Deps{
		Cfg:        cfg,
		Backend:    backend,
		MQTT:       mqtt.SplitClient(breaker, mqttClient),
		Catalog:    cat,
		HASS:       hassDiscovery,
		State:      store,
		Logger:     logger,
		HARuntime:  haRuntime,
		StatePlane: statePlane,
	})
	lifecycle.OnConnect(func(cctx context.Context) { coord.PublishOnline(cctx) })

	logger.Info("zendure2mqtt.starting",
		slog.String("connection", cfg.Connection), slog.String("mqtt", cfg.MQTTServer),
		slog.Bool("hass", cfg.HASSEnable), slog.Bool("web", cfg.WebEnable), slog.String("lang", cfg.Language))

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return coord.Run(gctx) })

	if cfg.WebEnable {
		srv := web.New(web.Deps{
			Cfg:           cfg,
			Store:         store,
			MQTTConnected: mqttClient.IsConnected,
			Logger:        logger,
		})
		g.Go(func() error { return srv.Run(gctx) })
	}

	err = g.Wait()

	// Drain a birth-triggered config replay before announcing offline, so a
	// resync in flight cannot write "online"-era configs after the shutdown
	// marker.
	haRuntime.Close()

	// Graceful shutdown: explicitly mark the bridge offline (the LWT only
	// fires on an ungraceful disconnect) before the deferred MQTT stop.
	offCtx, offCancel := context.WithTimeout(context.Background(), 2*time.Second)
	coord.PublishOffline(offCtx)
	offCancel()
	return err
}

// buildBackend constructs the transport backend selected by CONNECTION.
func buildBackend(cfg *config.Config, logger *slog.Logger) source.Backend {
	if cfg.IsCloud() {
		if !cfg.CloudConfigured() {
			logger.Warn("zendure2mqtt.cloud_token_missing",
				slog.String("hint", "set CLOUD_APP_TOKEN (from the Zendure app); the bridge stays idle until then"))
		}
		return cloud.New(cfg.CloudAppToken, cfg.CloudTLSVerify, logger)
	}
	devices := make([]local.DeviceConfig, 0, len(cfg.LocalDevices))
	for _, d := range cfg.LocalDevices {
		devices = append(devices, local.DeviceConfig{SN: d.SN, Host: d.Host, DeviceName: d.DeviceName, Model: d.Model})
	}
	return local.New(devices, cfg.RefreshDuration(), logger)
}

// loadConfig resolves the config path (explicit flag or standard search)
// and loads it with environment overrides applied.
func loadConfig(configPath string, logger *slog.Logger) (*config.Config, error) {
	env := config.OSEnv{}
	path := configPath
	if path == "" {
		if located, ok := config.Locate(env); ok {
			path = located
		}
	}
	if path == "" {
		cfg, err := config.Load(strings.NewReader(""), env)
		if err != nil {
			return nil, err
		}
		logger.Info("zendure2mqtt.config_loaded", slog.String("path", "(environment only)"))
		return cfg, nil
	}
	cfg, err := config.LoadFile(path, env)
	if err != nil {
		return nil, err
	}
	logger.Info("zendure2mqtt.config_loaded", slog.String("path", path))
	return cfg, nil
}

// errTransportNotWired is returned by a [deferredTransport] used before its
// client was supplied. A programming error, reported rather than panicked
// because the caller is a publish path and the daemon losing one config
// message is better than the daemon dying.
var errTransportNotWired = errors.New("mqtt: transport used before the client was wired")

// deferredTransport is a [publisher.Transport] whose client is supplied after
// construction.
//
// It exists for one ordering constraint, and it is a real one: the Last Will
// is part of CONNECT, so the MQTT client must be built with it — while the
// will itself is [publisher.Runtime.Will]'s answer, which is what makes the
// will's topic and the availability topic every entity references provably
// one string. One of the two has to be built first, and making it the runtime
// is what keeps the will a single statement instead of a literal here that
// has to agree with a literal in the library.
//
// wire is called before the lifecycle connects, so nothing can reach a method
// here beforehand. The field is guarded anyway: once connected it is read
// from the transport's read loop (the birth subscription) and from the poll
// path at the same time.
type deferredTransport struct {
	mu sync.RWMutex
	tr publisher.Transport
}

// wire supplies the transport. Calling it twice is a programming error and
// the last call wins; nothing in this daemon does.
func (d *deferredTransport) wire(tr publisher.Transport) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tr = tr
}

func (d *deferredTransport) target() (publisher.Transport, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.tr == nil {
		return nil, errTransportNotWired
	}
	return d.tr, nil
}

// Publish implements [publisher.Transport].
func (d *deferredTransport) Publish(ctx context.Context, topic string, payload []byte, qos byte, retain bool) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Publish(ctx, topic, payload, qos, retain)
}

// Subscribe implements [publisher.Transport].
func (d *deferredTransport) Subscribe(ctx context.Context, filter string, qos byte, h publisher.Handler) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Subscribe(ctx, filter, qos, h)
}

// Unsubscribe implements [publisher.Transport].
func (d *deferredTransport) Unsubscribe(ctx context.Context, filter string) error {
	tr, err := d.target()
	if err != nil {
		return err
	}
	return tr.Unsubscribe(ctx, filter)
}

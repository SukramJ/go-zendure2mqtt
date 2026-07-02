// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Command zendure2mqtt is the standalone daemon that bridges Zendure
// devices (locally via the on-board HTTP API / zenSDK, or via the Zendure
// cloud) to MQTT, including optional Home Assistant discovery.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/coordinator"
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

	// --- MQTT (output broker) ---
	statusTopic := cfg.MQTTTopic + "/bridge/status"
	mqttClient := mqtt.NewTCPClient(mqtt.TCPConfig{
		BrokerURL:    fmt.Sprintf("tcp://%s:%d", cfg.MQTTServer, cfg.MQTTPort),
		ClientID:     config.MQTTClientID,
		Username:     cfg.MQTTLogin,
		Password:     cfg.MQTTPassword,
		WillTopic:    statusTopic,
		WillPayload:  []byte("offline"),
		WillRetain:   true,
		CleanSession: true,
		Logger:       logger,
	})
	lifecycle := mqtt.NewLifecycle(mqtt.LifecycleConfig{Logger: logger}, mqttClient)
	if err := lifecycle.Start(ctx); err != nil {
		return fmt.Errorf("mqtt: %w", err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = lifecycle.Stop(stopCtx)
	}()

	// --- HA discovery (optional) ---
	var discovery *hass.Discovery
	if cfg.HASSEnable {
		discovery = hass.New(cfg.HASSBaseTopic, cfg.MQTTTopic, cfg.Language, mqttClient, logger)
	}

	// --- Diagnostic web UI state cache (only when the web UI is enabled) ---
	var store *state.Store
	if cfg.WebEnable {
		store = state.New()
	}

	// --- Coordinator ---
	coord := coordinator.New(coordinator.Deps{
		Cfg:     cfg,
		Backend: backend,
		MQTT:    mqttClient,
		Catalog: cat,
		HASS:    discovery,
		State:   store,
		Logger:  logger,
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
		devices = append(devices, local.DeviceConfig{SN: d.SN, Host: d.Host, Model: d.Model})
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

// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package cloud

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// Errors returned by the cloud backend.
var (
	// ErrNotImplemented marks paths the cloud transport does not support
	// (the push-based stream has no one-shot read).
	ErrNotImplemented = errors.New("cloud: not implemented")
	// ErrNotConnected is returned by Write before the cloud MQTT session is up.
	ErrNotConnected = errors.New("cloud: not connected")
)

// reportSubtopic is the trailing topic segment carrying device telemetry.
const reportSubtopic = "properties/report"

// Backend is the Zendure cloud transport: a signed REST login discovers the
// devices and the cloud broker credentials, then an MQTT/TLS session streams
// telemetry (subscribe) and accepts control (publish). It satisfies
// [source.Backend].
type Backend struct {
	token     string
	tlsVerify bool
	http      *http.Client
	logger    *slog.Logger

	mu        sync.RWMutex
	devices   []source.Device
	byID      map[string]source.Device // deviceId → device (topic routing)
	creds     MQTTCredentials
	client    *mqtt.TCPClient
	onReading source.Handler
	msgID     atomic.Int64
}

// New builds a cloud backend from the operator's app token. tlsVerify enables
// strict certificate verification for the cloud broker (off by default — see
// [config.Config.CloudTLSVerify]).
func New(token string, tlsVerify bool, logger *slog.Logger) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{
		token:     token,
		tlsVerify: tlsVerify,
		http:      &http.Client{Timeout: 20 * time.Second},
		logger:    logger,
		byID:      map[string]source.Device{},
	}
}

// login decodes the token and fetches the device list + broker credentials.
func (b *Backend) login(ctx context.Context) error {
	apiURL, appKey, err := DecodeToken(b.token)
	if err != nil {
		return err
	}
	res, err := Login(ctx, b.http, apiURL, appKey)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.creds = res.MQTT
	b.devices = b.devices[:0]
	b.byID = make(map[string]source.Device, len(res.DeviceList))
	for _, d := range res.DeviceList {
		dev := source.Device{SN: d.SnNumber, DeviceID: d.DeviceKey, ProductKey: d.ProductKey, Model: d.ProductModel, Address: d.IP}
		b.devices = append(b.devices, dev)
		b.byID[d.DeviceKey] = dev
	}
	return nil
}

// Devices implements [source.Source]. Populated by the login in Run.
func (b *Backend) Devices() []source.Device {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]source.Device, len(b.devices))
	copy(out, b.devices)
	return out
}

// Run logs in, opens the cloud MQTT/TLS session and streams telemetry to
// onReading until ctx is cancelled. A login or connect failure is logged and
// the backend stays idle (non-fatal) so the output broker keeps running.
func (b *Backend) Run(ctx context.Context, onReading source.Handler) error {
	if b.token == "" {
		b.logger.Warn("cloud.token_missing",
			slog.String("hint", "set CLOUD_APP_TOKEN (from the Zendure app); the cloud backend stays idle until then"))
		<-ctx.Done()
		return nil
	}
	if err := b.login(ctx); err != nil {
		b.logger.Error("cloud.login_failed", slog.String("err", err.Error()))
		<-ctx.Done()
		return nil
	}
	b.onReading = onReading

	b.mu.RLock()
	creds := b.creds
	b.mu.RUnlock()
	host, port := creds.HostPort()

	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: !b.tlsVerify} //nolint:gosec // Zendure's cloud cert is non-standard; opt in to strict via CLOUD_TLS_VERIFY
	if !b.tlsVerify {
		b.logger.Warn("cloud.tls_insecure",
			slog.String("hint", "Zendure cloud cert is non-standard; TLS verification disabled (connection stays encrypted). Set CLOUD_TLS_VERIFY: true to enforce."))
	}
	client := mqtt.NewTCPClient(mqtt.TCPConfig{
		BrokerURL:    fmt.Sprintf("tls://%s:%d", host, port),
		ClientID:     creds.ClientID,
		Username:     creds.Username,
		Password:     creds.Password,
		TLSConfig:    tlsCfg,
		CleanSession: true,
		Logger:       b.logger,
	})
	b.mu.Lock()
	b.client = client
	b.mu.Unlock()

	b.logger.Info("cloud.connecting",
		slog.String("broker", host), slog.Int("port", port), slog.Int("devices", len(b.Devices())))
	b.connectLoop(ctx, client)
	return nil
}

// Reconnect timings for the cloud session.
const (
	minReconnect    = 1 * time.Second
	maxReconnect    = 30 * time.Second
	sessionStableAt = 60 * time.Second
)

// connectLoop keeps the cloud MQTT session alive with a stability-aware,
// event-driven backoff: connect → subscribe → block on the connection-lost
// signal. On a drop it reconnects after a backoff that grows on rapid,
// repeated drops (a broker that keeps closing us) and resets to the minimum
// once a session stays up long enough to count as stable — so an occasional
// drop recovers within ~1 s while a flapping broker is throttled to ~30 s.
func (b *Backend) connectLoop(ctx context.Context, client *mqtt.TCPClient) {
	backoff := minReconnect
	for {
		if ctx.Err() != nil {
			return
		}
		if err := client.Connect(ctx); err != nil {
			b.logger.Warn("cloud.connect_failed", slog.String("err", err.Error()))
		} else {
			connectedAt := time.Now()
			b.subscribeAll(ctx)
			select {
			case <-ctx.Done():
				// ctx is already cancelled here; detach from its cancellation
				// (keeping its values) so the graceful disconnect still gets
				// its own 3 s budget.
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				_ = client.Disconnect(stopCtx)
				cancel()
				return
			case <-client.ConnectionLost():
				up := time.Since(connectedAt)
				b.logger.Warn("cloud.connection_lost", slog.Duration("up", up))
				if up >= sessionStableAt {
					backoff = minReconnect // stable session → reconnect promptly
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxReconnect)
	}
}

// subscribeAll (re)subscribes to every device's topic tree. Both the `iot/`
// and bare-prefix forms are subscribed, matching the Zendure cloud.
func (b *Backend) subscribeAll(ctx context.Context) {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()
	if client == nil {
		return
	}
	for _, dev := range b.Devices() {
		for _, filter := range []string{
			fmt.Sprintf("iot/%s/%s/#", dev.ProductKey, dev.DeviceID),
			fmt.Sprintf("/%s/%s/#", dev.ProductKey, dev.DeviceID),
		} {
			if err := client.Subscribe(ctx, filter, mqtt.QoS0, b.handleMessage); err != nil {
				b.logger.Warn("cloud.subscribe_failed", slog.String("filter", filter), slog.String("err", err.Error()))
			}
		}
	}
	b.logger.Info("cloud.subscribed", slog.Int("devices", len(b.Devices())))
}

// handleMessage routes an inbound cloud message: only telemetry reports are
// turned into readings.
func (b *Backend) handleMessage(topic string, payload []byte, _ bool) {
	deviceID, sub := deviceAndSub(topic)
	if sub != reportSubtopic {
		return
	}
	b.mu.RLock()
	dev, ok := b.byID[deviceID]
	handler := b.onReading
	b.mu.RUnlock()
	if !ok || handler == nil {
		return
	}
	report, err := model.ParseReport(payload)
	if err != nil {
		b.logger.Warn("cloud.parse_failed", slog.String("device", deviceID), slog.String("err", err.Error()))
		return
	}
	if report.SN == "" {
		report.SN = dev.SN
	}
	handler(source.Reading{Device: dev, Report: report})
}

// deviceAndSub extracts the deviceId and sub-topic from a Zendure cloud topic
// of the form (iot/|/)?{productKey}/{deviceId}/{sub...}.
func deviceAndSub(topic string) (deviceID, sub string) {
	t := strings.TrimPrefix(topic, "/")
	t = strings.TrimPrefix(t, "iot/")
	parts := strings.SplitN(t, "/", 3)
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

// Write implements [source.Controller]: publishes a control message to the
// device's cloud write topic.
func (b *Backend) Write(ctx context.Context, dev source.Device, props map[string]any) error {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()
	if client == nil {
		return ErrNotConnected
	}
	payload, err := json.Marshal(model.WriteRequest{
		DeviceID:   dev.DeviceID,
		MessageID:  b.msgID.Add(1),
		Timestamp:  time.Now().Unix(),
		Properties: props,
	})
	if err != nil {
		return fmt.Errorf("cloud: marshal write: %w", err)
	}
	topic := fmt.Sprintf("iot/%s/%s/properties/write", dev.ProductKey, dev.DeviceID)
	return client.Publish(ctx, topic, payload, mqtt.QoS0, false)
}

// Read implements [source.Source]. The cloud transport is push-based (the
// device echoes changes over MQTT), so there is no one-shot read.
func (b *Backend) Read(_ context.Context, _ source.Device) (*model.Report, error) {
	return nil, ErrNotImplemented
}

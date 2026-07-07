// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package local implements the local Zendure transport: the device's
// on-board HTTP API as documented by the zenSDK
// (GET /properties/report, POST /properties/write). No authentication is
// required on the LAN.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// DefaultHTTPTimeout bounds a single request to a device.
const DefaultHTTPTimeout = 10 * time.Second

// maxBodyBytes caps a device HTTP response. Real reports are a few KB; the
// client Timeout bounds duration but not bytes, so without this a malfunctioning
// or spoofed LAN peer (the transport is unauthenticated plain HTTP) could stream
// gigabytes into memory and OOM the daemon.
const maxBodyBytes = 4 << 20 // 4 MiB

// FetchReport performs GET http://<host>/properties/report and parses it.
func FetchReport(ctx context.Context, hc *http.Client, host string) (*model.Report, error) {
	url := fmt.Sprintf("http://%s/properties/report", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("local: build request: %w", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local: GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("local: read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("local: GET %s: response exceeds %d bytes", url, maxBodyBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local: GET %s: status %d", url, resp.StatusCode)
	}
	return model.ParseReport(body)
}

// WriteProperties performs POST http://<host>/properties/write with the
// {sn, properties} body the device expects.
func WriteProperties(ctx context.Context, hc *http.Client, host, sn string, props map[string]any) error {
	url := fmt.Sprintf("http://%s/properties/write", host)
	payload, err := json.Marshal(model.WriteRequest{SN: sn, Properties: props})
	if err != nil {
		return fmt.Errorf("local: marshal write: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("local: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("local: POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("local: POST %s: status %d", url, resp.StatusCode)
	}
	return nil
}

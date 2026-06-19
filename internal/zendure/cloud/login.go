// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package cloud implements the Zendure cloud transport.
//
// The flow mirrors the Zendure Home Assistant integration: the operator
// pastes an "app token" obtained from the Zendure app; it base64-decodes
// to "<api_url>.<appKey>". A signed POST to {api_url}/api/ha/deviceList
// returns the device list and the cloud MQTT broker credentials. Those
// credentials are then used for the MQTT telemetry/control stream (wired
// in a later milestone).
package cloud

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // SHA1 is mandated by the Zendure cloud sign scheme
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cloud sign constants, matching the Zendure HA integration's request
// scheme. The HA app key and client id are fixed app-side values.
const (
	haKey    = "C*dafwArEOXK"
	clientID = "zenHa"
)

// MQTTCredentials are the cloud broker connection parameters returned by
// the login. Url has the form "host:port".
type MQTTCredentials struct {
	ClientID string `json:"clientId"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// HostPort splits Url into host and port, defaulting to 8883 (the cloud
// broker's TLS port) when no port is present.
func (m MQTTCredentials) HostPort() (string, int) {
	host, port, found := strings.Cut(m.URL, ":")
	if !found {
		return m.URL, 8883
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return host, 8883
	}
	return host, p
}

// CloudDevice is one device entry from the login response.
type CloudDevice struct {
	DeviceKey    string `json:"deviceKey"`
	DeviceName   string `json:"deviceName"`
	SnNumber     string `json:"snNumber"`
	ProductKey   string `json:"productKey"`
	ProductModel string `json:"productModel"`
	IP           string `json:"ip"`
}

// LoginResult is the decoded {mqtt, deviceList} payload.
type LoginResult struct {
	MQTT       MQTTCredentials `json:"mqtt"`
	DeviceList []CloudDevice   `json:"deviceList"`
}

// DecodeToken base64-decodes the app token into its API URL and app key.
// The decoded form is "<api_url>.<appKey>"; the API URL itself contains
// dots, so the split is on the LAST separator (Python's rsplit(".", 1)).
func DecodeToken(token string) (apiURL, appKey string, err error) {
	raw, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if derr != nil {
		return "", "", fmt.Errorf("cloud: decode token: %w", derr)
	}
	decoded := strings.TrimSpace(string(raw))
	i := strings.LastIndex(decoded, ".")
	if i <= 0 || i == len(decoded)-1 {
		return "", "", fmt.Errorf("cloud: token must decode to '<api_url>.<appKey>'")
	}
	return decoded[:i], decoded[i+1:], nil
}

// sign builds the SHA1(haKey + sorted(params) + haKey) signature, upper-hex.
func sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params[k])
	}
	sum := sha1.Sum([]byte(haKey + b.String() + haKey)) //nolint:gosec // scheme-mandated
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Login performs the signed POST {apiURL}/api/ha/deviceList and returns the
// device list and MQTT credentials.
func Login(ctx context.Context, hc *http.Client, apiURL, appKey string) (*LoginResult, error) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := strconv.Itoa(rand.Intn(90000) + 10000) //nolint:gosec // nonce, not security-critical

	signature := sign(map[string]string{
		"appKey":    appKey,
		"timestamp": timestamp,
		"nonce":     nonce,
	})

	body, err := json.Marshal(map[string]string{"appKey": appKey})
	if err != nil {
		return nil, fmt.Errorf("cloud: marshal body: %w", err)
	}
	url := strings.TrimRight(apiURL, "/") + "/api/ha/deviceList"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cloud: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("timestamp", timestamp)
	req.Header.Set("nonce", nonce)
	req.Header.Set("clientid", clientID)
	req.Header.Set("sign", signature)

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud: POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cloud: read body: %w", err)
	}

	var envelope struct {
		Code int         `json:"code"`
		Msg  string      `json:"msg"`
		Data LoginResult `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("cloud: parse response: %w", err)
	}
	if envelope.Code != 200 {
		return nil, fmt.Errorf("cloud: login failed: code=%d msg=%q", envelope.Code, envelope.Msg)
	}
	return &envelope.Data, nil
}

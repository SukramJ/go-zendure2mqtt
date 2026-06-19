// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package cloud_test

import (
	"encoding/base64"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/cloud"
)

func TestDecodeToken(t *testing.T) {
	token := base64.StdEncoding.EncodeToString([]byte("https://app.zendure.tech.MYAPPKEY123"))
	apiURL, appKey, err := cloud.DecodeToken(token)
	if err != nil {
		t.Fatalf("DecodeToken: %v", err)
	}
	if apiURL != "https://app.zendure.tech" {
		t.Errorf("apiURL = %q, want https://app.zendure.tech", apiURL)
	}
	if appKey != "MYAPPKEY123" {
		t.Errorf("appKey = %q, want MYAPPKEY123", appKey)
	}
}

func TestDecodeTokenRejectsGarbage(t *testing.T) {
	if _, _, err := cloud.DecodeToken("not-base64-!!!"); err == nil {
		t.Error("expected error for non-base64 token")
	}
	noDot := base64.StdEncoding.EncodeToString([]byte("nodelimiter"))
	if _, _, err := cloud.DecodeToken(noDot); err == nil {
		t.Error("expected error for token without '.' delimiter")
	}
}

func TestMQTTCredentialsHostPort(t *testing.T) {
	c := cloud.MQTTCredentials{URL: "mqtt.zendure.tech:8883"}
	host, port := c.HostPort()
	if host != "mqtt.zendure.tech" || port != 8883 {
		t.Errorf("HostPort = %s:%d, want mqtt.zendure.tech:8883", host, port)
	}
	// Default port when none is present.
	c2 := cloud.MQTTCredentials{URL: "broker.example"}
	if _, port := c2.HostPort(); port != 8883 {
		t.Errorf("default port = %d, want 8883", port)
	}
}

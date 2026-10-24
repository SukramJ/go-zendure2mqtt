// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
	"github.com/SukramJ/go-zendure2mqtt/internal/state"
	"github.com/SukramJ/go-zendure2mqtt/internal/web"
)

func newServer(user, pass string) *web.Server {
	return web.New(web.Deps{
		Cfg:           &config.Config{WebUser: user, WebPassword: pass, Connection: "local", Language: "de"},
		Store:         state.New(),
		MQTTConnected: func() bool { return true },
	})
}

func do(s *web.Server, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestHealthEndpoint(t *testing.T) {
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var h map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h["mqtt_connected"] != true {
		t.Errorf("mqtt_connected = %v, want true", h["mqtt_connected"])
	}
	if h["version"] == "" {
		t.Error("version missing")
	}
}

func TestSnapshotEndpoint(t *testing.T) {
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/api/snapshot", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestIndexServed(t *testing.T) {
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "html") {
		t.Errorf("content-type = %q, want html", ct)
	}
}

func TestBasicAuth(t *testing.T) {
	s := newServer("admin", "secret")

	if rr := do(s, httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody)); rr.Code != http.StatusUnauthorized {
		t.Errorf("no creds: status = %d, want 401", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody)
	req.SetBasicAuth("admin", "secret")
	if rr := do(s, req); rr.Code != http.StatusOK {
		t.Errorf("valid creds: status = %d, want 200", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody)
	req.SetBasicAuth("admin", "wrong")
	if rr := do(s, req); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong creds: status = %d, want 401", rr.Code)
	}
}

func TestHealthCarriesTheMatterPairingCode(t *testing.T) {
	s := web.New(web.Deps{
		Cfg:           &config.Config{Connection: "local", Language: "en"},
		Store:         state.New(),
		MatterPairing: func() (string, string, error) { return "MT:Y.K90AFN00KA0648G00", "34970112332", nil },
	})
	rr := do(s, httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody))
	var h map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h["matter_manual_code"] != "34970112332" || h["matter_qr_payload"] != "MT:Y.K90AFN00KA0648G00" {
		t.Errorf("health %v, want the pairing codes", h)
	}
	rr = do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody))
	if strings.Contains(rr.Body.String(), "matter_") {
		t.Errorf("health without MATTER_ENABLED carries %s", rr.Body.String())
	}
}

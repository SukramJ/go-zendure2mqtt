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
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
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

func TestHealthReportsEVCC(t *testing.T) {
	s := web.New(web.Deps{Cfg: &config.Config{EVCCEnable: true}, Store: state.New()})
	rr := do(s, httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody))
	var h map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h["evcc_enabled"] != true {
		t.Errorf("evcc_enabled = %v, want true", h["evcc_enabled"])
	}
}

// The evcc entries ride in the device's entries; a device without a report
// does not appear for them alone.
func TestSnapshotCarriesEVCC(t *testing.T) {
	st := state.New()
	st.Update(source.Device{SN: "SN1"}, nil, nil, "en")
	st.SetEVCC("SN1", []state.Entry{{Group: "evcc", Topic: "evcc/battery/soc", Name: "Battery state of charge", Unit: "%", Value: 64.0}})
	st.SetEVCC("SN2", []state.Entry{{Group: "evcc", Topic: "evcc/battery/soc", Value: 1.0}})
	s := web.New(web.Deps{Cfg: &config.Config{EVCCEnable: true}, Store: st})
	rr := do(s, httptest.NewRequest(http.MethodGet, "/api/snapshot", http.NoBody))
	var snap state.Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Devices) != 1 || len(snap.Devices[0].Entries) != 1 {
		t.Fatalf("snapshot = %+v, want SN1 with one evcc entry", snap.Devices)
	}
	if e := snap.Devices[0].Entries[0]; e.Group != "evcc" || e.Unit != "%" || e.Value != 64.0 {
		t.Errorf("entry = %+v", e)
	}
}

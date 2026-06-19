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
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/api/health", nil))
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
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/api/snapshot", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestIndexServed(t *testing.T) {
	rr := do(newServer("", ""), httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "html") {
		t.Errorf("content-type = %q, want html", ct)
	}
}

func TestBasicAuth(t *testing.T) {
	s := newServer("admin", "secret")

	if rr := do(s, httptest.NewRequest(http.MethodGet, "/api/health", nil)); rr.Code != http.StatusUnauthorized {
		t.Errorf("no creds: status = %d, want 401", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.SetBasicAuth("admin", "secret")
	if rr := do(s, req); rr.Code != http.StatusOK {
		t.Errorf("valid creds: status = %d, want 200", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.SetBasicAuth("admin", "wrong")
	if rr := do(s, req); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong creds: status = %d, want 401", rr.Code)
	}
}

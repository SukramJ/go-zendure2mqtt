// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package cloud

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/SukramJ/go-mqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

func TestSignKnownAnswer(t *testing.T) {
	// SHA1("C*dafwArEOXK" + "a1b2c3d4e5" + "C*dafwArEOXK"), upper-hex, computed
	// independently with shasum; keys are concatenated in sorted order.
	got := sign(map[string]string{"d": "4", "b": "2", "e": "5", "a": "1", "c": "3"})
	const want = "2E05E6A02B3F3E0A97F1DCA9D08239B34E77CF3A"
	if got != want {
		t.Errorf("sign = %s, want %s", got, want)
	}
}

func TestDeviceAndSub(t *testing.T) {
	tests := []struct {
		topic, dev, sub string
	}{
		{"iot/pk/dev1/properties/report", "dev1", "properties/report"},
		{"/iot/pk/dev1/properties/report", "dev1", "properties/report"},
		{"pk/dev1/properties/report", "dev1", "properties/report"},
		{"pk/dev1", "", ""},
		{"", "", ""},
	}
	for _, tc := range tests {
		dev, sub := deviceAndSub(tc.topic)
		if dev != tc.dev || sub != tc.sub {
			t.Errorf("deviceAndSub(%q) = (%q,%q), want (%q,%q)", tc.topic, dev, sub, tc.dev, tc.sub)
		}
	}
}

func TestLogin(t *testing.T) {
	var gotPath, gotSign, gotClient, gotNonce, gotTS, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSign = r.Header.Get("sign")
		gotClient = r.Header.Get("clientid")
		gotNonce = r.Header.Get("nonce")
		gotTS = r.Header.Get("timestamp")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"code":200,"msg":"ok","data":{"mqtt":{"url":"mq.example:8883","username":"u"},"deviceList":[{"deviceKey":"k1","snNumber":"SN1","productKey":"pk"}]}}`)
	}))
	defer srv.Close()

	res, err := Login(context.Background(), srv.Client(), srv.URL+"/", "APPKEY")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotPath != "/api/ha/deviceList" {
		t.Errorf("path = %q, want /api/ha/deviceList", gotPath)
	}
	if gotClient != clientID {
		t.Errorf("clientid = %q, want %q", gotClient, clientID)
	}
	if want := sign(map[string]string{"appKey": "APPKEY", "timestamp": gotTS, "nonce": gotNonce}); gotSign != want {
		t.Errorf("sign header = %q, want %q", gotSign, want)
	}
	if !strings.Contains(gotBody, `"appKey":"APPKEY"`) {
		t.Errorf("body = %q, want appKey", gotBody)
	}
	if res.MQTT.URL != "mq.example:8883" || len(res.DeviceList) != 1 || res.DeviceList[0].SnNumber != "SN1" {
		t.Errorf("result = %+v", res)
	}
}

func TestLoginErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"api error code", `{"code":401,"msg":"bad sign"}`, "code=401"},
		{"malformed json", `not json`, "parse response"},
		{"oversize body", strings.Repeat("x", maxLoginBody+1), "exceeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := Login(context.Background(), srv.Client(), srv.URL, "K")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestHandleMessageRouting(t *testing.T) {
	dev := source.Device{SN: "SN1", DeviceID: "dev1", ProductKey: "pk"}
	newBackend := func(h source.Handler) *Backend {
		return &Backend{
			logger:    slog.New(slog.DiscardHandler),
			byID:      map[string]source.Device{"dev1": dev},
			onReading: h,
		}
	}

	var got []source.Reading
	b := newBackend(func(r source.Reading) { got = append(got, r) })

	// A report for a known device is delivered, with the SN filled in.
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/report", Payload: []byte(`{"properties":{"a":1}}`)})
	if len(got) != 1 || got[0].Device.DeviceID != "dev1" || got[0].Report.SN != "SN1" {
		t.Fatalf("readings = %+v, want one for dev1 with SN1", got)
	}

	// A report carrying its own SN keeps it.
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/report", Payload: []byte(`{"sn":"OWN"}`)})
	if len(got) != 2 || got[1].Report.SN != "OWN" {
		t.Fatalf("readings = %+v, want second keeping SN OWN", got)
	}

	// Non-report sub-topics, unknown devices and bad payloads are dropped.
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/write", Payload: []byte(`{}`)})
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/other/properties/report", Payload: []byte(`{}`)})
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/report", Payload: []byte(`garbage`)})
	if len(got) != 2 {
		t.Errorf("readings = %d, want 2 (others dropped)", len(got))
	}

	// No handler installed: nothing to deliver, no panic.
	newBackend(nil).handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/report", Payload: []byte(`{}`)})
}

func TestWriteBeforeConnectAndReadUnsupported(t *testing.T) {
	b := &Backend{}
	if err := b.Write(context.Background(), source.Device{}, map[string]any{"x": 1}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Write err = %v, want ErrNotConnected", err)
	}
	if _, err := b.Read(context.Background(), source.Device{}); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Read err = %v, want ErrNotImplemented", err)
	}
}

// recordingObserver records what the backend tells the coordinator.
type recordingObserver struct {
	events []string
}

func (o *recordingObserver) UpstreamUsable(u bool) {
	o.events = append(o.events, "upstream="+strconv.FormatBool(u))
}

func (o *recordingObserver) DeviceReachable(dev source.Device, r bool) {
	o.events = append(o.events, dev.SN+"="+strconv.FormatBool(r))
}

// TestCloudReportsReachability pins what feeds `<name>/connected` and
// `<name>/status/<sn>/online` in cloud mode: the session is the upstream and
// alone moves `connected`; every listed device is reachable through the cloud
// while the session is up, re-reported at once when it comes back — also a
// device that has not reported since — and a drop does not latch any device
// offline (0.10.0 did, until that device's next report).
func TestCloudReportsReachability(t *testing.T) {
	dev := source.Device{SN: "SN1", DeviceID: "dev1", ProductKey: "pk"}
	quiet := source.Device{SN: "SN2", DeviceID: "dev2", ProductKey: "pk"}
	o := &recordingObserver{}
	b := &Backend{
		logger:    slog.New(slog.DiscardHandler),
		byID:      map[string]source.Device{"dev1": dev, "dev2": quiet},
		devices:   []source.Device{dev, quiet},
		onReading: func(source.Reading) {},
	}
	b.Observe(o)

	b.sessionUp(true)
	b.handleMessage(&mqtt.Message{Topic: "iot/pk/dev1/properties/report", Payload: []byte(`{}`)})
	b.sessionUp(false)
	b.sessionUp(true)

	want := "SN1=true SN2=true upstream=true SN1=true upstream=false SN1=true SN2=true upstream=true"
	if got := strings.Join(o.events, " "); got != want {
		t.Errorf("events = %q, want %q", got, want)
	}
}

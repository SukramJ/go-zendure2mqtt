// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"
	hagomqtt "github.com/SukramJ/go-hamqtt/publisher/gomqtt"

	"github.com/SukramJ/go-zendure2mqtt/internal/config"
)

// TestHARuntimeConfigIsTheOnlySpelling pins the tie [HARuntimeConfig] exists
// to make: every field the Home Assistant plane depends on is derived from
// the operator's config, and the test fixtures build their runtime from the
// same function the composition root does.
//
// The defect this replaces was byte-for-byte go-mtec2mqtt's: the composition
// root stated publisher.Config.LegacyEntityTopics and the golden fixture
// re-stated it, with nothing comparing the two — so deleting it from
// cmd/zendure2mqtt left the whole suite green while the shipped daemon
// retracted 29 topics the fleet does not hold and published its device
// documents into a tree still holding the real per-entity configs.
//
// Mutation check: deleting LegacyEntityTopics from HARuntimeConfig fails
// TestLegacyConfigsAreRetractedBeforeTheDocument,
// TestAFailedRetractionAbortsTheDocument and
// TestRetractionsAreReSentAfterAReconnect, because the fixtures now run on
// the daemon's own statement. Bypassing HARuntimeConfig at a construction
// site instead is what [New]'s guard below refuses.
func TestHARuntimeConfigIsTheOnlySpelling(t *testing.T) {
	cfg := &config.Config{HASSBaseTopic: "ha-discovery", MQTTTopic: "zendure2mqtt"}
	got := HARuntimeConfig(cfg, discardLogger())

	if got.Prefix != cfg.HASSBaseTopic {
		t.Errorf("Prefix = %q, want the operator's HASS_BASE_TOPIC %q", got.Prefix, cfg.HASSBaseTopic)
	}
	if want := BridgeStatusTopic(cfg.MQTTTopic); got.StatusTopic != want {
		t.Errorf("StatusTopic = %q, want %q", got.StatusTopic, want)
	}
	if got.QoS != publisher.QoSAtMostOnce {
		t.Errorf("QoS = %v, want QoSAtMostOnce — the zero value means unset and resolves to QoS 1", got.QoS)
	}
	if len(got.LegacyEntityTopics) == 0 {
		t.Fatal("LegacyEntityTopics is unstated: PublishBundle would retract the library's " +
			"five-segment default, which 0 of this fleet's 29 retained configs are on")
	}

	// And the form it resolves to, rendered rather than named: the
	// four-segment per-entity topic this fleet's installed base is on.
	const want = "ha-discovery/sensor/zendure2mqtt_SF2400AC0012345_electric_level/config"
	topic := got.LegacyEntityTopics[0](publisher.LegacyEntity{
		Prefix:   cfg.HASSBaseTopic,
		Platform: "sensor",
		UniqueID: "zendure2mqtt_SF2400AC0012345_electric_level",
	})
	if topic != want {
		t.Errorf("legacy topic = %q, want %q", topic, want)
	}
}

// TestNewRefusesARuntimeThatBypassesHARuntimeConfig is the guard's own pin.
//
// A panic rather than an error because it is a composition-root mistake: the
// only symptom in production is one WARNING line inside Home Assistant's log
// and a fleet of entities that never appear. Nothing on the wire is an error
// and nothing in this daemon's log is either, which is why the daemon must
// refuse to start instead.
func TestNewRefusesARuntimeThatBypassesHARuntimeConfig(t *testing.T) {
	pub := &capturingClient{}
	deps := func(rt *publisher.Runtime) Deps {
		cfg := &config.Config{HASSBaseTopic: "homeassistant", MQTTTopic: "zendure2mqtt"}
		return Deps{
			Cfg:        cfg,
			Backend:    &goldenBackend{},
			MQTT:       pub,
			Catalog:    goldenCatalog(t),
			Logger:     discardLogger(),
			HARuntime:  rt,
			StatePlane: newStatePlane(pub, cfg.MQTTTopic),
		}
	}

	t.Run("legacy form unstated", func(t *testing.T) {
		// Exactly the mutation: a runtime built by hand, correct in every
		// other field, with LegacyEntityTopics left off.
		rt := publisher.New(hagomqtt.Transport(pub), publisher.Config{
			Prefix:      "homeassistant",
			StatusTopic: BridgeStatusTopic("zendure2mqtt"),
			QoS:         publisher.QoSAtMostOnce,
			Logger:      discardLogger(),
		})
		defer rt.Close()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("New accepted a runtime with no legacy config topic form stated")
			}
			if msg, _ := r.(string); !strings.Contains(msg, "HARuntimeConfig") {
				t.Errorf("panic = %v, want it to name HARuntimeConfig as the answer", r)
			}
		}()
		New(deps(rt))
	})

	t.Run("no runtime at all", func(t *testing.T) {
		// The message is asserted, not just the panic: a nil runtime would
		// fault inside LegacyForms anyway, and a nil-pointer dereference in
		// the coordinator's constructor tells the next reader nothing about
		// where the runtime is supposed to be built or why.
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("New accepted a nil HARuntime")
			}
			msg, _ := r.(string)
			if !strings.Contains(msg, "Deps.HARuntime is required") {
				t.Errorf("panic = %v, want it to name the missing dependency", r)
			}
		}()
		New(deps(nil))
	})

	t.Run("built with HARuntimeConfig", func(t *testing.T) {
		cfg := &config.Config{HASSBaseTopic: "homeassistant", MQTTTopic: "zendure2mqtt"}
		rt := publisher.New(hagomqtt.Transport(pub), HARuntimeConfig(cfg, discardLogger()))
		defer rt.Close()
		if New(deps(rt)) == nil {
			t.Fatal("New returned nil")
		}
	})
}

// TestWantLegacyFormsIsDerived asserts the guard reads its expectation off
// [HARuntimeConfig] rather than off a literal.
//
// A literal here would be the very thing this PR removes: a second spelling
// of the same statement, free to drift from the first. Derived, the guard can
// only ever catch a construction site that bypassed HARuntimeConfig — which
// is the failure it is for. Deleting the form from HARuntimeConfig is caught
// by the retraction tests instead, which run against the frozen fleet.
func TestWantLegacyFormsIsDerived(t *testing.T) {
	want := wantLegacyForms()
	if len(want) == 0 {
		t.Fatal("wantLegacyForms is empty")
	}
	probe := publisher.New(nopTransport{},
		HARuntimeConfig(&config.Config{}, slog.New(slog.DiscardHandler)))
	defer probe.Close()
	if got := probe.LegacyForms(); !slices.Equal(got, want) {
		t.Errorf("wantLegacyForms = %v, want %v", want, got)
	}
}

// TestHAStateConfigStatesBothQoSFields pins the second half of F14.
//
// PulseQoS is the one field in publisher whose default is QoS 0 rather than
// QoS 1, so leaving it unset resolves to the same wire byte this bridge
// wants — today. go-hamqtt v0.34.0 warns about it once per boot
// (`publisher.state.pulse_qos_unstated`) precisely because the coincidence
// breaks the moment the state QoS is not 0, and no single-configuration test
// would see it.
//
// Mutation check: dropping either field from HAStateConfig fails here.
func TestHAStateConfigStatesBothQoSFields(t *testing.T) {
	got := HAStateConfig("zendure2mqtt", discardLogger())
	if got.QoS != publisher.QoSAtMostOnce {
		t.Errorf("QoS = %v, want QoSAtMostOnce", got.QoS)
	}
	if got.PulseQoS != publisher.QoSAtMostOnce {
		t.Errorf("PulseQoS = %v, want QoSAtMostOnce stated, not left to coincide with the default", got.PulseQoS)
	}
	if want := []string{CommandFilter("zendure2mqtt")}; !slices.Equal(got.CommandFilters, want) {
		t.Errorf("CommandFilters = %v, want %v", got.CommandFilters, want)
	}
}

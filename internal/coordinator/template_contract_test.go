// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/SukramJ/go-hamqtt/publisher"

	"github.com/SukramJ/go-zendure2mqtt/internal/hass"
	"github.com/SukramJ/go-zendure2mqtt/internal/source"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// The discovery documents paired with the bytes they read.
//
// Every other pin in this package looks at one side: the documents byte for
// byte, or the state topics. Nothing asked what Home Assistant makes of the
// one when it reads the other, which is how 0.10.0 shipped a select that
// showed a code outside its map as the last valid option (finding 2 of the
// 0.10.0 review) and sensors that kept their old state, with a template
// error, whenever an item was cleared.
//
// This test runs the production publish path — process.Resolve over the
// shipped zendure.yaml, the virtual switches, hass.Discovery and the state
// plane, plus the backend callbacks that drive `<name>/connected` and
// `<name>/status/<sn>/online` — through a scripted sequence of reports, and
// evaluates every component of every document it wrote against every payload
// written to that component's topics, with the strict Jinja subset in
// jinja_test.go (which fails on a shape it cannot evaluate rather than
// skipping it). What Home Assistant does with the rendered string is modelled
// from core 2026.10, homeassistant/components/mqtt/:
//
//   - sensor.py `_update_state`: "None" sets the value unknown; a numeric
//     sensor (device_class, unit or state_class) otherwise takes any string
//     and fails on a non-number; an enum sensor ignores a value outside its
//     options with a warning.
//   - number.py `_message_received`: payload_reset (default "None") is
//     unknown, anything else must parse as a number within min..max.
//   - select.py `_message_received`: "none" (any case) is unknown, anything
//     else must be one of the options or is logged "Invalid option" and
//     ignored — the entity keeps its previous option.
//   - switch.py `_is_on_map`: payload_on, payload_off or "None".
//   - entity.py `_availability_message_received`: the rendered value is
//     compared with payload_available / payload_not_available; anything
//     else changes nothing.
//
// The state each step leaves an entity in is what its last write of that
// step renders to, which is what makes the scripted expectations — an
// unmapped code reads unknown, a cleared item reads unknown — checkable.
//
// What it cannot evaluate: Home Assistant's own state machinery beyond the
// rendered string (expire_after, last_reset, force_update), templates of a
// shape the subset refuses (it fails then, it does not skip), and the
// enum-sensor case outside the shipped catalog, which has none — that one is
// covered by internal/harender's own test of the rendered component.

// contractStep is one report fed through the publish path, with the rendered
// states it must leave behind, keyed by component key (unit document only).
type contractStep struct {
	name   string
	report func() *model.Report
	want   map[string]string // component key -> rendered state; "" = must not be written this step
}

func contractSteps() []contractStep {
	golden := goldenReport
	with := func(mut func(*model.Report)) func() *model.Report {
		return func() *model.Report {
			r := goldenReport()
			mut(r)
			return r
		}
	}
	return []contractStep{
		{name: "golden", report: golden},
		{
			name: "unmapped codes",
			report: with(func(r *model.Report) {
				r.Properties["acMode"] = float64(7)   // a code the catalog does not map
				r.Properties["smartMode"] = "volatil" // not a code at all: 0.10.0 read it as 0, "persist"
			}),
			want: map[string]string{"ac_mode": "None", "smart_mode": "None"},
		},
		{name: "golden again", report: golden},
		{
			name: "cleared values",
			report: with(func(r *model.Report) {
				r.Properties["acMode"] = nil
				r.Properties["electricLevel"] = nil
				r.Properties["socSet"] = ""
				r.PackData[0]["socLevel"] = nil
			}),
			want: map[string]string{"ac_mode": "None", "electric_level": "None", "soc_set": "None"},
		},
		{name: "golden after the clear", report: golden},
	}
}

// TestTemplatesAcceptWhatThePublishPathWrites is the contract test. It must
// fail on 0.10.0: there the select rendered the raw 7 (an invalid option,
// ignored), smart_mode rendered "persist" for a value that was no code, and
// every cleared item failed its template.
func TestTemplatesAcceptWhatThePublishPathWrites(t *testing.T) {
	for _, lang := range []string{"en", "de"} {
		t.Run(lang+"/golden-unit-and-pack", func(t *testing.T) {
			runContract(t, lang, goldenUnit(), contractSteps())
		})
		for _, c := range identityCases() {
			t.Run(lang+"/"+c.name, func(t *testing.T) {
				runContract(t, lang, c.dev, []contractStep{{name: "fixture", report: func() *model.Report { return c.report }}})
			})
		}
	}
}

func runContract(t *testing.T, lang string, dev source.Device, steps []contractStep) {
	t.Helper()
	pub := &capturingClient{} // every publish in order, empty ones included
	cfg := testConfig(t, lang)
	rt := newHARuntime(pub, cfg.MQTTTopic)
	t.Cleanup(rt.Close)
	backend := &writeRecorder{goldenBackend: goldenBackend{devices: []source.Device{dev}}}
	coord := New(Deps{
		Cfg:     cfg,
		Backend: backend,
		MQTT:    pub,
		Catalog: goldenCatalog(t),
		HASS: hass.New("homeassistant", cfg.MQTTTopic, cfg.IdentityRoot(),
			testRenderer(cfg.Language), rt, discardLogger()),
		Logger:     discardLogger(),
		HARuntime:  rt,
		StatePlane: newStatePlane(pub, cfg.MQTTTopic),
	})
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	coord.runCtx = dead // no orphan sweep, no re-read
	ctx := context.Background()

	coord.PublishOnline(ctx) // connected = 1
	coord.UpstreamUsable(true)
	coord.DeviceReachable(dev, true)

	stepEnds := make([]int, 0, len(steps))
	for _, s := range steps {
		coord.publish(ctx, dev, s.report())
		stepEnds = append(stepEnds, len(pub.wire()))
	}

	coord.DeviceReachable(dev, false)
	coord.UpstreamUsable(false)
	coord.PublishOffline(ctx)

	wire := pub.wire()
	docs := contractDocuments(t, wire)
	if len(docs) == 0 {
		t.Fatal("the publish path wrote no device document")
	}
	evaluated := 0
	for topic, doc := range docs {
		unit := !strings.Contains(topic, "_pack_")
		for key, comp := range doc {
			evaluated++
			c := contractComponent{t: t, topic: topic, key: key, body: comp}
			c.checkState(wire, steps, stepEnds, unit)
			c.checkAvailability(wire)
			c.checkCommand(coord, backend, wire)
		}
	}
	if evaluated == 0 {
		t.Fatal("no component was evaluated")
	}
}

// contractDocuments decodes the last non-empty write of every device
// document into its components.
func contractDocuments(t *testing.T, wire []wireRecord) map[string]map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]map[string]any{}
	for _, rec := range wire {
		if !strings.HasPrefix(rec.Topic, "homeassistant/device/") || len(rec.Payload) == 0 {
			continue
		}
		var doc struct {
			Components map[string]map[string]any `json:"components"`
		}
		if err := json.Unmarshal(rec.Payload, &doc); err != nil {
			t.Fatalf("%s: %v", rec.Topic, err)
		}
		out[rec.Topic] = doc.Components
	}
	return out
}

type contractComponent struct {
	t          *testing.T
	topic, key string
	body       map[string]any
}

func (c contractComponent) str(field string) string {
	s, _ := c.body[field].(string)
	return s
}

func (c contractComponent) errorf(format string, args ...any) {
	c.t.Helper()
	c.t.Errorf("%s %s: %s", c.topic, c.key, fmt.Sprintf(format, args...))
}

func (c contractComponent) options() []string {
	raw, _ := c.body["options"].([]any)
	out := make([]string, 0, len(raw))
	for _, o := range raw {
		s, _ := o.(string)
		out = append(out, s)
	}
	return out
}

// render applies the component's value_template the way Home Assistant does,
// or passes the payload through when there is none.
func (c contractComponent) render(field string, payload []byte) (string, error) {
	tmpl := c.str(field)
	if tmpl == "" {
		return strings.TrimSpace(string(payload)), nil
	}
	return renderJinja(tmpl, string(payload))
}

// accepts reports whether the platform takes the rendered state, per the
// Home Assistant handlers named at the top of this file.
func (c contractComponent) accepts(state string) error {
	platform := c.str("platform")
	switch platform {
	case "sensor":
		if state == "None" {
			return nil
		}
		if opts := c.options(); len(opts) > 0 {
			if !slices.Contains(opts, state) {
				return fmt.Errorf("enum sensor state %q is not in %v", state, opts)
			}
			return nil
		}
		dc := c.str("device_class")
		numeric := c.str("unit_of_measurement") != "" || c.str("state_class") != "" || (dc != "" && dc != "enum")
		if numeric {
			if _, err := strconv.ParseFloat(state, 64); err != nil {
				return fmt.Errorf("numeric sensor state %q is not a number", state)
			}
		}
		if state == "" {
			return fmt.Errorf("empty state")
		}
		return nil
	case "number":
		if state == "None" {
			return nil
		}
		f, err := strconv.ParseFloat(state, 64)
		if err != nil {
			return fmt.Errorf("number state %q is not a number", state)
		}
		lo, _ := c.body["min"].(float64)
		hi, ok := c.body["max"].(float64)
		if !ok {
			hi = 100 // Home Assistant's DEFAULT_MAX_VALUE
		}
		if _, ok := c.body["min"]; !ok {
			lo = 1 // DEFAULT_MIN_VALUE
		}
		if f < lo || f > hi {
			return fmt.Errorf("number state %v outside %v..%v", f, lo, hi)
		}
		return nil
	case "select":
		if strings.EqualFold(state, "none") {
			return nil
		}
		if !slices.Contains(c.options(), state) {
			return fmt.Errorf("select state %q is not in %v", state, c.options())
		}
		return nil
	case "switch", "binary_sensor":
		on, off := c.str("payload_on"), c.str("payload_off")
		if on == "" || off == "" {
			return fmt.Errorf("%s without payload_on/payload_off", platform)
		}
		if state != on && state != off && state != "None" {
			return fmt.Errorf("%s state %q is neither %q nor %q", platform, state, on, off)
		}
		return nil
	}
	return fmt.Errorf("platform %q is not modelled by this test", platform)
}

// checkState renders every write to the state topic and checks the scripted
// expectations against the last write of each step.
func (c contractComponent) checkState(wire []wireRecord, steps []contractStep, stepEnds []int, unit bool) {
	c.t.Helper()
	stateTopic := c.str("state_topic")
	if stateTopic == "" {
		c.errorf("no state_topic")
		return
	}
	start := 0
	writes := 0
	for i, s := range steps {
		var last *wireRecord
		for j := start; j < stepEnds[i]; j++ {
			if wire[j].Topic != stateTopic {
				continue
			}
			writes++
			last = &wire[j]
			state, err := c.render("value_template", wire[j].Payload)
			if err != nil {
				c.errorf("step %q: value_template on %q: %v", s.name, wire[j].Payload, err)
				continue
			}
			if err := c.accepts(state); err != nil {
				c.errorf("step %q: payload %q renders %q: %v", s.name, wire[j].Payload, state, err)
			}
		}
		start = stepEnds[i]
		want, scripted := s.want[c.key]
		if !unit || !scripted {
			continue
		}
		if last == nil {
			c.errorf("step %q: nothing written, want the state %q", s.name, want)
			continue
		}
		if got, err := c.render("value_template", last.Payload); err != nil || got != want {
			c.errorf("step %q: state after %q = %q (%v), want %q", s.name, last.Payload, got, err, want)
		}
	}
	if writes == 0 {
		c.errorf("the publish path never wrote %s", stateTopic)
	}
}

// checkAvailability renders every write to each availability topic and
// requires one of the entry's two payloads — and the right one.
func (c contractComponent) checkAvailability(wire []wireRecord) {
	c.t.Helper()
	entries, _ := c.body["availability"].([]any)
	if len(entries) == 0 {
		c.errorf("no availability list")
		return
	}
	for _, raw := range entries {
		e, _ := raw.(map[string]any)
		topic, _ := e["topic"].(string)
		tmpl, _ := e["value_template"].(string)
		yes, _ := e["payload_available"].(string)
		no, _ := e["payload_not_available"].(string)
		if yes == "" {
			yes = "online" // DEFAULT_PAYLOAD_AVAILABLE
		}
		if no == "" {
			no = "offline"
		}
		seen := map[bool]bool{}
		for _, rec := range wire {
			if rec.Topic != topic {
				continue
			}
			got := strings.TrimSpace(string(rec.Payload))
			if tmpl != "" {
				var err error
				if got, err = renderJinja(tmpl, string(rec.Payload)); err != nil {
					c.errorf("availability %s on %q: %v", topic, rec.Payload, err)
					continue
				}
			}
			want := availableFor(topic, rec.Payload)
			switch {
			case got == yes && want:
				seen[true] = true
			case got == no && !want:
				seen[false] = true
			default:
				c.errorf("availability %s on %q renders %q, want %q (available %v)", topic, rec.Payload, got, map[bool]string{true: yes, false: no}[want], want)
			}
		}
		if !seen[true] || !seen[false] {
			c.errorf("availability %s: the scenario reached available=%v unavailable=%v, want both", topic, seen[true], seen[false])
		}
	}
}

// availableFor is what an availability payload means by the convention:
// `<name>/connected` at 2, or an `online` status object carrying true.
func availableFor(topic string, payload []byte) bool {
	if strings.HasSuffix(topic, "/connected") {
		return strings.TrimSpace(string(payload)) == "2"
	}
	var item struct {
		Val any `json:"val"`
	}
	_ = json.Unmarshal(payload, &item)
	return item.Val == true
}

// checkCommand sends what Home Assistant would send for each state the
// entity can show back through the set handler, and requires the write that
// restores it.
func (c contractComponent) checkCommand(coord *Coordinator, backend *writeRecorder, wire []wireRecord) {
	c.t.Helper()
	cmdTopic := c.str("command_topic")
	if cmdTopic == "" {
		return
	}
	send := func(option string) []map[string]any {
		c.t.Helper()
		payload := option
		if tmpl := c.str("command_template"); tmpl != "" {
			var err error
			if payload, err = renderJinja(tmpl, option); err != nil {
				c.errorf("command_template on %q: %v", option, err)
				return nil
			}
		}
		cmd, ok := coord.commands.Route(cmdTopic)
		if !ok {
			c.errorf("%s is not routed", cmdTopic)
			return nil
		}
		cmd.Payload = []byte(payload)
		v, err := publisher.ParseSet(cmd.Payload)
		if err != nil {
			c.errorf("ParseSet(%q): %v", payload, err)
			return nil
		}
		backend.take()
		coord.handleSet(context.Background(), cmd, v)
		return backend.take()
	}

	switch c.str("platform") {
	case "select":
		entry, ok := coord.deps.Catalog.ByTopic(c.key)
		if !ok {
			c.errorf("no catalog entry")
			return
		}
		for _, code := range entry.Codes() {
			token, _ := entry.Token(code)
			option, err := c.render("value_template", []byte(`{"val":`+strconv.Quote(token)+`}`))
			if err != nil {
				c.errorf("value_template on token %q: %v", token, err)
				continue
			}
			want, _ := strconv.Atoi(code)
			writes := send(option)
			if len(writes) != 1 || !maps.Equal(writes[0], map[string]any{entry.Property: want}) {
				c.errorf("option %q (token %q) wrote %v, want %s=%d", option, token, writes, entry.Property, want)
			}
		}
	case "switch":
		on, off := send(c.str("payload_on")), send(c.str("payload_off"))
		if len(on) != 1 || len(off) != 1 || len(on[0]) == 0 || maps.Equal(on[0], off[0]) {
			c.errorf("payload_on wrote %v, payload_off wrote %v; want one distinct write each", on, off)
		}
	case "number":
		entry, ok := coord.deps.Catalog.ByTopic(c.key)
		if !ok {
			c.errorf("no catalog entry")
			return
		}
		raw, _ := goldenReport().Properties[entry.Property].(float64)
		stateTopic := c.str("state_topic")
		for _, rec := range wire {
			if rec.Topic != stateTopic || len(rec.Payload) == 0 {
				continue
			}
			shown, err := c.render("value_template", rec.Payload)
			if err != nil || shown == "None" {
				continue
			}
			writes := send(shown)
			if len(writes) != 1 || writes[0][entry.Property] != int(math.Round(raw)) {
				c.errorf("value %q wrote %v, want %s=%v (the raw value it was read from)", shown, writes, entry.Property, raw)
			}
			return
		}
		c.errorf("no number value was written to round-trip")
	default:
		c.errorf("command_topic on platform %q is not modelled by this test", c.str("platform"))
	}
}

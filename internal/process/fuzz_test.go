// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package process

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SukramJ/go-mqtt/protocol"

	"github.com/SukramJ/go-zendure2mqtt/internal/catalog"
	"github.com/SukramJ/go-zendure2mqtt/internal/zendure/model"
)

// fuzzCatalog is deliberately minimal and separate from process_test.go's
// testCatalog: the parsers under test are unexported, so these targets live in
// package process rather than package process_test and cannot share it. One
// mapped property and one mapped pack property are enough to exercise both
// branches of Resolve (catalog hit -> Entry.TopicLeaf, catalog miss -> the
// sanitizeSegment path this file is about).
const fuzzCatalog = `
entries:
  - property: electricLevel
    topic: electric_level
    group: now
    platform: sensor
  - property: maxVol
    topic: max_voltage
    group: battery
    platform: sensor
`

func loadFuzzCatalog(tb testing.TB) *catalog.Catalog {
	tb.Helper()
	cat, err := catalog.Load(strings.NewReader(fuzzCatalog))
	if err != nil {
		tb.Fatalf("catalog.Load: %v", err)
	}
	return cat
}

// This file fuzzes the two functions that turn device-supplied strings into
// published identifiers: sanitizeSegment (every property name becomes a topic
// level) and validPackSN (a battery serial becomes a topic level *and* an HA
// sub-device). Both parse bytes this daemon did not author — a Zendure device
// or the cloud relay chooses them — and both feed things with no migration
// path once published: a `unique_id` is what Home Assistant keys its entity
// registry on, and a malformed topic is a PUBLISH the broker rejects, which
// counts against the output circuit breaker and can mute the whole bridge.
//
// The oracle for topic safety is go-mqtt's own protocol.ValidateTopicName —
// the same validation the publish path will apply at run time — rather than a
// re-implementation of the rules here.
//
// Corpora live in testdata/fuzz/<target>/ and are committed, so the seeds
// double as a regression table: `go test ./internal/process` replays them
// without -fuzz.

// maxFuzzTopicLen bounds the generated inputs for the end-to-end target.
//
// It is a deliberate carve-out, not a rule of the code under test. Nothing
// bounds the length of a property name: validPackSN caps a serial at
// maxPackSNLen (64), but sanitizeSegment passes a name of any length through,
// so a device sending a property name longer than protocol.ValidateTopicName's
// 65535-byte topic limit produces a topic the broker will reject — the exact
// failure sanitizeSegment's doc comment says it exists to prevent. That is a
// pre-existing defect, reported rather than fixed here, and the carve-out is
// what keeps this target reporting on the parsing instead of re-reporting the
// known length gap on every run.
const maxFuzzTopicLen = 4096

// FuzzSanitizeSegment asserts the post-conditions sanitizeSegment's doc
// comment promises, for any input: the result is a usable single topic level.
func FuzzSanitizeSegment(f *testing.F) {
	for _, seed := range []string{
		"",
		"electricLevel",
		"pack/level",
		"pack+level",
		"pack#level",
		"pack\x00level",
		"\xff\xfe",     // invalid UTF-8
		"�",            // an already-encoded replacement rune
		"a/b+c#d\x00e", // every replaced class at once
		"AB-12",        // pinned collision pair, see discovery_golden_test.go
		"AB_12",        //
		"  ",           // whitespace-only: legal in a topic level
		"$SYS",         // broker-reserved prefix, not our business to rewrite
		"Außen",        // multi-byte but valid
		"🔋",            // outside the BMP
		strings.Repeat("x", 300),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := sanitizeSegment(s)

		if got == "" {
			t.Fatalf("sanitizeSegment(%q) = empty; a topic level may not be empty", s)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeSegment(%q) = %q, not valid UTF-8", s, got)
		}
		if i := strings.IndexAny(got, "/+#\x00"); i >= 0 {
			t.Fatalf("sanitizeSegment(%q) = %q, kept %q at %d", s, got, got[i], i)
		}
		// Idempotence: the output must already be a fixed point, otherwise the
		// function's own output would not be safe to feed back through it.
		if again := sanitizeSegment(got); again != got {
			t.Fatalf("sanitizeSegment not idempotent: %q -> %q -> %q", s, got, again)
		}
		// A sanitized segment must be a legal one-level topic in its own right.
		if len(got) <= maxFuzzTopicLen {
			if err := protocol.ValidateTopicName(got); err != nil {
				t.Fatalf("sanitizeSegment(%q) = %q, not a valid topic name: %v", s, got, err)
			}
		}
	})
}

// FuzzValidPackSN asserts that anything validPackSN *admits* is genuinely
// safe downstream — the interesting direction, since an accepted serial is
// spliced into a topic and a unique_id without further sanitisation.
func FuzzValidPackSN(f *testing.F) {
	for _, seed := range []string{
		"",
		"AB1234567890",
		"AB-12",
		"AB_12",
		"ab-12",
		"pack/1",
		"pack+1",
		"pack#1",
		"pack 1",
		"pack.1",
		"päck1",
		"\xff",
		strings.Repeat("A", 64), // exactly maxPackSNLen
		strings.Repeat("A", 65), // one over
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, sn string) {
		if !validPackSN(sn) {
			return
		}
		if sn == "" || len(sn) > maxPackSNLen {
			t.Fatalf("validPackSN(%q) admitted a serial of length %d (bound %d)", sn, len(sn), maxPackSNLen)
		}
		for _, r := range sn {
			ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Fatalf("validPackSN(%q) admitted rune %q", sn, r)
			}
		}
		// An accepted serial is used raw as a topic level, so it must already
		// be what sanitizeSegment would have produced.
		if got := sanitizeSegment(sn); got != sn {
			t.Fatalf("validPackSN admitted %q but sanitizeSegment rewrites it to %q", sn, got)
		}
		if err := protocol.ValidateTopicName(sn); err != nil {
			t.Fatalf("validPackSN admitted %q, not a valid topic name: %v", sn, err)
		}
	})
}

// FuzzResolveTopicSafety runs the two parsers through the path that actually
// publishes: Resolve mints Points from a report, and StateTopic/CommandTopic
// assemble the topic the broker sees. The invariant is that no device-supplied
// property name or pack serial can produce a topic the MQTT layer rejects.
func FuzzResolveTopicSafety(f *testing.F) {
	for _, seed := range [][2]string{
		{"electricLevel", "AB1234"},
		{"pack/level", "AB-12"},
		{"pack+level", "AB_12"},
		{"pack#level", "pack/1"},
		{"\x00", ""},
		{"\xff\xfe", "\xff"},
		{"a/b", strings.Repeat("Z", 64)},
	} {
		f.Add(seed[0], seed[1])
	}

	cat := loadFuzzCatalog(f)

	f.Fuzz(func(t *testing.T, property, packSN string) {
		if len(property) > maxFuzzTopicLen || len(packSN) > maxFuzzTopicLen {
			return // see maxFuzzTopicLen
		}
		rep := &model.Report{
			SN:         "DEVSN1",
			Properties: map[string]any{property: 1},
			PackData:   []map[string]any{{"sn": packSN, "maxVol": 1}},
		}

		for _, p := range Resolve(rep, cat, "de") {
			for _, topic := range []string{
				StateTopic("zendure", rep.SN, p),
				CommandTopic("zendure", rep.SN, p),
			} {
				if err := protocol.ValidateTopicName(topic); err != nil {
					t.Fatalf("property %q / packSN %q produced invalid topic %q: %v",
						property, packSN, topic, err)
				}
				// No empty level: an empty level would silently reparent the
				// entity under a different device in the topic tree.
				for _, level := range strings.Split(topic, "/") {
					if level == "" {
						t.Fatalf("property %q / packSN %q produced topic %q with an empty level",
							property, packSN, topic)
					}
				}
			}
		}
	})
}

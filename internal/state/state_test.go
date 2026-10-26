// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package state

import (
	"strconv"
	"testing"

	"github.com/SukramJ/go-zendure2mqtt/internal/source"
)

// A change reaches every subscriber, coalesced to one pending signal, and a
// released subscription hears nothing more.
func TestChangesCoalesceAndRelease(t *testing.T) {
	s := New()
	ch, release := s.Changes()
	s.Update(source.Device{SN: "A"}, nil, nil, "en")
	s.SetEVCC("A", nil)
	select {
	case <-ch:
	default:
		t.Fatal("no change signalled")
	}
	select {
	case <-ch:
		t.Fatal("two changes were not coalesced")
	default:
	}
	release()
	release() // idempotent
	s.Update(source.Device{SN: "A"}, nil, nil, "en")
	select {
	case <-ch:
		t.Fatal("a released subscription was signalled")
	default:
	}
}

// Reachability rides on the device, and a unit known only from it — it
// never reported — is listed without entries.
func TestSnapshotCarriesOnline(t *testing.T) {
	s := New()
	s.Update(source.Device{SN: "A"}, nil, nil, "en")
	s.SetOnline("A", true)
	s.SetOnline("B", false)
	snap := s.Snapshot()
	if len(snap.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(snap.Devices))
	}
	if a := snap.Devices[0]; a.SN != "A" || a.Online == nil || !*a.Online {
		t.Errorf("A = %+v", a)
	}
	if b := snap.Devices[1]; b.SN != "B" || b.Online == nil || *b.Online || !b.UpdatedAt.IsZero() || b.Entries == nil {
		t.Errorf("B = %+v", b)
	}
}

// Only a change of reachability is signalled.
func TestSetOnlineSignalsChangesOnly(t *testing.T) {
	s := New()
	s.SetOnline("A", true)
	ch, release := s.Changes()
	defer release()
	s.SetOnline("A", true)
	select {
	case <-ch:
		t.Fatal("an unchanged reachability was signalled")
	default:
	}
	s.SetOnline("A", false)
	select {
	case <-ch:
	default:
		t.Fatal("a changed reachability was not signalled")
	}
}

// The problem log keeps the newest entries, newest first.
func TestProblemsBounded(t *testing.T) {
	s := New()
	for i := range maxProblems + 5 {
		s.AddProblem(Problem{Event: strconv.Itoa(i)})
	}
	ps := s.Problems()
	if len(ps) != maxProblems {
		t.Fatalf("problems = %d, want %d", len(ps), maxProblems)
	}
	if ps[0].Event != strconv.Itoa(maxProblems+4) || ps[len(ps)-1].Event != "5" {
		t.Errorf("order = %s … %s", ps[0].Event, ps[len(ps)-1].Event)
	}
	if ps[0].At.IsZero() {
		t.Error("problem not stamped")
	}
}

func TestUpstream(t *testing.T) {
	s := New()
	if _, known := s.Upstream(); known {
		t.Error("upstream known before any report")
	}
	s.SetUpstream(false)
	if usable, known := s.Upstream(); !known || usable {
		t.Errorf("upstream = %v, %v", usable, known)
	}
}

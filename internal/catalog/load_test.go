// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package catalog

import (
	"strings"
	"testing"
)

func TestLoadRejectsDuplicateProperty(t *testing.T) {
	y := "entries:\n  - property: acMode\n    topic: a\n  - property: acMode\n    topic: b\n"
	if _, err := Load(strings.NewReader(y)); err == nil {
		t.Fatal("expected error for duplicate property")
	}
}

func TestLoadRejectsDuplicateTopicLeaf(t *testing.T) {
	// Second entry's topic falls back to its property "limit", colliding with
	// the first entry's explicit topic "limit".
	y := "entries:\n  - property: inputLimit\n    topic: limit\n  - property: limit\n"
	if _, err := Load(strings.NewReader(y)); err == nil {
		t.Fatal("expected error for duplicate topic leaf")
	}
}

func TestLoadRejectsInvalidPlatform(t *testing.T) {
	y := "entries:\n  - property: acMode\n    platform: gauge\n"
	if _, err := Load(strings.NewReader(y)); err == nil {
		t.Fatal("expected error for invalid platform")
	}
}

func TestLoadAcceptsCleanCatalog(t *testing.T) {
	y := "entries:\n  - property: acMode\n    platform: select\n  - property: inputLimit\n    topic: input_limit\n    platform: number\n"
	if _, err := Load(strings.NewReader(y)); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

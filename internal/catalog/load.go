// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package catalog

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// file is the on-disk shape of zendure.yaml.
type file struct {
	Entries []Entry `yaml:"entries"`
}

// Load parses a catalog from r.
func Load(r io.Reader) (*Catalog, error) {
	var f file
	if err := yaml.NewDecoder(r).Decode(&f); err != nil {
		return nil, fmt.Errorf("catalog: parse yaml: %w", err)
	}
	seenProp := make(map[string]int, len(f.Entries))
	seenLeaf := make(map[string]int, len(f.Entries))
	for i := range f.Entries {
		e := &f.Entries[i]
		if e.Property == "" {
			return nil, fmt.Errorf("catalog: entry %d has no 'property'", i)
		}
		if !validPlatform(e.Platform) {
			return nil, fmt.Errorf("catalog: entry %d (%q) has invalid platform %q", i, e.Property, e.Platform)
		}
		// Duplicate properties or topic leaves silently last-win in the index,
		// which misroutes an inbound .../set to the wrong device property.
		if j, dup := seenProp[e.Property]; dup {
			return nil, fmt.Errorf("catalog: entries %d and %d share property %q", j, i, e.Property)
		}
		if j, dup := seenLeaf[e.TopicLeaf()]; dup {
			return nil, fmt.Errorf("catalog: entries %d and %d share topic leaf %q", j, i, e.TopicLeaf())
		}
		seenProp[e.Property] = i
		seenLeaf[e.TopicLeaf()] = i
	}
	return newCatalog(f.Entries), nil
}

// validPlatform reports whether p is a supported HA platform (empty disables
// discovery for the entry). An unknown platform yields an entity HA silently
// ignores, so it is rejected at load time instead.
func validPlatform(p string) bool {
	switch p {
	case "", "sensor", "binary_sensor", "number", "select", "switch":
		return true
	default:
		return false
	}
}

// LoadFile opens path and parses the catalog from it.
func LoadFile(path string) (*Catalog, error) {
	fh, err := os.Open(path) //nolint:gosec // operator-supplied catalog path
	if err != nil {
		return nil, fmt.Errorf("catalog: open %s: %w", path, err)
	}
	defer func() { _ = fh.Close() }()
	return Load(fh)
}

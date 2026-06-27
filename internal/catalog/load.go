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
	for i := range f.Entries {
		if f.Entries[i].Property == "" {
			return nil, fmt.Errorf("catalog: entry %d has no 'property'", i)
		}
	}
	return newCatalog(f.Entries), nil
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

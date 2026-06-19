// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Env abstracts process environment access so tests can inject a hermetic
// env without touching os.Environ.
type Env interface {
	LookupEnv(key string) (string, bool)
	Environ() []string
}

// OSEnv is the real-process implementation of [Env].
type OSEnv struct{}

// LookupEnv implements Env.
func (OSEnv) LookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// Environ implements Env.
func (OSEnv) Environ() []string { return os.Environ() }

// Load reads a config from r, applies ZENDURE_* overrides from env, fills
// defaults, and validates the result.
func Load(r io.Reader, env Env) (*Config, error) {
	var raw map[string]any
	if err := yaml.NewDecoder(r).Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			raw = map[string]any{} // empty file is allowed; defaults kick in
		} else {
			return nil, fmt.Errorf("config: parse yaml: %w", err)
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}

	if env != nil {
		applyEnvOverrides(raw, env)
	}

	// Round-trip through yaml.v3 so the typed Config sees the merged view.
	bs, err := yaml.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("config: re-marshal merged config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(bs, &cfg); err != nil {
		return nil, fmt.Errorf("config: decode merged config: %w", err)
	}

	applyDefaults(&cfg)
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadFile is a convenience wrapper around [Load] that opens path itself.
func LoadFile(path string, env Env) (*Config, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Load(f, env)
}

// Locate walks the standard search order (CWD → XDG/APPDATA → ~/.config)
// and returns the first config.yaml that exists.
func Locate(env Env) (string, bool) {
	if env == nil {
		env = OSEnv{}
	}
	for _, p := range configCandidates(env, ConfigFile) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// configCandidates returns the ordered lookup paths for file name.
func configCandidates(env Env, name string) []string {
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, name))
	}
	switch runtime.GOOS {
	case "windows":
		if v, ok := env.LookupEnv("APPDATA"); ok && v != "" {
			candidates = append(candidates, filepath.Join(v, AppDirName, name))
		}
	default:
		if v, ok := env.LookupEnv("XDG_CONFIG_HOME"); ok && v != "" {
			candidates = append(candidates, filepath.Join(v, AppDirName, name))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", AppDirName, name))
	}
	return candidates
}

// applyEnvOverrides walks every ZENDURE_<KEY>=value pair and sets
// raw[KEY] = coerced(value). The raw map is mutated in place.
//
// Coercion order: bool → int → float → string.
func applyEnvOverrides(raw map[string]any, env Env) {
	for _, kv := range env.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, EnvPrefix) {
			continue
		}
		cfgKey := key[len(EnvPrefix):]
		if cfgKey == "" {
			continue
		}
		raw[cfgKey] = coerceEnvValue(val)
	}
}

// coerceEnvValue applies the bool → int → float → string ladder.
func coerceEnvValue(s string) any {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true
	case "false":
		return false
	}
	// Atoi parses straight into a platform-width int (no int64→int narrowing,
	// which CodeQL flags as go/incorrect-integer-conversion).
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

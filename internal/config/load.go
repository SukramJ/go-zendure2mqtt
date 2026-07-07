// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
// Coercion is per target field type: a value bound for a bool/int/float field
// is parsed accordingly, while a value bound for a string field (or an unknown
// key) is stored verbatim. Type-blind coercion would silently corrupt
// numeric-looking string credentials (e.g. MQTT_PASSWORD=0123 → 123).
func applyEnvOverrides(raw map[string]any, env Env) {
	kinds := configFieldKinds()
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
		raw[cfgKey] = coerceEnvValue(val, kinds[cfgKey])
	}
}

// configFieldKinds maps each scalar Config YAML key to its field kind (pointers
// dereferenced to their element). Composite fields (slices/structs) are omitted
// — they are not settable via the scalar env-override path.
func configFieldKinds() map[string]reflect.Kind {
	t := reflect.TypeOf(Config{})
	kinds := make(map[string]reflect.Kind, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		kinds[name] = ft.Kind()
	}
	return kinds
}

// coerceEnvValue converts an env string to the type expected by kind. String
// (and unknown) targets keep the raw value; bool/int/float targets are parsed,
// falling back to the raw string on a parse error so validation can report it.
func coerceEnvValue(s string, kind reflect.Kind) any {
	switch kind {
	case reflect.Bool:
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Atoi parses straight into a platform-width int (no int64→int narrowing,
		// which CodeQL flags as go/incorrect-integer-conversion).
		if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return i
		}
	case reflect.Float32, reflect.Float64:
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f
		}
	}
	return s
}

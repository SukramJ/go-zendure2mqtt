// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package process

// Item returns a point's mqtt-smarthome item path, the levels below
// `<name>/status/` and `<name>/set/`:
//
//	<sn>/<group>/<topic>
//	<sn>/battery/<packSN>/<topic>   (battery pack values)
//
// It is the one formula of this bridge's item tree; internal/harender turns it
// into topics through go-hamqtt's topic.SmartHome, which makes every level
// topic-safe.
func Item(sn string, p Point) []string {
	parts := []string{sn, p.Group}
	if p.PackSN != "" {
		parts = append(parts, p.PackSN)
	}
	return append(parts, p.Topic)
}

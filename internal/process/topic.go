// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package process

import "strings"

// StateTopic returns the MQTT topic a point's value is published to:
//
//	<root>/<sn>/<group>/<topic>/state
//	<root>/<sn>/battery/<packSN>/<topic>/state   (battery pack values)
func StateTopic(root, sn string, p Point) string {
	return topicBase(root, sn, p) + "/state"
}

// CommandTopic returns the MQTT topic a writable point listens on for
// commands (Home Assistant / manual writes): the state topic with a /set
// suffix instead of /state.
func CommandTopic(root, sn string, p Point) string {
	return topicBase(root, sn, p) + "/set"
}

// topicBase builds the shared prefix (without the /state|/set leaf).
func topicBase(root, sn string, p Point) string {
	parts := []string{root, sn, p.Group}
	if p.PackSN != "" {
		parts = append(parts, p.PackSN)
	}
	parts = append(parts, p.Topic)
	return strings.Join(parts, "/")
}

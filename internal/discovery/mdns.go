// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

// Package discovery implements a tiny, dependency-free multicast-DNS
// browser used to find Zendure devices that advertise the zenSDK service
// (_zendure._tcp) on the local network.
//
// It sends a one-shot PTR query with the unicast-response (QU) bit set so
// responders answer directly to our ephemeral socket, then parses the
// PTR/SRV/A/TXT records (handling DNS name compression) into a flat
// service list. Only stdlib net + encoding/binary are used — no third
// party DNS library — to keep the project's minimal-dependency promise.
package discovery

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// ZendureService is the zenSDK mDNS service type.
const ZendureService = "_zendure._tcp"

const (
	mdnsGroup = "224.0.0.251:5353"
	typeA     = 1
	typePTR   = 12
	typeTXT   = 16
	typeSRV   = 33
	qclassQU  = 0x8001 // IN class with the unicast-response bit set
)

// Service is one discovered mDNS service instance.
type Service struct {
	Instance string   // first label, e.g. "Zendure-SolarFlow2400-AABBCC"
	Host     string   // SRV target host, e.g. "zendure-xxx.local."
	Addrs    []net.IP // resolved A records
	Port     int      // SRV port
	TXT      []string // TXT key=value strings
}

// Browse sends an mDNS query for service (e.g. [ZendureService]) and
// collects responses until timeout elapses or ctx is cancelled.
func Browse(ctx context.Context, service string, timeout time.Duration) ([]Service, error) {
	full := strings.TrimSuffix(service, ".") + ".local."

	group, err := net.ResolveUDPAddr("udp4", mdnsGroup)
	if err != nil {
		return nil, fmt.Errorf("discovery: resolve group: %w", err)
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("discovery: listen: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.WriteToUDP(buildQuery(full), group); err != nil {
		return nil, fmt.Errorf("discovery: send query: %w", err)
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)

	col := newCollector()
	buf := make([]byte, 9000)
	for {
		if ctx.Err() != nil {
			break
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				break // expected: read deadline reached
			}
			return col.result(), fmt.Errorf("discovery: read: %w", err)
		}
		col.ingest(buf[:n], full)
	}
	return col.result(), nil
}

// buildQuery encodes a single-question PTR query for name with the QU bit.
func buildQuery(name string) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 0)        // ID
	b = binary.BigEndian.AppendUint16(b, 0)        // flags (standard query)
	b = binary.BigEndian.AppendUint16(b, 1)        // QDCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)        // ANCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)        // NSCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)        // ARCOUNT
	b = appendName(b, name)                        // QNAME
	b = binary.BigEndian.AppendUint16(b, typePTR)  // QTYPE
	b = binary.BigEndian.AppendUint16(b, qclassQU) // QCLASS
	return b
}

// appendName encodes a dotted DNS name as length-prefixed labels.
func appendName(b []byte, name string) []byte {
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			continue
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0)
}

// collector accumulates records across one or more response packets.
type collector struct {
	services map[string]*Service // keyed by instance full name
	hostIP   map[string][]net.IP // host name → A records
}

func newCollector() *collector {
	return &collector{services: map[string]*Service{}, hostIP: map[string][]net.IP{}}
}

// ingest parses one DNS response message and folds its records in.
func (c *collector) ingest(msg []byte, serviceFull string) {
	if len(msg) < 12 {
		return
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	total := int(binary.BigEndian.Uint16(msg[6:8])) + // answers
		int(binary.BigEndian.Uint16(msg[8:10])) + // authority
		int(binary.BigEndian.Uint16(msg[10:12])) // additional
	off := 12

	var err error
	for range qd { // skip questions: name + qtype(2) + qclass(2)
		if _, off, err = parseName(msg, off); err != nil || off+4 > len(msg) {
			return
		}
		off += 4
	}

	for range total {
		var name string
		if name, off, err = parseName(msg, off); err != nil || off+10 > len(msg) {
			return
		}
		rrType := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		rdStart := off + 10
		off = rdStart + rdlen
		if off > len(msg) {
			return
		}
		switch rrType {
		case typePTR:
			if !strings.EqualFold(name, serviceFull) {
				continue
			}
			if target, _, perr := parseName(msg, rdStart); perr == nil {
				c.ensure(target)
			}
		case typeSRV:
			if rdlen < 6 {
				continue
			}
			target, _, perr := parseName(msg, rdStart+6)
			if perr != nil {
				continue
			}
			s := c.ensure(name)
			s.Port = int(binary.BigEndian.Uint16(msg[rdStart+4 : rdStart+6]))
			s.Host = target
		case typeTXT:
			c.ensure(name).TXT = append(c.ensure(name).TXT, parseTXT(msg[rdStart:off])...)
		case typeA:
			if rdlen == 4 {
				c.hostIP[name] = append(c.hostIP[name], net.IPv4(msg[rdStart], msg[rdStart+1], msg[rdStart+2], msg[rdStart+3]))
			}
		}
	}
}

// ensure returns (creating if needed) the service for an instance full name.
func (c *collector) ensure(instanceFull string) *Service {
	s, ok := c.services[instanceFull]
	if !ok {
		s = &Service{Instance: firstLabel(instanceFull)}
		c.services[instanceFull] = s
	}
	return s
}

// parseName decodes a (possibly compressed) DNS name starting at off and
// returns the dotted name (with trailing dot) plus the offset just past the
// name in the record stream.
func parseName(msg []byte, off int) (string, int, error) {
	var parts []string
	next := -1
	pos := off
	for {
		if pos < 0 || pos >= len(msg) {
			return "", 0, errors.New("discovery: name out of bounds")
		}
		b := msg[pos]
		switch {
		case b == 0:
			pos++
			if next < 0 {
				next = pos
			}
			return strings.Join(parts, ".") + ".", next, nil
		case b&0xC0 == 0xC0:
			if pos+1 >= len(msg) {
				return "", 0, errors.New("discovery: truncated pointer")
			}
			ptr := int(b&0x3F)<<8 | int(msg[pos+1])
			if next < 0 {
				next = pos + 2
			}
			if ptr >= pos { // pointers must reference earlier data (loop guard)
				return "", 0, errors.New("discovery: non-backward pointer")
			}
			pos = ptr
		default:
			pos++
			if pos+int(b) > len(msg) {
				return "", 0, errors.New("discovery: label out of bounds")
			}
			parts = append(parts, string(msg[pos:pos+int(b)]))
			pos += int(b)
		}
	}
}

// parseTXT splits TXT rdata into its length-prefixed strings.
func parseTXT(rdata []byte) []string {
	var out []string
	for i := 0; i < len(rdata); {
		l := int(rdata[i])
		i++
		if i+l > len(rdata) {
			break
		}
		if l > 0 {
			out = append(out, string(rdata[i:i+l]))
		}
		i += l
	}
	return out
}

// firstLabel returns the first dot-separated label of a DNS name.
func firstLabel(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// result attaches A records to services and returns them as a slice.
func (c *collector) result() []Service {
	out := make([]Service, 0, len(c.services))
	for _, s := range c.services {
		if ips := c.hostIP[s.Host]; len(ips) > 0 {
			s.Addrs = append(s.Addrs, ips...)
		}
		out = append(out, *s)
	}
	return out
}

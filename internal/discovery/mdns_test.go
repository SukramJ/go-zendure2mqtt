// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package discovery

import (
	"encoding/binary"
	"testing"
)

// TestIngestExtractsService builds a synthetic mDNS response (PTR + SRV +
// TXT + A) and checks the collector reassembles one fully-resolved service.
func TestIngestExtractsService(t *testing.T) {
	service := "_zendure._tcp.local."
	instance := "Zendure-Test._zendure._tcp.local."
	host := "zendure-test.local."

	var b []byte
	b = binary.BigEndian.AppendUint16(b, 0)      // ID
	b = binary.BigEndian.AppendUint16(b, 0x8400) // response flags
	b = binary.BigEndian.AppendUint16(b, 0)      // QDCOUNT
	b = binary.BigEndian.AppendUint16(b, 4)      // ANCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)      // NSCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)      // ARCOUNT

	rr := func(name string, typ uint16, rdata []byte) {
		b = appendName(b, name)
		b = binary.BigEndian.AppendUint16(b, typ)
		b = binary.BigEndian.AppendUint16(b, 1)   // class IN
		b = binary.BigEndian.AppendUint32(b, 120) // TTL
		b = binary.BigEndian.AppendUint16(b, uint16(len(rdata)))
		b = append(b, rdata...)
	}

	rr(service, typePTR, appendName(nil, instance))

	srv := []byte{0, 0, 0, 0} // priority + weight
	srv = binary.BigEndian.AppendUint16(srv, 8080)
	srv = appendName(srv, host)
	rr(instance, typeSRV, srv)

	txt := append([]byte{byte(len("sn=ABC123"))}, "sn=ABC123"...)
	rr(instance, typeTXT, txt)

	rr(host, typeA, []byte{192, 168, 1, 50})

	col := newCollector()
	col.ingest(b, service)
	res := col.result()

	if len(res) != 1 {
		t.Fatalf("want 1 service, got %d", len(res))
	}
	s := res[0]
	if s.Instance != "Zendure-Test" {
		t.Errorf("Instance = %q, want Zendure-Test", s.Instance)
	}
	if s.Host != host {
		t.Errorf("Host = %q, want %q", s.Host, host)
	}
	if s.Port != 8080 {
		t.Errorf("Port = %d, want 8080", s.Port)
	}
	if len(s.Addrs) != 1 || s.Addrs[0].String() != "192.168.1.50" {
		t.Errorf("Addrs = %v, want [192.168.1.50]", s.Addrs)
	}
	if len(s.TXT) != 1 || s.TXT[0] != "sn=ABC123" {
		t.Errorf("TXT = %v, want [sn=ABC123]", s.TXT)
	}
}

// TestParseNameCompression verifies the compression-pointer path: a label
// "foo" followed by a pointer back to a "local." encoded at offset 12.
func TestParseNameCompression(t *testing.T) {
	msg := append(make([]byte, 0, 32), make([]byte, 12)...) // 12-byte dummy header
	base := len(msg)                                        // 12
	msg = appendName(msg, "local.")                         // "local." at offset 12
	start := len(msg)
	msg = append(msg, 3, 'f', 'o', 'o', 0xC0, byte(base)) // label "foo" + backward pointer to offset 12

	name, next, err := parseName(msg, start)
	if err != nil {
		t.Fatalf("parseName: %v", err)
	}
	if name != "foo.local." {
		t.Errorf("name = %q, want foo.local.", name)
	}
	if next != len(msg) {
		t.Errorf("next = %d, want %d", next, len(msg))
	}
}

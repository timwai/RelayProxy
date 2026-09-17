package divert

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// These fixtures and checksum helpers do not call the production codec. This
// checks the wire representation, rather than just round-tripping two functions
// that could share the same checksum or extension-header error.
func packetTestFixture(ipv6 bool, protocol Protocol, payload []byte, extras bool) ([]byte, int) {
	ipLength, transportLength, next := 20, 8, byte(17)
	if ipv6 {
		ipLength = 40
	}
	if extras {
		if ipv6 {
			ipLength += 32
		} else {
			ipLength += 4
		}
	}
	if protocol == ProtoTCP {
		transportLength, next = 20, 6
		if extras {
			transportLength += 12
		}
	}
	data := make([]byte, ipLength+transportLength+len(payload))
	if ipv6 {
		copy(data[:4], []byte{0x62, 0x40, 0x12, 0x34})
		binary.BigEndian.PutUint16(data[4:6], uint16(len(data)-40))
		data[6], data[7] = next, 63
		src, dst := netip.MustParseAddr("2001:db8:1::10").As16(), netip.MustParseAddr("2001:db8:2::20").As16()
		copy(data[8:24], src[:])
		copy(data[24:40], dst[:])
		if extras {
			data[6] = 0
			copy(data[40:48], []byte{43, 0, 1, 4, 0, 0, 0, 0})
			copy(data[48:56], []byte{60, 0, 253, 0, 0, 0, 0, 0})
			copy(data[56:64], []byte{44, 0, 0x1e, 2, 9, 8, 1, 0})
			copy(data[64:72], []byte{next, 0, 0, 0, 1, 2, 3, 4})
		}
	} else {
		data[0], data[1], data[6], data[8], data[9] = 0x40|byte(ipLength/4), 0xb8, 0x40, 63, next
		binary.BigEndian.PutUint16(data[2:4], uint16(len(data)))
		binary.BigEndian.PutUint16(data[4:6], 0x2a43)
		copy(data[12:16], []byte{192, 0, 2, 10})
		copy(data[16:20], []byte{198, 51, 100, 20})
		if extras {
			copy(data[20:24], []byte{1, 1, 0, 0})
		}
	}
	transport := data[ipLength:]
	binary.BigEndian.PutUint16(transport[:2], 50123)
	binary.BigEndian.PutUint16(transport[2:4], 443)
	if protocol == ProtoTCP {
		binary.BigEndian.PutUint32(transport[4:8], 0x12345678)
		binary.BigEndian.PutUint32(transport[8:12], 0x87654321)
		transport[12], transport[13] = byte(transportLength/4)<<4, 0x18
		binary.BigEndian.PutUint16(transport[14:16], 4096)
		if extras {
			copy(transport[20:32], []byte{1, 1, 8, 10, 0, 0, 0, 1, 0, 0, 0, 2})
		}
	} else {
		binary.BigEndian.PutUint16(transport[4:6], uint16(len(transport)))
	}
	copy(transport[transportLength:], payload)
	packetTestSetChecksums(data, ipLength, protocol)
	return data, ipLength
}

func packetTestChecksum(data []byte) uint16 {
	var sum uint64
	for index := 0; index < len(data); index += 2 {
		word := uint16(data[index]) << 8
		if index+1 < len(data) {
			word |= uint16(data[index+1])
		}
		sum += uint64(word)
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func packetTestPseudoBytes(data []byte, offset int, protocol Protocol) []byte {
	length := 12
	if data[0]>>4 == 6 {
		length = 40
	}
	pseudo := make([]byte, length+len(data)-offset)
	next := byte(6)
	if protocol == ProtoUDP {
		next = 17
	}
	if length == 12 {
		copy(pseudo[:8], data[12:20])
		pseudo[9] = next
		binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(data)-offset))
	} else {
		copy(pseudo[:32], data[8:40])
		binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(data)-offset))
		pseudo[39] = next
	}
	copy(pseudo[length:], data[offset:])
	return pseudo
}

func packetTestSetChecksums(data []byte, offset int, protocol Protocol) {
	if data[0]>>4 == 4 {
		data[10], data[11] = 0, 0
		binary.BigEndian.PutUint16(data[10:12], packetTestChecksum(data[:int(data[0]&15)*4]))
	}
	checksumOffset := offset + 16
	if protocol == ProtoUDP {
		checksumOffset = offset + 6
	}
	data[checksumOffset], data[checksumOffset+1] = 0, 0
	checksum := packetTestChecksum(packetTestPseudoBytes(data, offset, protocol))
	if protocol == ProtoUDP && checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(data[checksumOffset:], checksum)
}

func packetTestAssertChecksums(t *testing.T, data []byte, offset int, protocol Protocol) {
	t.Helper()
	if data[0]>>4 == 4 && packetTestChecksum(data[:int(data[0]&15)*4]) != 0 {
		t.Fatal("IPv4 header checksum is invalid")
	}
	if got := packetTestChecksum(packetTestPseudoBytes(data, offset, protocol)); got != 0 {
		t.Fatalf("transport checksum is invalid: residual %#04x", got)
	}
	if protocol == ProtoUDP && binary.BigEndian.Uint16(data[offset+6:offset+8]) == 0 {
		t.Fatal("UDP reply omitted its checksum")
	}
}

func TestPacketChecksumGoldenAndOddChunks(t *testing.T) {
	// RFC 1071 section 3's eight bytes sum to 0xddf2 before complementing.
	data := []byte{0, 1, 0xf2, 3, 0xf4, 0xf5, 0xf6, 0xf7}
	if got := packetChecksum(data); got != 0x220d {
		t.Fatalf("RFC 1071 checksum = %#04x, want 0x220d", got)
	}
	for end := 0; end <= len(data); end++ {
		for split := 0; split <= end; split++ {
			if got, want := packetChecksum(data[:split], nil, data[split:end]), packetTestChecksum(data[:end]); got != want {
				t.Fatalf("end=%d split=%d: checksum %#04x, want %#04x", end, split, got, want)
			}
		}
	}
	// A published IPv4 header example has checksum 0xb861.
	header := []byte{0x45, 0, 0, 0x73, 0, 0, 0x40, 0, 0x40, 0x11, 0, 0, 0xc0, 0xa8, 0, 1, 0xc0, 0xa8, 0, 0xc7}
	if got := packetChecksum(header); got != 0xb861 {
		t.Fatalf("IPv4 golden checksum = %#04x, want 0xb861", got)
	}
}

func TestPacketParseAndNATRoundTrip(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			for _, extras := range []bool{false, true} {
				t.Run(fmt.Sprintf("ipv6=%t/%s/options=%t", ipv6, protocol, extras), func(t *testing.T) {
					payload := []byte{9, 8, 7, 6, 5}
					data, offset := packetTestFixture(ipv6, protocol, payload, extras)
					original := bytes.Clone(data)
					packet, err := parseIPPacket(data)
					if err != nil {
						t.Fatal(err)
					}
					if packet.TransportOffset != offset || packet.Protocol != protocol || !bytes.Equal(packet.Payload, payload) {
						t.Fatalf("incorrect packet metadata: %+v", packet)
					}
					if &packet.Bytes[0] != &data[0] || &packet.Payload[0] != &data[len(data)-len(payload)] {
						t.Fatal("packet buffers should alias the caller's input")
					}
					if protocol == ProtoTCP && (packet.TCPFlags != 0x18 || packet.TCPSequence != 0x12345678 || packet.TCPAck != 0x87654321) {
						t.Fatal("TCP sequence, flags or acknowledgement parsed incorrectly")
					}
					source, destination := netip.MustParseAddrPort("127.0.0.1:42001"), netip.MustParseAddrPort("127.0.0.1:42002")
					if ipv6 {
						source, destination = netip.MustParseAddrPort("[::1]:42001"), netip.MustParseAddrPort("[::1]:42002")
					}
					if err := rewriteIPPacket(data, source, destination); err != nil {
						t.Fatal(err)
					}
					packetTestAssertChecksums(t, data, offset, protocol)
					rewritten, err := parseIPPacket(data)
					if err != nil || rewritten.Source != source || rewritten.Destination != destination || !bytes.Equal(rewritten.Payload, payload) {
						t.Fatalf("NAT result incorrect: %+v, %v", rewritten, err)
					}
					if err := rewriteIPPacket(data, packet.Source, packet.Destination); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(data, original) {
						t.Fatal("reverse NAT did not restore original headers, options and payload")
					}
					// Offloaded/invalid input checksums must be repaired too.
					if !ipv6 {
						data[10], data[11] = 0, 0
					}
					checksumOffset := offset + 16
					if protocol == ProtoUDP {
						checksumOffset = offset + 6
					}
					data[checksumOffset], data[checksumOffset+1] = 0xab, 0xcd
					if err := rewriteIPPacket(data, packet.Source, packet.Destination); err != nil {
						t.Fatal(err)
					}
					packetTestAssertChecksums(t, data, offset, protocol)
					if !bytes.Equal(data, original) {
						t.Fatal("checksum repair did not reconstruct the valid original packet")
					}
				})
			}
		}
	}
}

func TestPacketUDPReply(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, size := range []int{0, 1, 5, maxUDPPayload} {
			t.Run(fmt.Sprintf("ipv6=%t/payload=%d", ipv6, size), func(t *testing.T) {
				original, _ := packetTestFixture(ipv6, ProtoUDP, nil, false)
				incoming, err := parseIPPacket(original)
				if err != nil {
					t.Fatal(err)
				}
				key := FlowKey{Protocol: ProtoUDP, Source: incoming.Source, Destination: incoming.Destination}
				payload := bytes.Repeat([]byte{0xa7}, size)
				data, err := makeUDPReply(key, payload)
				if err != nil {
					t.Fatal(err)
				}
				reply, err := parseIPPacket(data)
				if err != nil {
					t.Fatal(err)
				}
				if reply.Source != key.Destination || reply.Destination != key.Source || reply.Protocol != ProtoUDP || !bytes.Equal(reply.Payload, payload) {
					t.Fatal("UDP reply did not preserve the original remote source and application payload")
				}
				packetTestAssertChecksums(t, data, reply.TransportOffset, ProtoUDP)
				if size > 0 {
					payload[0] ^= 0xff
					if reply.Payload[0] != 0xa7 {
						t.Fatal("UDP reply must own its payload storage")
					}
				}
			})
		}
	}
}

func TestPacketUDPZeroChecksumIsEncodedAsFFFF(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		data, offset := packetTestFixture(ipv6, ProtoUDP, []byte{0, 0}, false)
		original, err := parseIPPacket(data)
		if err != nil {
			t.Fatal(err)
		}
		key := FlowKey{Protocol: ProtoUDP, Source: original.Destination, Destination: original.Source}
		data[offset+6], data[offset+7] = 0, 0
		payload := make([]byte, 2)
		binary.BigEndian.PutUint16(payload, packetTestChecksum(packetTestPseudoBytes(data, offset, ProtoUDP)))
		reply, err := makeUDPReply(key, payload)
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.BigEndian.Uint16(reply[offset+6:]); got != 0xffff {
			t.Fatalf("ipv6=%t: checksum %#04x, want 0xffff", ipv6, got)
		}
		packetTestAssertChecksums(t, reply, offset, ProtoUDP)
	}
}

func TestPacketTCPResetSequenceSpace(t *testing.T) {
	cases := []struct {
		name      string
		flags     byte
		sequence  uint32
		ack       uint32
		payload   []byte
		wantFlags byte
		wantSeq   uint32
		wantAck   uint32
	}{
		{"syn", 0x02, 100, 0, nil, 0x14, 0, 101},
		{"ack", 0x10, 100, 700, nil, 0x04, 700, 0},
		{"syn-ack", 0x12, 100, 700, nil, 0x04, 700, 0},
		{"data-fin", 0x01, 100, 0, []byte{1, 2, 3}, 0x14, 0, 104},
		{"syn-data-fin", 0x03, 100, 0, []byte{1, 2, 3}, 0x14, 0, 105},
		{"wrap", 0x02, ^uint32(0) - 1, 0, []byte{1, 2, 3}, 0x14, 0, 2},
		{"empty-no-ack", 0, 100, 700, nil, 0x14, 0, 100},
	}
	for _, ipv6 := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("ipv6=%t/%s", ipv6, tc.name), func(t *testing.T) {
				data, offset := packetTestFixture(ipv6, ProtoTCP, tc.payload, true)
				data[offset+13] = tc.flags
				binary.BigEndian.PutUint32(data[offset+4:], tc.sequence)
				binary.BigEndian.PutUint32(data[offset+8:], tc.ack)
				packet, err := parseIPPacket(data)
				if err != nil {
					t.Fatal(err)
				}
				reset, err := makeTCPReset(packet)
				if err != nil {
					t.Fatal(err)
				}
				reply, err := parseIPPacket(reset)
				if err != nil {
					t.Fatal(err)
				}
				if reply.Source != packet.Destination || reply.Destination != packet.Source || reply.TCPFlags != tc.wantFlags || reply.TCPSequence != tc.wantSeq || reply.TCPAck != tc.wantAck || len(reply.Payload) != 0 {
					t.Fatalf("incorrect TCP reset: %+v", reply)
				}
				if len(reset)-reply.TransportOffset != 20 || binary.BigEndian.Uint16(reset[reply.TransportOffset+14:]) != 0 {
					t.Fatal("reset must have a minimal TCP header and zero window")
				}
				packetTestAssertChecksums(t, reset, reply.TransportOffset, ProtoTCP)
			})
		}
	}
	for _, flags := range []byte{0x04, 0x14} {
		if reply, err := makeTCPReset(ipPacket{Protocol: ProtoTCP, TCPFlags: flags}); err != nil || reply != nil {
			t.Fatalf("RST must not generate another RST: %x, %v", reply, err)
		}
	}
	if _, err := makeTCPReset(ipPacket{Protocol: ProtoUDP}); err == nil {
		t.Fatal("UDP input accepted as TCP reset source")
	}
}

func TestPacketRejectsMalformedOrUnsupportedPackets(t *testing.T) {
	type invalidPacket struct {
		name string
		data []byte
		want string
	}
	cases := []invalidPacket{{"empty", nil, "empty"}, {"version", []byte{0x70}, "version"}}
	add := func(name string, base []byte, mutate func([]byte), want string) {
		data := bytes.Clone(base)
		mutate(data)
		cases = append(cases, invalidPacket{name, data, want})
	}
	ipv4, tcpOffset := packetTestFixture(false, ProtoTCP, []byte{1, 2, 3}, true)
	ipv6, _ := packetTestFixture(true, ProtoTCP, []byte{1, 2, 3}, true)
	udp4, udp4Offset := packetTestFixture(false, ProtoUDP, []byte{1, 2, 3}, false)
	add("ipv4-small-ihl", ipv4, func(d []byte) { d[0] = 0x44 }, "header length")
	add("ipv4-large-ihl", ipv4, func(d []byte) { d[0] = 0x4f }, "header length")
	add("ipv4-small-total", ipv4, func(d []byte) { binary.BigEndian.PutUint16(d[2:4], 10) }, "total length")
	add("ipv4-large-total", ipv4, func(d []byte) { binary.BigEndian.PutUint16(d[2:4], uint16(len(d)+1)) }, "total length")
	add("ipv4-reserved-flag", ipv4, func(d []byte) { d[6] |= 0x80 }, "reserved")
	add("ipv4-first-fragment", ipv4, func(d []byte) { d[6] = 0x20 }, "reassembly")
	add("ipv4-later-fragment", ipv4, func(d []byte) { d[7] = 1 }, "reassembly")
	add("ipv4-source-route", ipv4, func(d []byte) { d[20], d[21] = 131, 4 }, "source routing")
	add("ipv4-short-option", ipv4, func(d []byte) { d[20], d[21] = 7, 1 }, "option length")
	add("ipv4-long-option", ipv4, func(d []byte) { d[20], d[21] = 7, 5 }, "option length")
	add("ipv4-truncated-option", ipv4, func(d []byte) { copy(d[20:24], []byte{1, 1, 1, 7}) }, "truncated IPv4 option")
	add("ipv4-esp", ipv4, func(d []byte) { d[9] = 50 }, "protocol 50")
	add("tcp-small-offset", ipv4, func(d []byte) { d[tcpOffset+12] = 4 << 4 }, "TCP header length")
	add("tcp-large-offset", ipv4, func(d []byte) { d[tcpOffset+12] = 15 << 4 }, "TCP header length")
	add("tcp-short-option", ipv4, func(d []byte) { d[tcpOffset+20], d[tcpOffset+21] = 2, 1 }, "TCP option length")
	add("tcp-long-option", ipv4, func(d []byte) { d[tcpOffset+20], d[tcpOffset+21] = 2, 40 }, "TCP option length")
	add("udp-small-length", udp4, func(d []byte) { binary.BigEndian.PutUint16(d[udp4Offset+4:], 7) }, "UDP length")
	add("udp-large-length", udp4, func(d []byte) { binary.BigEndian.PutUint16(d[udp4Offset+4:], 12) }, "UDP length")
	add("udp-unclaimed-ip-bytes", udp4, func(d []byte) { binary.BigEndian.PutUint16(d[udp4Offset+4:], 8) }, "UDP length")
	add("ipv6-jumbogram", ipv6, func(d []byte) { d[4], d[5] = 0, 0 }, "jumbogram")
	add("ipv6-mismatched-length", ipv6, func(d []byte) { d[5]-- }, "payload length")
	add("ipv6-extension-length", ipv6, func(d []byte) { d[41] = 255 }, "extension header length")
	add("ipv6-late-hop-header", ipv6, func(d []byte) { d[40] = 0 }, "must be first")
	add("ipv6-active-route", ipv6, func(d []byte) { d[51] = 1 }, "source routing")
	add("ipv6-first-fragment", ipv6, func(d []byte) { d[67] = 1 }, "reassembly")
	add("ipv6-later-fragment", ipv6, func(d []byte) { d[67] = 8 }, "reassembly")
	add("ipv6-fragment-reserved-byte", ipv6, func(d []byte) { d[65] = 1 }, "reserved")
	add("ipv6-fragment-reserved-bit", ipv6, func(d []byte) { d[67] = 2 }, "reserved")
	add("ipv6-duplicate-fragment", ipv6, func(d []byte) { d[64] = 44 }, "duplicate")
	add("ipv6-esp", ipv6, func(d []byte) { d[6] = 50 }, "ESP/AH")
	add("ipv6-ah", ipv6, func(d []byte) { d[6] = 51 }, "ESP/AH")
	add("ipv6-no-next-header", ipv6, func(d []byte) { d[6] = 59 }, "next header 59")
	add("ipv6-bad-option-length", ipv6, func(d []byte) { d[43] = 5 }, "IPv6 option length")
	add("ipv6-truncated-option", ipv6, func(d []byte) { copy(d[42:48], []byte{0, 0, 0, 0, 0, 1}) }, "truncated IPv6 option")
	add("ipv6-jumbo-option", ipv6, func(d []byte) { d[42] = 0xc2 }, "Jumbo Payload")
	add("ipv6-home-address-option", ipv6, func(d []byte) { d[42] = 0xc9 }, "Home Address")
	add("ipv6-mapped-source", ipv6, func(d []byte) {
		addr := netip.MustParseAddr("::ffff:192.0.2.10").As16()
		copy(d[8:24], addr[:])
	}, "IPv4-mapped")
	for _, tc := range []struct {
		name   string
		base   []byte
		length int
		want   string
	}{
		{"ipv4-header", ipv4, 19, "truncated IPv4 header"},
		{"ipv6-header", ipv6, 39, "truncated IPv6 header"},
		{"tcp-header", ipv4, tcpOffset + 19, "truncated TCP header"},
		{"udp-header", udp4, udp4Offset + 7, "truncated UDP header"},
		{"ipv6-extension", ipv6, 44, "truncated IPv6 extension"},
		{"ipv6-fragment", ipv6, 68, "truncated IPv6 Fragment"},
	} {
		data := bytes.Clone(tc.base[:tc.length])
		if tc.length >= 40 && data[0]>>4 == 6 {
			binary.BigEndian.PutUint16(data[4:6], uint16(len(data)-40))
		} else if tc.length >= 20 && data[0]>>4 == 4 {
			binary.BigEndian.PutUint16(data[2:4], uint16(len(data)))
		}
		cases = append(cases, invalidPacket{tc.name, data, tc.want})
	}
	hugeUDP, _ := packetTestFixture(true, ProtoUDP, make([]byte, maxUDPPayload+1), false)
	cases = append(cases, invalidPacket{"oversized-udp", hugeUDP, "exceeds"})
	hugeIP := make([]byte, 65576)
	hugeIP[0] = 0x60
	binary.BigEndian.PutUint16(hugeIP[4:6], 65535)
	cases = append(cases, invalidPacket{"oversized-ip", hugeIP, "payload length"})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := bytes.Clone(tc.data)
			if _, err := parseIPPacket(tc.data); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want %q", err, tc.want)
			}
			if err := rewriteIPPacket(tc.data, netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2")); err == nil {
				t.Fatal("rewrite accepted an invalid packet")
			}
			if !bytes.Equal(before, tc.data) {
				t.Fatal("failed rewrite modified its input")
			}
		})
	}
}

func TestPacketRejectsEveryTruncatedPrefixAndTrailingBytes(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			data, _ := packetTestFixture(ipv6, protocol, []byte{1, 2, 3, 4, 5}, true)
			for length := 0; length < len(data); length++ {
				if _, err := parseIPPacket(data[:length]); err == nil {
					t.Fatalf("ipv6=%t %s: accepted truncated prefix of length %d", ipv6, protocol, length)
				}
			}
			if _, err := parseIPPacket(append(bytes.Clone(data), 0)); err == nil {
				t.Fatalf("ipv6=%t %s: accepted bytes beyond IP length", ipv6, protocol)
			}
		}
	}
}

func TestPacketRejectsInvalidEndpointsAndOversizedReplies(t *testing.T) {
	data, _ := packetTestFixture(false, ProtoUDP, nil, false)
	packet, err := parseIPPacket(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoints := range [][2]netip.AddrPort{
		{{}, packet.Destination},
		{packet.Source, {}},
		{packet.Source, netip.MustParseAddrPort("[::1]:443")},
		{netip.MustParseAddrPort("[::1]:1234"), netip.MustParseAddrPort("[::1]:443")},
		{netip.MustParseAddrPort("[fe80::1%eth0]:1234"), netip.MustParseAddrPort("[fe80::2%eth0]:443")},
	} {
		before := bytes.Clone(data)
		if err := rewriteIPPacket(data, endpoints[0], endpoints[1]); err == nil {
			t.Fatalf("rewrite accepted invalid endpoints %v", endpoints)
		}
		if !bytes.Equal(data, before) {
			t.Fatal("invalid endpoints changed the packet")
		}
	}
	key := FlowKey{Protocol: ProtoUDP, Source: packet.Source, Destination: packet.Destination}
	if _, err := makeUDPReply(key, make([]byte, maxUDPPayload+1)); err == nil {
		t.Fatal("oversized UDP reply accepted")
	}
	key.Protocol = ProtoTCP
	if _, err := makeUDPReply(key, nil); err == nil {
		t.Fatal("TCP flow accepted for UDP reply")
	}
	key.Protocol, key.Source = ProtoUDP, netip.AddrPort{}
	if _, err := makeUDPReply(key, nil); err == nil {
		t.Fatal("invalid UDP source accepted")
	}
	key.Source, key.Destination = netip.MustParseAddrPort("[::ffff:192.0.2.10]:50123"), packet.Destination
	reply, err := makeUDPReply(key, nil)
	if err != nil || len(reply) != 28 || reply[0]>>4 != 4 {
		t.Fatalf("mapped IPv4 endpoint should generate an IPv4 reply: %v", err)
	}
}

func FuzzParseIPPacket(f *testing.F) {
	for _, ipv6 := range []bool{false, true} {
		for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
			for _, extras := range []bool{false, true} {
				data, _ := packetTestFixture(ipv6, protocol, []byte{1, 2, 3, 4, 5}, extras)
				f.Add(data)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		packet, err := parseIPPacket(data)
		if err != nil {
			return
		}
		if packet.TransportOffset < 20 || packet.TransportOffset >= len(data) || !packet.Source.IsValid() || !packet.Destination.IsValid() {
			t.Fatal("parser accepted inconsistent packet metadata")
		}
		payload := bytes.Clone(packet.Payload)
		rewritten := bytes.Clone(data)
		if err := rewriteIPPacket(rewritten, packet.Source, packet.Destination); err != nil {
			t.Fatalf("valid packet could not be rewritten: %v", err)
		}
		packetTestAssertChecksums(t, rewritten, packet.TransportOffset, packet.Protocol)
		again, err := parseIPPacket(rewritten)
		if err != nil || again.Source != packet.Source || again.Destination != packet.Destination || !bytes.Equal(again.Payload, payload) {
			t.Fatalf("rewrite corrupted a valid packet: %v", err)
		}
	})
}

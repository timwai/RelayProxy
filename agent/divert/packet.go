package divert

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// ipPacket describes one complete IP packet. Bytes and Payload alias the input;
// callers retaining a packet after the next receive must copy it first.
type ipPacket struct {
	Bytes               []byte
	Source, Destination netip.AddrPort
	Protocol            Protocol
	TCPFlags            uint8
	TCPSequence, TCPAck uint32
	TransportOffset     int
	Payload             []byte
}

// parseIPPacket accepts complete TCP/UDP packets, including IPv4 options and
// supported IPv6 extension headers. Checksums are deliberately not verified:
// captured outbound packets can still be awaiting hardware checksum offload.
// Fragment reassembly, IPsec, source routing and jumbograms need different
// handling and must never be mistaken for ordinary transport packets.
func parseIPPacket(data []byte) (ipPacket, error) {
	var packet ipPacket
	if len(data) == 0 {
		return packet, errors.New("divert: empty IP packet")
	}
	var source, destination netip.Addr
	var next byte
	var offset int
	switch data[0] >> 4 {
	case 4:
		if len(data) < 20 {
			return packet, errors.New("divert: truncated IPv4 header")
		}
		offset = int(data[0]&15) * 4
		if offset < 20 || offset > len(data) {
			return packet, errors.New("divert: invalid IPv4 header length")
		}
		if total := int(binary.BigEndian.Uint16(data[2:4])); total < offset || total != len(data) {
			return packet, errors.New("divert: IPv4 total length does not match packet")
		}
		fragment := binary.BigEndian.Uint16(data[6:8])
		if fragment&0x8000 != 0 {
			return packet, errors.New("divert: reserved IPv4 fragment flag is set")
		}
		if fragment&0x3fff != 0 {
			return packet, errors.New("divert: fragmented IPv4 packets require reassembly")
		}
		if err := validatePacketOptions(data[20:offset], "IPv4"); err != nil {
			return packet, err
		}
		source = netip.AddrFrom4([4]byte(data[12:16]))
		destination = netip.AddrFrom4([4]byte(data[16:20]))
		next = data[9]
	case 6:
		if len(data) < 40 {
			return packet, errors.New("divert: truncated IPv6 header")
		}
		payloadLength := int(binary.BigEndian.Uint16(data[4:6]))
		if payloadLength == 0 {
			return packet, errors.New("divert: IPv6 jumbograms or empty payloads are unsupported")
		}
		if payloadLength+40 != len(data) {
			return packet, errors.New("divert: IPv6 payload length does not match packet")
		}
		source = netip.AddrFrom16([16]byte(data[8:24]))
		destination = netip.AddrFrom16([16]byte(data[24:40]))
		if source.Is4In6() || destination.Is4In6() {
			return packet, errors.New("divert: IPv4-mapped addresses are invalid on an IPv6 wire packet")
		}
		var err error
		offset, next, err = ipv6PacketTransport(data)
		if err != nil {
			return packet, err
		}
	default:
		return packet, errors.New("divert: unsupported IP version")
	}

	transport := data[offset:]
	switch next {
	case 6:
		if len(transport) < 20 {
			return packet, errors.New("divert: truncated TCP header")
		}
		headerLength := int(transport[12]>>4) * 4
		if headerLength < 20 || headerLength > len(transport) {
			return packet, errors.New("divert: invalid TCP header length")
		}
		if err := validatePacketOptions(transport[20:headerLength], "TCP"); err != nil {
			return packet, err
		}
		packet.Protocol = ProtoTCP
		packet.TCPFlags = transport[13]
		packet.TCPSequence = binary.BigEndian.Uint32(transport[4:8])
		packet.TCPAck = binary.BigEndian.Uint32(transport[8:12])
		packet.Payload = transport[headerLength:]
	case 17:
		if len(transport) < 8 {
			return packet, errors.New("divert: truncated UDP header")
		}
		if length := int(binary.BigEndian.Uint16(transport[4:6])); length < 8 || length != len(transport) {
			return packet, errors.New("divert: UDP length does not match IP payload")
		}
		if len(transport)-8 > maxUDPPayload {
			return packet, fmt.Errorf("divert: UDP payload exceeds %d bytes", maxUDPPayload)
		}
		packet.Protocol = ProtoUDP
		packet.Payload = transport[8:]
	default:
		return packet, fmt.Errorf("divert: unsupported IP transport protocol %d", next)
	}
	packet.Bytes = data
	packet.TransportOffset = offset
	packet.Source = netip.AddrPortFrom(source, binary.BigEndian.Uint16(transport[:2]))
	packet.Destination = netip.AddrPortFrom(destination, binary.BigEndian.Uint16(transport[2:4]))
	return packet, nil
}

func validatePacketOptions(options []byte, header string) error {
	for offset := 0; offset < len(options); {
		kind := options[offset]
		switch kind {
		case 0: // End of options; the rest is padding.
			return nil
		case 1: // NOP occupies one byte.
			offset++
			continue
		}
		if offset+2 > len(options) {
			return fmt.Errorf("divert: truncated %s option", header)
		}
		length := int(options[offset+1])
		if length < 2 || length > len(options)-offset {
			return fmt.Errorf("divert: invalid %s option length", header)
		}
		if header == "IPv4" && (kind == 131 || kind == 137) {
			return errors.New("divert: IPv4 source routing is unsupported")
		}
		offset += length
	}
	return nil
}

func ipv6PacketTransport(data []byte) (int, byte, error) {
	next, offset := data[6], 40
	fragmentSeen := false
	for {
		switch next {
		case 0, 43, 60: // Hop-by-Hop, Routing, Destination Options.
			if offset+8 > len(data) {
				return 0, 0, errors.New("divert: truncated IPv6 extension header")
			}
			length := (int(data[offset+1]) + 1) * 8
			if length > len(data)-offset {
				return 0, 0, errors.New("divert: invalid IPv6 extension header length")
			}
			if next == 0 && offset != 40 {
				return 0, 0, errors.New("divert: IPv6 Hop-by-Hop header must be first")
			}
			if next == 43 {
				// When Segments Left is zero the current destination is final.
				// Otherwise the pseudo-header and actual flow target differ.
				if data[offset+3] != 0 {
					return 0, 0, errors.New("divert: active IPv6 source routing is unsupported")
				}
			} else if err := validateIPv6PacketOptions(data[offset+2 : offset+length]); err != nil {
				return 0, 0, err
			}
			next, offset = data[offset], offset+length
		case 44:
			if fragmentSeen {
				return 0, 0, errors.New("divert: duplicate IPv6 Fragment header")
			}
			if offset+8 > len(data) {
				return 0, 0, errors.New("divert: truncated IPv6 Fragment header")
			}
			fragment := binary.BigEndian.Uint16(data[offset+2 : offset+4])
			if data[offset+1] != 0 || fragment&6 != 0 {
				return 0, 0, errors.New("divert: reserved IPv6 Fragment bits are set")
			}
			if fragment != 0 {
				return 0, 0, errors.New("divert: fragmented IPv6 packets require reassembly")
			}
			fragmentSeen = true // Offset=0 and M=0 is an atomic fragment.
			next, offset = data[offset], offset+8
		case 50, 51:
			return 0, 0, errors.New("divert: IPv6 ESP/AH packets cannot be transparently rewritten")
		case 6, 17:
			return offset, next, nil
		default:
			return 0, 0, fmt.Errorf("divert: unsupported IPv6 next header %d", next)
		}
	}
}

func validateIPv6PacketOptions(options []byte) error {
	for offset := 0; offset < len(options); {
		kind := options[offset]
		if kind == 0 { // Pad1.
			offset++
			continue
		}
		if offset+2 > len(options) {
			return errors.New("divert: truncated IPv6 option")
		}
		length := int(options[offset+1]) + 2
		if length > len(options)-offset {
			return errors.New("divert: invalid IPv6 option length")
		}
		if kind == 0xc2 || kind == 0xc9 {
			return errors.New("divert: IPv6 Jumbo Payload/Home Address options are unsupported")
		}
		offset += length
	}
	return nil
}

// rewriteIPPacket leaves options and transport data intact and repairs every
// affected checksum, even when the captured packet had offloaded checksums.
// Invalid packets/endpoints are rejected before any bytes are changed.
func rewriteIPPacket(data []byte, source, destination netip.AddrPort) error {
	packet, err := parseIPPacket(data)
	if err != nil {
		return err
	}
	source, destination, err = packetEndpointPair(source, destination)
	if err != nil {
		return err
	}
	if source.Addr().Is4() != (data[0]>>4 == 4) {
		return errors.New("divert: cannot change the address family of an IP packet")
	}
	if source.Addr().Is4() {
		src, dst := source.Addr().As4(), destination.Addr().As4()
		copy(data[12:16], src[:])
		copy(data[16:20], dst[:])
	} else {
		src, dst := source.Addr().As16(), destination.Addr().As16()
		copy(data[8:24], src[:])
		copy(data[24:40], dst[:])
	}
	binary.BigEndian.PutUint16(data[packet.TransportOffset:], source.Port())
	binary.BigEndian.PutUint16(data[packet.TransportOffset+2:], destination.Port())
	repairPacketChecksums(packet)
	return nil
}

func packetEndpointPair(source, destination netip.AddrPort) (netip.AddrPort, netip.AddrPort, error) {
	if !source.IsValid() || !destination.IsValid() {
		return source, destination, errors.New("divert: invalid IP packet endpoint")
	}
	if source.Addr().Zone() != "" || destination.Addr().Zone() != "" {
		return source, destination, errors.New("divert: packet endpoint zones require interface metadata")
	}
	source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
	destination = netip.AddrPortFrom(destination.Addr().Unmap(), destination.Port())
	if source.Addr().Is4() != destination.Addr().Is4() {
		return source, destination, errors.New("divert: IP packet endpoint address families differ")
	}
	return source, destination, nil
}

// newTransportPacket allocates a minimal unfragmented IP packet. IPv4 DF makes
// identification zero valid; synthesized replies are injected inbound locally.
func newTransportPacket(source, destination netip.AddrPort, protocol Protocol, length int) (ipPacket, error) {
	source, destination, err := packetEndpointPair(source, destination)
	if err != nil {
		return ipPacket{}, err
	}
	headerLength, next, minimumLength := 40, byte(6), 20
	if source.Addr().Is4() {
		headerLength = 20
	}
	switch protocol {
	case ProtoTCP:
	case ProtoUDP:
		next, minimumLength = 17, 8
	default:
		return ipPacket{}, errors.New("divert: unsupported transport packet protocol")
	}
	if length < minimumLength || length > 65535 || (headerLength == 20 && length > 65535-headerLength) {
		return ipPacket{}, errors.New("divert: transport packet exceeds IP length limits")
	}
	data := make([]byte, headerLength+length)
	if headerLength == 20 {
		data[0], data[6], data[8], data[9] = 0x45, 0x40, 64, next
		binary.BigEndian.PutUint16(data[2:4], uint16(len(data)))
		src, dst := source.Addr().As4(), destination.Addr().As4()
		copy(data[12:16], src[:])
		copy(data[16:20], dst[:])
	} else {
		data[0], data[6], data[7] = 0x60, next, 64
		binary.BigEndian.PutUint16(data[4:6], uint16(length))
		src, dst := source.Addr().As16(), destination.Addr().As16()
		copy(data[8:24], src[:])
		copy(data[24:40], dst[:])
	}
	binary.BigEndian.PutUint16(data[headerLength:], source.Port())
	binary.BigEndian.PutUint16(data[headerLength+2:], destination.Port())
	return ipPacket{Bytes: data, Source: source, Destination: destination, Protocol: protocol, TransportOffset: headerLength}, nil
}

// makeUDPReply restores the original remote source endpoint, including its port.
func makeUDPReply(key FlowKey, payload []byte) ([]byte, error) {
	if key.Protocol != ProtoUDP {
		return nil, errors.New("divert: UDP reply requires a UDP flow")
	}
	if len(payload) > maxUDPPayload {
		return nil, fmt.Errorf("divert: UDP payload exceeds %d bytes", maxUDPPayload)
	}
	packet, err := newTransportPacket(key.Destination, key.Source, ProtoUDP, 8+len(payload))
	if err != nil {
		return nil, err
	}
	udp := packet.Bytes[packet.TransportOffset:]
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], payload)
	repairPacketChecksums(packet)
	return packet.Bytes, nil
}

// makeTCPReset follows RFC 9293: acknowledge the incoming sequence space only
// when the segment has no ACK. RST segments are never answered (nil, nil).
func makeTCPReset(packet ipPacket) ([]byte, error) {
	if packet.Protocol != ProtoTCP {
		return nil, errors.New("divert: TCP reset requires a TCP packet")
	}
	if packet.TCPFlags&0x04 != 0 {
		return nil, nil
	}
	if len(packet.Payload) > 65535-20 {
		return nil, errors.New("divert: invalid TCP payload length")
	}
	reply, err := newTransportPacket(packet.Destination, packet.Source, ProtoTCP, 20)
	if err != nil {
		return nil, err
	}
	tcp := reply.Bytes[reply.TransportOffset:]
	tcp[12], tcp[13] = 5<<4, 0x04
	if packet.TCPFlags&0x10 != 0 {
		binary.BigEndian.PutUint32(tcp[4:8], packet.TCPAck)
	} else {
		ack := packet.TCPSequence + uint32(len(packet.Payload))
		if packet.TCPFlags&0x02 != 0 { // SYN consumes one sequence number.
			ack++
		}
		if packet.TCPFlags&0x01 != 0 { // FIN also consumes one.
			ack++
		}
		tcp[13] |= 0x10
		binary.BigEndian.PutUint32(tcp[8:12], ack)
	}
	repairPacketChecksums(reply)
	return reply.Bytes, nil
}

func repairPacketChecksums(packet ipPacket) {
	data, offset := packet.Bytes, packet.TransportOffset
	if data[0]>>4 == 4 {
		data[10], data[11] = 0, 0
		binary.BigEndian.PutUint16(data[10:12], packetChecksum(data[:offset]))
	}
	checksumOffset, protocol := 16, byte(6)
	if packet.Protocol == ProtoUDP {
		checksumOffset, protocol = 6, 17
	}
	transport := data[offset:]
	transport[checksumOffset], transport[checksumOffset+1] = 0, 0
	var pseudo [40]byte
	length := 40
	if data[0]>>4 == 4 {
		copy(pseudo[:8], data[12:20])
		pseudo[9] = protocol
		binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(transport)))
		length = 12
	} else {
		copy(pseudo[:32], data[8:40])
		binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(transport)))
		pseudo[39] = protocol
	}
	checksum := packetChecksum(pseudo[:length], transport)
	if packet.Protocol == ProtoUDP && checksum == 0 {
		checksum = 0xffff // Zero on the wire means "no checksum" for IPv4 UDP.
	}
	binary.BigEndian.PutUint16(transport[checksumOffset:], checksum)
}

func packetChecksum(parts ...[]byte) uint16 {
	var sum uint32
	var high byte
	odd := false
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		if odd {
			sum += uint32(high)<<8 | uint32(part[0])
			part, odd = part[1:], false
		}
		for len(part) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(part[:2]))
			part = part[2:]
		}
		if len(part) != 0 {
			high, odd = part[0], true
		}
	}
	if odd {
		sum += uint32(high) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

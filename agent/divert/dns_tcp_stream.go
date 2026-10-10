package divert

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"
)

// Passive DNS/TCP observation cannot delay or change the application socket.
// Limit both the number and size of reassembly buffers to avoid converting
// arbitrary port-53 traffic into unbounded application-level state.
const (
	dnsTCPMaxStreams = 512
	dnsTCPMaxFrame = 8192
	dnsTCPStreamTTL = 10 * time.Second
)

type dnsTCPStreamKey struct {
	source, destination netip.AddrPort
}

type dnsTCPStream struct {
	next uint32
	buffer []byte
	expires time.Time
	invalid bool
}

type dnsTCPObserver struct {
	mu sync.Mutex
	streams map[dnsTCPStreamKey]*dnsTCPStream
	now func() time.Time
}

func newDNSTCPObserver() *dnsTCPObserver {
	return &dnsTCPObserver{
		streams: make(map[dnsTCPStreamKey]*dnsTCPStream),
		now: time.Now,
	}
}

// observe returns complete DNS/TCP messages carried in sequential TCP
// segments. Out-of-order segments and untrusted length prefixes invalidate
// only that direction until a new SYN or expiration. No payload is changed.
func (o *dnsTCPObserver) observe(p ipPacket) [][]byte {
	if p.Protocol != ProtoTCP {
		return nil
	}
	key := dnsTCPStreamKey{source: p.Source, destination: p.Destination}
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	if p.TCPFlags&0x02 != 0 {
		delete(o.streams, key)
	}
	if p.TCPFlags&0x04 != 0 {
		delete(o.streams, key)
		return nil
	}
	if len(p.Payload) == 0 {
		if p.TCPFlags&0x01 != 0 {
			delete(o.streams, key)
		}
		return nil
	}
	state := o.streams[key]
	if state != nil && !state.expires.After(now) {
		delete(o.streams, key)
		state = nil
	}
	if state == nil {
		if len(o.streams) >= dnsTCPMaxStreams {
			for k, v := range o.streams {
				if !v.expires.After(now) {
					delete(o.streams, k)
				}
			}
			if len(o.streams) >= dnsTCPMaxStreams {
				return nil
			}
		}
		state = &dnsTCPStream{next: p.TCPSequence, expires: now.Add(dnsTCPStreamTTL)}
		o.streams[key] = state
	}
	if state.invalid {
		return nil
	}
	if p.TCPSequence != state.next {
		// A complete retransmission can be ignored, but a gap or partial
		// overlap must not be interpreted as the start of a DNS frame.
		if p.TCPSequence < state.next && uint64(p.TCPSequence)+uint64(len(p.Payload)) <= uint64(state.next) {
			return nil
		}
		state.invalid = true
		state.buffer = nil
		return nil
	}
	state.next += uint32(len(p.Payload))
	state.expires = now.Add(dnsTCPStreamTTL)
	if len(state.buffer)+len(p.Payload) > dnsTCPMaxFrame+2 {
		state.invalid = true
		state.buffer = nil
		return nil
	}
	state.buffer = append(state.buffer, p.Payload...)
	var messages [][]byte
	for len(state.buffer) >= 2 {
		size := int(binary.BigEndian.Uint16(state.buffer[:2]))
		if size < 12 || size > dnsTCPMaxFrame {
			state.invalid = true
			state.buffer = nil
			return messages
		}
		if len(state.buffer) < size+2 {
			break
		}
		messages = append(messages, append([]byte(nil), state.buffer[2:size+2]...))
		state.buffer = state.buffer[size+2:]
	}
	if p.TCPFlags&0x01 != 0 {
		delete(o.streams, key)
	}
	return messages
}

func (d *dnsAssociations) observeTCPQuery(p ipPacket) {
	if d == nil || d.tcp == nil || p.Destination.Port() != 53 {
		return
	}
	for _, message := range d.tcp.observe(p) {
		d.queryProtocol(ProtoTCP, p.Source, p.Destination, message)
	}
}

func (d *dnsAssociations) observeTCPResponse(p ipPacket) {
	if d == nil || d.tcp == nil || p.Source.Port() != 53 {
		return
	}
	for _, message := range d.tcp.observe(p) {
		d.responseProtocol(ProtoTCP, p.Source, p.Destination, message)
	}
}

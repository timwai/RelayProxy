package punch

import (
	"encoding/binary"
	"sync"
	"time"

	"relayproxy/internal/protocol"
)

const (
	maxReassemblyPackets = 32
	fragmentExpiry       = 200 * time.Millisecond
)

// FragmentUDP emits the same bounded fragments used by the QUIC relay. The
// callback consumes each frame synchronously, so the stack buffer is reused and
// no per-fragment allocation is needed on the direct P2P hot path.
func FragmentUDP(packetID uint32, payload []byte, send func([]byte) error) error {
	if len(payload) > protocol.MaxUDPDatagramPayload {
		return protocol.ErrUDPFragment
	}
	count := max(1, (len(payload)+protocol.UDPFragmentPayload-1)/protocol.UDPFragmentPayload)
	var frame [protocol.UDPFragmentHeaderSize + protocol.UDPFragmentPayload]byte
	for i := 0; i < count; i++ {
		start := min(i*protocol.UDPFragmentPayload, len(payload))
		end := min((i+1)*protocol.UDPFragmentPayload, len(payload))
		binary.BigEndian.PutUint32(frame[:4], packetID)
		binary.BigEndian.PutUint16(frame[4:6], uint16(len(payload)))
		frame[6], frame[7] = byte(i), byte(count)
		copy(frame[protocol.UDPFragmentHeaderSize:], payload[start:end])
		if err := send(frame[:protocol.UDPFragmentHeaderSize+end-start]); err != nil {
			return err
		}
	}
	return nil
}

type pendingFragmentPacket struct {
	total    uint16
	count    uint8
	seen     uint64
	received int
	data     []byte
	created  time.Time
}

// Reassembler reconstructs one original UDP packet from authenticated direct
// path fragments. It is safe for a read loop plus connection cleanup to call
// concurrently.
type Reassembler struct {
	mu      sync.Mutex
	pending map[uint32]*pendingFragmentPacket
}

func NewReassembler() *Reassembler {
	return &Reassembler{pending: make(map[uint32]*pendingFragmentPacket)}
}

// Feed copies one validated fragment into dst when the original packet is
// complete. It returns (0, false, nil) while more fragments are needed.
func (r *Reassembler) Feed(frame, dst []byte) (int, bool, error) {
	f, err := protocol.DecodeUDPFragment(frame)
	if err != nil {
		return 0, false, err
	}
	if f.Count == 1 {
		if len(dst) < len(f.Payload) {
			return 0, false, ioErrShortBuffer{}
		}
		return copy(dst, f.Payload), true, nil
	}

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reapLocked(now)
	p := r.pending[f.PacketID]
	if p == nil {
		if len(r.pending) >= maxReassemblyPackets {
			r.dropOldestLocked()
		}
		p = &pendingFragmentPacket{total: f.Total, count: f.Count, data: make([]byte, int(f.Total)), created: now}
		r.pending[f.PacketID] = p
	}
	if p.total != f.Total || p.count != f.Count {
		delete(r.pending, f.PacketID)
		return 0, false, nil
	}
	mask := uint64(1) << f.Index
	if p.seen&mask == 0 {
		copy(p.data[int(f.Index)*protocol.UDPFragmentPayload:], f.Payload)
		p.seen |= mask
		p.received++
	}
	if p.received != int(p.count) {
		return 0, false, nil
	}
	if len(dst) < len(p.data) {
		delete(r.pending, f.PacketID)
		return 0, false, ioErrShortBuffer{}
	}
	n := copy(dst, p.data)
	delete(r.pending, f.PacketID)
	return n, true, nil
}

func (r *Reassembler) reapLocked(now time.Time) {
	for id, p := range r.pending {
		if now.Sub(p.created) >= fragmentExpiry {
			delete(r.pending, id)
		}
	}
}

func (r *Reassembler) dropOldestLocked() {
	var oldestID uint32
	var oldest time.Time
	for id, p := range r.pending {
		if oldest.IsZero() || p.created.Before(oldest) {
			oldestID, oldest = id, p.created
		}
	}
	if !oldest.IsZero() {
		delete(r.pending, oldestID)
	}
}

func (r *Reassembler) Close() {
	r.mu.Lock()
	r.pending = make(map[uint32]*pendingFragmentPacket)
	r.mu.Unlock()
}

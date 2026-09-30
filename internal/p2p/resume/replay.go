package resume

import (
	"errors"
	"sync"
)

var ErrReplayLimit = errors.New("resumable stream replay buffer limit reached")

type segment struct {
	seq  uint64
	data []byte
}

// ReplayBuffer retains unacknowledged bytes for one stream direction. It is
// bounded so a peer that stops acknowledging cannot grow process memory
// without limit.
type ReplayBuffer struct {
	mu       sync.Mutex
	limit    int
	next     uint64
	acked    uint64
	buffered int
	segments []segment
}

func NewReplayBuffer(limit int) *ReplayBuffer {
	if limit <= 0 {
		limit = 512 << 10
	}
	return &ReplayBuffer{limit: limit}
}

func (b *ReplayBuffer) Append(payload []byte) (uint64, error) {
	if b == nil || len(payload) == 0 {
		return 0, ErrFrame
	}
	if len(payload) > MaxPayload {
		return 0, ErrPayloadSize
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buffered+len(payload) > b.limit {
		return 0, ErrReplayLimit
	}
	seq := b.next
	copyPayload := append([]byte(nil), payload...)
	b.segments = append(b.segments, segment{seq: seq, data: copyPayload})
	b.next += uint64(len(copyPayload))
	b.buffered += len(copyPayload)
	return seq, nil
}

// Ack releases all bytes below offset. Duplicate ACKs are harmless. An ACK
// beyond the amount ever sent is rejected because accepting it would make a
// reconnect silently skip bytes.
func (b *ReplayBuffer) Ack(offset uint64) error {
	if b == nil {
		return ErrFrame
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if offset > b.next {
		return ErrFrame
	}
	if offset <= b.acked {
		return nil
	}
	b.acked = offset

	for len(b.segments) > 0 {
		first := &b.segments[0]
		end := first.seq + uint64(len(first.data))
		if end <= offset {
			b.buffered -= len(first.data)
			b.segments[0] = segment{}
			b.segments = b.segments[1:]
			continue
		}
		if first.seq < offset {
			trim := int(offset - first.seq)
			first.data = append([]byte(nil), first.data[trim:]...)
			first.seq = offset
			b.buffered -= trim
		}
		break
	}
	return nil
}

func (b *ReplayBuffer) Snapshot() []Frame {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Frame, 0, len(b.segments))
	for _, item := range b.segments {
		out = append(out, Frame{Type: FrameData, Seq: item.seq, Payload: append([]byte(nil), item.data...)})
	}
	return out
}

func (b *ReplayBuffer) Acked() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.acked
}

func (b *ReplayBuffer) Next() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.next
}

func (b *ReplayBuffer) Buffered() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffered
}

// Receiver de-duplicates replayed bytes after a rebind and rejects gaps.
// Transport streams are ordered, so out-of-order buffering is unnecessary.
type Receiver struct {
	mu       sync.Mutex
	expected uint64
}

func (r *Receiver) Accept(seq uint64, payload []byte) ([]byte, uint64, error) {
	if r == nil || len(payload) == 0 {
		return nil, 0, ErrFrame
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if seq > r.expected {
		return nil, r.expected, ErrSequenceGap
	}
	end := seq + uint64(len(payload))
	if end <= r.expected {
		return nil, r.expected, nil
	}
	start := 0
	if seq < r.expected {
		start = int(r.expected - seq)
	}
	fresh := append([]byte(nil), payload[start:]...)
	r.expected += uint64(len(fresh))
	return fresh, r.expected, nil
}

func (r *Receiver) Expected() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.expected
}

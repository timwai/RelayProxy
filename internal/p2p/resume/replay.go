package resume

import (
	"errors"
	"sync"
)

var ErrReplayLimit = errors.New("resumable stream replay buffer limit reached")

type segment struct {
	seq  uint64
	data []byte
	wire []byte
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
	spare    []byte // largest acknowledged wire block, capped by HeaderSize+MaxPayload
}

func NewReplayBuffer(limit int) *ReplayBuffer {
	if limit <= 0 {
		limit = 512 << 10
	}
	return &ReplayBuffer{limit: limit}
}

func (b *ReplayBuffer) Append(payload []byte) (uint64, error) {
	seq, _, _, err := b.appendRetained(payload)
	return seq, err
}

// appendRetained returns the replay buffer's immutable payload copy. Internal
// send paths can write that copy directly instead of allocating the same frame
// payload twice. Callers must not mutate the returned bytes.
func (b *ReplayBuffer) appendRetained(payload []byte) (uint64, []byte, []byte, error) {
	if b == nil {
		return 0, nil, nil, ErrFrame
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.appendRetainedLocked(payload)
}

func (b *ReplayBuffer) appendRetainedLocked(payload []byte) (uint64, []byte, []byte, error) {
	if b == nil || len(payload) == 0 {
		return 0, nil, nil, ErrFrame
	}
	if len(payload) > MaxPayload {
		return 0, nil, nil, ErrPayloadSize
	}
	if b.buffered+len(payload) > b.limit {
		return 0, nil, nil, ErrReplayLimit
	}
	if uint64(len(payload)) > ^uint64(0)-b.next {
		return 0, nil, nil, ErrFrame
	}
	seq := b.next
	wireSize := HeaderSize + len(payload)
	var wire []byte
	if cap(b.spare) >= wireSize {
		wire = b.spare[:wireSize]
		b.spare = nil
	} else {
		wire = make([]byte, wireSize)
	}
	copyPayload := wire[HeaderSize:]
	copy(copyPayload, payload)
	b.segments = append(b.segments, segment{seq: seq, data: copyPayload, wire: wire})
	b.next += uint64(len(copyPayload))
	b.buffered += len(copyPayload)
	return seq, copyPayload, wire, nil
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
	return b.ackLocked(offset)
}

func (b *ReplayBuffer) ackLocked(offset uint64) error {
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
			if cap(first.wire) > cap(b.spare) && cap(first.wire) <= HeaderSize+MaxPayload {
				b.spare = first.wire[:0]
			}
			b.segments[0] = segment{}
			if len(b.segments) == 1 {
				// Keep the small segment index allocation for the next frame.
				b.segments = b.segments[:0]
				break
			}
			b.segments = b.segments[1:]
			continue
		}
		if first.seq < offset {
			trim := int(offset - first.seq)
			// Only the leading segment can be partially acknowledged. Retaining
			// at most one frame's backing array avoids a hot-path copy while the
			// replay byte limit continues to bound all subsequently queued data.
			first.data = first.data[trim:]
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
	return b.snapshotLocked()
}

func (b *ReplayBuffer) snapshotLocked() []Frame {
	out := make([]Frame, 0, len(b.segments))
	for _, item := range b.segments {
		wire := make([]byte, HeaderSize+len(item.data))
		payload := wire[HeaderSize:]
		copy(payload, item.data)
		out = append(out, Frame{Type: FrameData, Seq: item.seq, Payload: payload, wire: wire})
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
	fresh, ack, err := r.acceptBorrowed(seq, payload)
	return append([]byte(nil), fresh...), ack, err
}

// acceptBorrowed returns a view into payload, which the receiver never retains.
func (r *Receiver) acceptBorrowed(seq uint64, payload []byte) ([]byte, uint64, error) {
	if r == nil {
		return nil, 0, ErrFrame
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acceptBorrowedLocked(seq, payload)
}

func (r *Receiver) acceptBorrowedLocked(seq uint64, payload []byte) ([]byte, uint64, error) {
	if r == nil || len(payload) == 0 {
		return nil, 0, ErrFrame
	}
	if seq > r.expected {
		return nil, r.expected, ErrSequenceGap
	}
	if uint64(len(payload)) > ^uint64(0)-seq {
		return nil, r.expected, ErrFrame
	}
	end := seq + uint64(len(payload))
	if end <= r.expected {
		return nil, r.expected, nil
	}
	start := 0
	if seq < r.expected {
		start = int(r.expected - seq)
	}
	fresh := payload[start:]
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

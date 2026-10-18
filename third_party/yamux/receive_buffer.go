// RelayProxy changes to HashiCorp yamux v0.1.2; licensed under MPL-2.0.
package yamux

import (
	"errors"
	"io"
)

var ErrSessionBufferExceeded = errors.New("session receive buffer budget exceeded")

// ReceiveBufferBytes reports reserved receive-array capacity, including the old
// and new arrays during growth. Stream metadata and transport buffers are extra.
func (s *Session) ReceiveBufferBytes() uint64 { return s.receiveBufferBytes.Load() }

func (s *Session) reserveReceiveBuffer(n int) bool {
	for {
		used := s.receiveBufferBytes.Load()
		limit := s.config.MaxSessionBufferSize
		if limit != 0 && (used > limit || uint64(n) > limit-used) {
			return false
		}
		if s.receiveBufferBytes.CompareAndSwap(used, used+uint64(n)) {
			return true
		}
	}
}

func (s *Session) releaseReceiveBuffer(n int) {
	if n != 0 {
		s.receiveBufferBytes.Add(^uint64(n - 1))
	}
}

// receiveBuffer is protected by its stream's recvLock. It only allocates after
// reserving capacity. Unlike bytes.Buffer.ReadFrom it never grows speculatively
// beyond the announced frame length, and empty buffers retain no backing array.
type receiveBuffer struct {
	session *Session
	data    []byte
	off     int
}

func (b *receiveBuffer) Len() int { return len(b.data) - b.off }
func (b *receiveBuffer) Cap() int { return cap(b.data) }

func (b *receiveBuffer) reset() {
	capacity := cap(b.data)
	b.data, b.off = nil, 0
	b.session.releaseReceiveBuffer(capacity)
}

func (b *receiveBuffer) Read(p []byte) (int, error) {
	n := copy(p, b.data[b.off:])
	b.off += n
	if b.off == len(b.data) {
		b.reset()
	}
	return n, nil
}

func (b *receiveBuffer) readFrom(r io.Reader, length int) (int, error) {
	if length > cap(b.data)-len(b.data) {
		needed := b.Len() + length
		if needed <= cap(b.data) {
			copy(b.data, b.data[b.off:])
			b.data = b.data[:b.Len()]
			b.off = 0
		} else {
			capacity := needed
			if doubled := 2 * cap(b.data); doubled > capacity && doubled <= int(b.session.config.MaxStreamWindowSize) {
				capacity = doubled
			}
			if !b.session.reserveReceiveBuffer(capacity) {
				return 0, ErrSessionBufferExceeded
			}
			next := make([]byte, b.Len(), capacity)
			copy(next, b.data[b.off:])
			previous := cap(b.data)
			b.data, b.off = next, 0
			b.session.releaseReceiveBuffer(previous)
		}
	}
	end := len(b.data)
	n, err := io.ReadFull(r, b.data[end:end+length])
	b.data = b.data[:end+n]
	return n, err
}

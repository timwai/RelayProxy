package resume

import "sync"

// StreamState combines one outbound replay buffer with one inbound receiver.
// It is transport-independent: a P2P QUIC stream and a Relay stream can bind
// to the same state at different generations without resetting byte offsets.
type StreamState struct {
	identity Identity
	send     *ReplayBuffer
	receive  *Receiver

	sendMu sync.Mutex
	recvMu sync.Mutex

	localFIN    bool
	localFINAck bool
	remoteFIN   bool
}

func NewStreamState(identity Identity, replayLimit int) (*StreamState, error) {
	if !identity.Valid() {
		return nil, ErrBinding
	}
	return &StreamState{
		identity: identity,
		send:     NewReplayBuffer(replayLimit),
		receive:  &Receiver{},
	}, nil
}

func NewRandomStreamState(replayLimit int) (*StreamState, error) {
	identity, err := NewIdentity()
	if err != nil {
		return nil, err
	}
	return NewStreamState(identity, replayLimit)
}

func (s *StreamState) Identity() Identity {
	if s == nil {
		return Identity{}
	}
	return s.identity
}

func (s *StreamState) receiveState() (ack uint64, flags uint16) {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	ack = s.receive.expected
	if s.remoteFIN {
		flags |= FlagFINAck
	}
	return ack, flags
}

// Data retains payload before returning the frame. The caller must not expose
// bytes to the transport if this returns an error, otherwise replay safety
// would be lost.
func (s *StreamState) Data(payload []byte) (Frame, error) {
	frame, err := s.dataRetained(payload)
	if err != nil {
		return Frame{}, err
	}
	// Data is a public state API, so keep the returned frame independent from
	// replay storage. Endpoint.Write uses dataRetained directly and never
	// exposes the shared immutable payload to its caller.
	frame.Payload = append([]byte(nil), frame.Payload...)
	frame.wire = nil
	return frame, nil
}

func (s *StreamState) dataRetained(payload []byte) (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.localFIN {
		return Frame{}, ErrFrame
	}
	seq, retained, wire, err := s.send.appendRetainedLocked(payload)
	if err != nil {
		return Frame{}, err
	}
	ack, flags := s.receiveState()
	return Frame{
		Type:    FrameData,
		Flags:   flags,
		Seq:     seq,
		Ack:     ack,
		Payload: retained,
		wire:    wire,
	}, nil
}

// FIN records a local half-close. It is replayed after transport loss until
// the peer explicitly acknowledges receipt with FlagFINAck.
func (s *StreamState) FIN() (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.localFIN = true
	ack, flags := s.receiveState()
	return Frame{Type: FrameFIN, Flags: flags, Seq: s.send.next, Ack: ack}, nil
}

func (s *StreamState) AckFrame() (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	ack, flags := s.receiveState()
	return Frame{Type: FrameAck, Flags: flags, Ack: ack}, nil
}

// RST terminates the logical stream immediately. Unlike transport loss, a
// logical reset must not leave the Exit target socket retained for rebind.
func (s *StreamState) RST() (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	ack, flags := s.receiveState()
	return Frame{Type: FrameRST, Flags: flags, Seq: s.send.next, Ack: ack}, nil
}

// Handle applies a peer frame to the logical stream. ACK offsets are validated
// before receive state is mutated, so an impossible acknowledgement cannot
// advance one half of the state and then fail the other half.
func (s *StreamState) Handle(frame Frame) ([]byte, uint64, error) {
	fresh, ack, err := s.handleBorrowed(frame)
	return append([]byte(nil), fresh...), ack, err
}

// handleBorrowed leaves fresh bytes in frame.Payload. The endpoint copies them
// into its bounded inbound buffer before the transport reader reuses storage.
func (s *StreamState) handleBorrowed(frame Frame) ([]byte, uint64, error) {
	if s == nil || frame.Flags & ^knownFlags != 0 {
		return nil, 0, ErrFrame
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if frame.Ack > s.send.next {
		return nil, s.currentReceiveOffset(), ErrFrame
	}
	if frame.Flags&FlagFINAck != 0 {
		if !s.localFIN || frame.Ack != s.send.next {
			return nil, s.currentReceiveOffset(), ErrFrame
		}
	}
	if err := s.send.ackLocked(frame.Ack); err != nil {
		return nil, s.currentReceiveOffset(), err
	}
	if frame.Flags&FlagFINAck != 0 {
		s.localFINAck = true
	}

	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	switch frame.Type {
	case FrameData:
		if s.remoteFIN {
			expected := s.receive.expected
			if uint64(len(frame.Payload)) > ^uint64(0)-frame.Seq {
				return nil, expected, ErrFrame
			}
			// After FIN, only a fully duplicate replay from before the FIN is
			// valid. New bytes would violate TCP half-close ordering.
			if frame.Seq+uint64(len(frame.Payload)) > expected {
				return nil, expected, ErrFrame
			}
		}
		fresh, receiveAck, err := s.receive.acceptBorrowedLocked(frame.Seq, frame.Payload)
		if err != nil {
			return nil, receiveAck, err
		}
		return fresh, receiveAck, nil
	case FrameFIN:
		expected := s.receive.expected
		if frame.Seq > expected {
			return nil, expected, ErrSequenceGap
		}
		if frame.Seq < expected {
			return nil, expected, ErrFrame
		}
		s.remoteFIN = true
		return nil, expected, nil
	case FrameAck:
		if len(frame.Payload) != 0 {
			return nil, s.receive.expected, ErrFrame
		}
		return nil, s.receive.expected, nil
	case FrameRST:
		if len(frame.Payload) != 0 {
			return nil, s.receive.expected, ErrFrame
		}
		return nil, s.receive.expected, nil
	default:
		return nil, s.receive.expected, ErrFrame
	}
}

func (s *StreamState) currentReceiveOffset() uint64 {
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	return s.receive.expected
}

// ReplayFrames snapshots all bytes not yet acknowledged by the peer. Every
// replay carries the newest receive ACK, allowing either direction to recover
// progress immediately after a transport rebind. An unacknowledged FIN is
// appended after replay data at the final byte offset.
func (s *StreamState) ReplayFrames() []Frame {
	if s == nil {
		return nil
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	frames := s.send.snapshotLocked()
	ack, flags := s.receiveState()
	for i := range frames {
		frames[i].Ack = ack
		frames[i].Flags = flags
	}
	if s.localFIN && !s.localFINAck {
		frames = append(frames, Frame{Type: FrameFIN, Flags: flags, Seq: s.send.next, Ack: ack})
	}
	return frames
}

func (s *StreamState) Binding(kind BindType, generation uint64) (Binding, error) {
	if s == nil || generation == 0 {
		return Binding{}, ErrBinding
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	receiveOffset := s.currentReceiveOffset()
	return Binding{
		Type:          kind,
		Identity:      s.identity,
		Generation:    generation,
		SendOffset:    s.send.next,
		ReceiveOffset: receiveOffset,
	}, nil
}

func (s *StreamState) BufferedReplayBytes() int {
	if s == nil {
		return 0
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.send.buffered
}

func (s *StreamState) LocalFINAcknowledged() bool {
	if s == nil {
		return false
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.localFINAck
}

func (s *StreamState) RemoteFIN() bool {
	if s == nil {
		return false
	}
	s.recvMu.Lock()
	defer s.recvMu.Unlock()
	return s.remoteFIN
}

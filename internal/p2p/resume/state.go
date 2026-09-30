package resume

// StreamState combines one outbound replay buffer with one inbound receiver.
// It is transport-independent: a P2P QUIC stream and a Relay stream can bind
// to the same state at different generations without resetting byte offsets.
type StreamState struct {
	identity Identity
	send     *ReplayBuffer
	receive  *Receiver
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

// Data retains payload before returning the frame. The caller must not expose
// bytes to the transport if this returns an error, otherwise replay safety
// would be lost.
func (s *StreamState) Data(payload []byte) (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	seq, err := s.send.Append(payload)
	if err != nil {
		return Frame{}, err
	}
	return Frame{
		Type:    FrameData,
		Seq:     seq,
		Ack:     s.receive.Expected(),
		Payload: append([]byte(nil), payload...),
	}, nil
}

func (s *StreamState) AckFrame() (Frame, error) {
	if s == nil {
		return Frame{}, ErrFrame
	}
	return Frame{Type: FrameAck, Ack: s.receive.Expected()}, nil
}

// Handle applies a peer frame to the logical stream. ACK offsets are validated
// before receive state is mutated, so an impossible acknowledgement cannot
// advance one half of the state and then fail the other half.
func (s *StreamState) Handle(frame Frame) ([]byte, uint64, error) {
	if s == nil {
		return nil, 0, ErrFrame
	}
	if frame.Ack > s.send.Next() {
		return nil, s.receive.Expected(), ErrFrame
	}
	switch frame.Type {
	case FrameData:
		fresh, receiveAck, err := s.receive.Accept(frame.Seq, frame.Payload)
		if err != nil {
			return nil, receiveAck, err
		}
		if err := s.send.Ack(frame.Ack); err != nil {
			return nil, receiveAck, err
		}
		return fresh, receiveAck, nil
	case FrameAck, FrameFIN, FrameRST:
		if len(frame.Payload) != 0 {
			return nil, s.receive.Expected(), ErrFrame
		}
		if err := s.send.Ack(frame.Ack); err != nil {
			return nil, s.receive.Expected(), err
		}
		return nil, s.receive.Expected(), nil
	default:
		return nil, s.receive.Expected(), ErrFrame
	}
}

// ReplayFrames snapshots all bytes not yet acknowledged by the peer. Every
// replay carries the newest receive ACK, allowing either direction to recover
// progress immediately after a transport rebind.
func (s *StreamState) ReplayFrames() []Frame {
	if s == nil {
		return nil
	}
	frames := s.send.Snapshot()
	ack := s.receive.Expected()
	for i := range frames {
		frames[i].Ack = ack
	}
	return frames
}

func (s *StreamState) Binding(kind BindType, generation uint64) (Binding, error) {
	if s == nil || generation == 0 {
		return Binding{}, ErrBinding
	}
	return Binding{
		Type:          kind,
		Identity:      s.identity,
		Generation:    generation,
		SendOffset:    s.send.Next(),
		ReceiveOffset: s.receive.Expected(),
	}, nil
}

func (s *StreamState) BufferedReplayBytes() int {
	if s == nil {
		return 0
	}
	return s.send.Buffered()
}

package desktop

import (
	"bytes"
	"fmt"
	"sync"
	"time"
)

type EncodedFrame struct {
	SessionID  uint64
	StreamID   uint16
	Generation uint32
	FrameID    uint32
	Timestamp  uint64
	KeyFrame   bool
	Config     bool
	Data       []byte
}

func PacketizeFrame(frame EncodedFrame, maxPacketSize int, firstSequence uint32) ([][]byte, uint32, error) {
	if frame.SessionID == 0 || frame.StreamID == 0 || frame.Generation == 0 {
		return nil, firstSequence, ErrMediaPacket
	}
	if len(frame.Data) == 0 || len(frame.Data) > MaxEncodedFrameSize {
		return nil, firstSequence, fmt.Errorf("%w: frame size %d", ErrMediaPacket, len(frame.Data))
	}
	if maxPacketSize <= MediaHeaderSize {
		return nil, firstSequence, fmt.Errorf("%w: packet size %d is too small", ErrMediaPacket, maxPacketSize)
	}
	payloadSize := maxPacketSize - MediaHeaderSize
	fragmentCount := (len(frame.Data) + payloadSize - 1) / payloadSize
	if fragmentCount > MaxMediaFragments {
		return nil, firstSequence, fmt.Errorf("%w: frame requires %d fragments", ErrMediaPacket, fragmentCount)
	}

	packets := make([][]byte, 0, fragmentCount)
	sequence := firstSequence
	for index, offset := 0, 0; offset < len(frame.Data); index, offset = index+1, offset+payloadSize {
		end := offset + payloadSize
		if end > len(frame.Data) {
			end = len(frame.Data)
		}
		flags := MediaFlags(0)
		if frame.KeyFrame {
			flags |= MediaFlagKeyFrame
		}
		if frame.Config {
			flags |= MediaFlagConfig
		}
		if index == fragmentCount-1 {
			flags |= MediaFlagEndOfFrame
		}
		packet, err := EncodeMediaPacket(MediaHeader{
			Version:       MediaProtocolVersion,
			Type:          MediaPacketVideo,
			Flags:         flags,
			SessionID:     frame.SessionID,
			StreamID:      frame.StreamID,
			Generation:    frame.Generation,
			Sequence:      sequence,
			FrameID:       frame.FrameID,
			FragmentIndex: uint16(index),
			FragmentCount: uint16(fragmentCount),
			Timestamp:     frame.Timestamp,
		}, frame.Data[offset:end])
		if err != nil {
			return nil, firstSequence, err
		}
		packets = append(packets, packet)
		sequence++
	}
	return packets, sequence, nil
}

type ReassemblerConfig struct {
	MaxFrames int
	MaxBytes  int
	FrameTTL  time.Duration
}

type frameKey struct {
	sessionID  uint64
	streamID   uint16
	generation uint32
	frameID    uint32
}

type pendingFrame struct {
	header    MediaHeader
	createdAt time.Time
	parts     [][]byte
	received  int
	bytes     int
}

type Reassembler struct {
	mu     sync.Mutex
	cfg    ReassemblerConfig
	frames map[frameKey]*pendingFrame
	bytes  int
}

func NewReassembler(cfg ReassemblerConfig) *Reassembler {
	if cfg.MaxFrames <= 0 {
		cfg.MaxFrames = 32
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = MaxEncodedFrameSize
	}
	if cfg.MaxBytes > MaxEncodedFrameSize*2 {
		cfg.MaxBytes = MaxEncodedFrameSize * 2
	}
	if cfg.FrameTTL <= 0 {
		cfg.FrameTTL = 750 * time.Millisecond
	}
	return &Reassembler{cfg: cfg, frames: make(map[frameKey]*pendingFrame)}
}

func (r *Reassembler) Push(packet []byte, now time.Time) (*EncodedFrame, error) {
	header, payload, err := DecodeMediaPacket(packet)
	if err != nil {
		return nil, err
	}
	if header.Type != MediaPacketVideo {
		return nil, ErrMediaPacket
	}
	if now.IsZero() {
		now = time.Now()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.evictExpiredLocked(now)

	key := frameKey{
		sessionID:  header.SessionID,
		streamID:   header.StreamID,
		generation: header.Generation,
		frameID:    header.FrameID,
	}
	pending := r.frames[key]
	if pending == nil {
		for len(r.frames) >= r.cfg.MaxFrames {
			if !r.evictOldestLocked() {
				return nil, ErrMediaCapacity
			}
		}
		pending = &pendingFrame{
			header:    header,
			createdAt: now,
			parts:     make([][]byte, int(header.FragmentCount)),
		}
		r.frames[key] = pending
	} else if pending.header.FragmentCount != header.FragmentCount ||
		pending.header.Timestamp != header.Timestamp ||
		(pending.header.Flags&(MediaFlagKeyFrame|MediaFlagConfig)) != (header.Flags&(MediaFlagKeyFrame|MediaFlagConfig)) {
		r.dropLocked(key)
		return nil, ErrMediaPacket
	}

	slot := int(header.FragmentIndex)
	if pending.parts[slot] != nil {
		if !bytes.Equal(pending.parts[slot], payload) {
			r.dropLocked(key)
			return nil, ErrMediaPacket
		}
		return nil, nil
	}
	if pending.bytes+len(payload) > MaxEncodedFrameSize || len(payload) > r.cfg.MaxBytes {
		r.dropLocked(key)
		return nil, ErrMediaCapacity
	}
	for r.bytes+len(payload) > r.cfg.MaxBytes {
		if !r.evictOldestExceptLocked(key) {
			r.dropLocked(key)
			return nil, ErrMediaCapacity
		}
		pending = r.frames[key]
		if pending == nil {
			return nil, ErrMediaCapacity
		}
	}

	pending.parts[slot] = append([]byte(nil), payload...)
	pending.received++
	pending.bytes += len(payload)
	r.bytes += len(payload)
	if pending.received != len(pending.parts) {
		return nil, nil
	}

	data := make([]byte, 0, pending.bytes)
	for _, part := range pending.parts {
		if part == nil {
			return nil, nil
		}
		data = append(data, part...)
	}
	flags := pending.header.Flags
	frame := &EncodedFrame{
		SessionID:  header.SessionID,
		StreamID:   header.StreamID,
		Generation: header.Generation,
		FrameID:    header.FrameID,
		Timestamp:  header.Timestamp,
		KeyFrame:   flags&MediaFlagKeyFrame != 0,
		Config:     flags&MediaFlagConfig != 0,
		Data:       data,
	}
	r.dropLocked(key)
	return frame, nil
}

func (r *Reassembler) evictExpiredLocked(now time.Time) {
	for key, pending := range r.frames {
		if now.Sub(pending.createdAt) >= r.cfg.FrameTTL {
			r.dropLocked(key)
		}
	}
}

func (r *Reassembler) evictOldestLocked() bool {
	var oldest frameKey
	var oldestTime time.Time
	found := false
	for key, pending := range r.frames {
		if !found || pending.createdAt.Before(oldestTime) {
			oldest, oldestTime, found = key, pending.createdAt, true
		}
	}
	if found {
		r.dropLocked(oldest)
	}
	return found
}

func (r *Reassembler) evictOldestExceptLocked(except frameKey) bool {
	var oldest frameKey
	var oldestTime time.Time
	found := false
	for key, pending := range r.frames {
		if key == except {
			continue
		}
		if !found || pending.createdAt.Before(oldestTime) {
			oldest, oldestTime, found = key, pending.createdAt, true
		}
	}
	if found {
		r.dropLocked(oldest)
	}
	return found
}

func (r *Reassembler) dropLocked(key frameKey) {
	pending := r.frames[key]
	if pending == nil {
		return
	}
	r.bytes -= pending.bytes
	if r.bytes < 0 {
		r.bytes = 0
	}
	delete(r.frames, key)
}

func (r *Reassembler) Reset() {
	r.mu.Lock()
	r.frames = make(map[frameKey]*pendingFrame)
	r.bytes = 0
	r.mu.Unlock()
}

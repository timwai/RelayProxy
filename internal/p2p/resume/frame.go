// Package resume contains the transport-independent framing primitives used by
// resumable proxy streams. The capability is intentionally not advertised until
// both Client and Exit can rebind an established logical TCP stream.
package resume

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Magic      uint32 = 0x52505352 // "RPSR"
	Version    byte   = 1
	HeaderSize        = 24

	MaxPayload = 32 << 10
)

type FrameType byte

const (
	FrameData FrameType = 1
	FrameAck  FrameType = 2
	FrameFIN  FrameType = 3
	FrameRST  FrameType = 4
)

var (
	ErrFrame       = errors.New("invalid resumable stream frame")
	ErrPayloadSize = errors.New("resumable stream payload is too large")
	ErrSequenceGap = errors.New("resumable stream sequence gap")
)

// Frame uses byte offsets instead of packet numbers so a partially
// acknowledged frame can be replayed without changing the logical stream.
type Frame struct {
	Type    FrameType
	Seq     uint64
	Ack     uint64
	Payload []byte
}

func WriteFrame(w io.Writer, frame Frame) error {
	if w == nil {
		return fmt.Errorf("%w: nil writer", ErrFrame)
	}
	if len(frame.Payload) > MaxPayload {
		return ErrPayloadSize
	}
	switch frame.Type {
	case FrameData:
		if len(frame.Payload) == 0 {
			return fmt.Errorf("%w: empty data frame", ErrFrame)
		}
	case FrameAck, FrameFIN, FrameRST:
		if len(frame.Payload) != 0 {
			return fmt.Errorf("%w: control frame payload", ErrFrame)
		}
	default:
		return fmt.Errorf("%w: type %d", ErrFrame, frame.Type)
	}
	var header [HeaderSize]byte
	binary.BigEndian.PutUint32(header[0:4], Magic)
	header[4] = Version
	header[5] = byte(frame.Type)
	binary.BigEndian.PutUint64(header[8:16], frame.Seq)
	binary.BigEndian.PutUint32(header[16:20], uint32(len(frame.Payload)))
	binary.BigEndian.PutUint32(header[20:24], uint32(frame.Ack))
	if frame.Ack > uint64(^uint32(0)) {
		return fmt.Errorf("%w: ack offset exceeds v1 range", ErrFrame)
	}
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(frame.Payload) == 0 {
		return nil
	}
	_, err := w.Write(frame.Payload)
	return err
}

func ReadFrame(r io.Reader) (Frame, error) {
	if r == nil {
		return Frame{}, fmt.Errorf("%w: nil reader", ErrFrame)
	}
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	if binary.BigEndian.Uint32(header[0:4]) != Magic || header[4] != Version {
		return Frame{}, ErrFrame
	}
	frame := Frame{
		Type: FrameType(header[5]),
		Seq:  binary.BigEndian.Uint64(header[8:16]),
		Ack:  uint64(binary.BigEndian.Uint32(header[20:24])),
	}
	n := binary.BigEndian.Uint32(header[16:20])
	if n > MaxPayload {
		return Frame{}, ErrPayloadSize
	}
	switch frame.Type {
	case FrameData:
		if n == 0 {
			return Frame{}, ErrFrame
		}
	case FrameAck, FrameFIN, FrameRST:
		if n != 0 {
			return Frame{}, ErrFrame
		}
	default:
		return Frame{}, ErrFrame
	}
	if n > 0 {
		frame.Payload = make([]byte, int(n))
		if _, err := io.ReadFull(r, frame.Payload); err != nil {
			return Frame{}, err
		}
	}
	return frame, nil
}

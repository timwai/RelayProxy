// Package resume contains the transport-independent framing primitives used by
// resumable proxy streams. Activation is capability-gated per authenticated
// Client/Exit pair and per concrete P2P QUIC session.
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
	HeaderSize        = 32

	MaxPayload = 32 << 10
)

type FrameType byte

const (
	FrameData FrameType = 1
	FrameAck  FrameType = 2
	FrameFIN  FrameType = 3
	FrameRST  FrameType = 4

	// FlagFINAck acknowledges that the peer's FIN at Ack was accepted.
	FlagFINAck uint16 = 1 << 0
	knownFlags        = FlagFINAck
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
	Flags   uint16
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
	if frame.Flags & ^knownFlags != 0 {
		return fmt.Errorf("%w: flags %#x", ErrFrame, frame.Flags)
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
	binary.BigEndian.PutUint16(header[6:8], frame.Flags)
	binary.BigEndian.PutUint64(header[8:16], frame.Seq)
	binary.BigEndian.PutUint64(header[16:24], frame.Ack)
	binary.BigEndian.PutUint32(header[24:28], uint32(len(frame.Payload)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, frame.Payload)
}

func ReadFrame(r io.Reader) (Frame, error) {
	if r == nil {
		return Frame{}, fmt.Errorf("%w: nil reader", ErrFrame)
	}
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	if binary.BigEndian.Uint32(header[0:4]) != Magic || header[4] != Version ||
		header[28] != 0 || header[29] != 0 || header[30] != 0 || header[31] != 0 {
		return Frame{}, ErrFrame
	}
	frame := Frame{
		Type:  FrameType(header[5]),
		Flags: binary.BigEndian.Uint16(header[6:8]),
		Seq:   binary.BigEndian.Uint64(header[8:16]),
		Ack:   binary.BigEndian.Uint64(header[16:24]),
	}
	if frame.Flags & ^knownFlags != 0 {
		return Frame{}, ErrFrame
	}
	n := binary.BigEndian.Uint32(header[24:28])
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

func writeAll(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

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
	wire    []byte // optional HeaderSize-byte prefix followed by Payload
}

func WriteFrame(w io.Writer, frame Frame) error {
	var header [HeaderSize]byte
	return writeFrame(w, frame, header[:])
}

// frameWriter reuses one header buffer. It must be serialized by its owner;
// Endpoint does so with writeMu for data, replay and control frames alike.
type frameWriter struct {
	header [HeaderSize]byte
}

func (writer *frameWriter) Write(w io.Writer, frame Frame) error {
	return writeFrame(w, frame, writer.header[:])
}

func writeFrame(w io.Writer, frame Frame, header []byte) error {
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
	combined := len(frame.Payload) > 0 && len(frame.wire) == HeaderSize+len(frame.Payload)
	if combined {
		header = frame.wire[:HeaderSize]
	}
	binary.BigEndian.PutUint32(header[0:4], Magic)
	header[4] = Version
	header[5] = byte(frame.Type)
	binary.BigEndian.PutUint16(header[6:8], frame.Flags)
	binary.BigEndian.PutUint64(header[8:16], frame.Seq)
	binary.BigEndian.PutUint64(header[16:24], frame.Ack)
	binary.BigEndian.PutUint32(header[24:28], uint32(len(frame.Payload)))
	if combined {
		return writeAll(w, frame.wire)
	}
	if err := writeAll(w, header); err != nil {
		return err
	}
	return writeAll(w, frame.Payload)
}

func ReadFrame(r io.Reader) (Frame, error) {
	var header [HeaderSize]byte
	return readFrame(r, header[:], nil)
}

// frameReader is owned by a single transport read loop. Payloads are valid only
// until the next Read; callers must consume or copy them before reading again.
// The public ReadFrame never reuses storage, keeping its payload caller-owned.
type frameReader struct {
	header  [HeaderSize]byte
	payload []byte
}

func (reader *frameReader) Read(r io.Reader) (Frame, error) {
	frame, err := readFrame(r, reader.header[:], reader.payload)
	if err == nil && len(frame.Payload) > 0 {
		reader.payload = frame.Payload
	}
	return frame, err
}

func readFrame(r io.Reader, header, payload []byte) (Frame, error) {
	if r == nil {
		return Frame{}, fmt.Errorf("%w: nil reader", ErrFrame)
	}
	if _, err := io.ReadFull(r, header); err != nil {
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
		if cap(payload) < int(n) {
			payload = make([]byte, int(n))
		}
		frame.Payload = payload[:int(n)]
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

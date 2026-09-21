package desktop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	MediaProtocolVersion uint8 = 1
	MediaHeaderSize            = 40

	MaxMediaFragments   = 4096
	MaxEncodedFrameSize = 32 << 20
)

type MediaPacketType uint8

const (
	MediaPacketVideo MediaPacketType = 1
	MediaPacketAudio MediaPacketType = 2
	MediaPacketCursor MediaPacketType = 3
)

type MediaFlags uint16

const (
	MediaFlagKeyFrame MediaFlags = 1 << iota
	MediaFlagConfig
	MediaFlagEndOfFrame
)

var (
	ErrMediaPacket   = errors.New("invalid remote desktop media packet")
	ErrMediaCapacity = errors.New("remote desktop media capacity exceeded")
)

type MediaHeader struct {
	Version       uint8
	Type          MediaPacketType
	Flags         MediaFlags
	SessionID     uint64
	StreamID      uint16
	Generation    uint32
	Sequence      uint32
	FrameID       uint32
	FragmentIndex uint16
	FragmentCount uint16
	Timestamp     uint64
	PayloadLength uint16
}

func validMediaType(value MediaPacketType) bool {
	switch value {
	case MediaPacketVideo, MediaPacketAudio, MediaPacketCursor:
		return true
	default:
		return false
	}
}

func EncodeMediaPacket(header MediaHeader, payload []byte) ([]byte, error) {
	if header.Version == 0 {
		header.Version = MediaProtocolVersion
	}
	if header.Version != MediaProtocolVersion || !validMediaType(header.Type) || header.SessionID == 0 || header.StreamID == 0 {
		return nil, ErrMediaPacket
	}
	if header.FragmentCount == 0 || header.FragmentCount > MaxMediaFragments || header.FragmentIndex >= header.FragmentCount {
		return nil, ErrMediaPacket
	}
	if len(payload) > math.MaxUint16 {
		return nil, fmt.Errorf("%w: payload is too large", ErrMediaPacket)
	}
	header.PayloadLength = uint16(len(payload))
	packet := make([]byte, MediaHeaderSize+len(payload))
	packet[0] = header.Version
	packet[1] = byte(header.Type)
	binary.BigEndian.PutUint16(packet[2:4], uint16(header.Flags))
	binary.BigEndian.PutUint64(packet[4:12], header.SessionID)
	binary.BigEndian.PutUint16(packet[12:14], header.StreamID)
	binary.BigEndian.PutUint32(packet[14:18], header.Generation)
	binary.BigEndian.PutUint32(packet[18:22], header.Sequence)
	binary.BigEndian.PutUint32(packet[22:26], header.FrameID)
	binary.BigEndian.PutUint16(packet[26:28], header.FragmentIndex)
	binary.BigEndian.PutUint16(packet[28:30], header.FragmentCount)
	binary.BigEndian.PutUint64(packet[30:38], header.Timestamp)
	binary.BigEndian.PutUint16(packet[38:40], header.PayloadLength)
	copy(packet[MediaHeaderSize:], payload)
	return packet, nil
}

func DecodeMediaPacket(packet []byte) (MediaHeader, []byte, error) {
	if len(packet) < MediaHeaderSize {
		return MediaHeader{}, nil, ErrMediaPacket
	}
	header := MediaHeader{
		Version:       packet[0],
		Type:          MediaPacketType(packet[1]),
		Flags:         MediaFlags(binary.BigEndian.Uint16(packet[2:4])),
		SessionID:     binary.BigEndian.Uint64(packet[4:12]),
		StreamID:      binary.BigEndian.Uint16(packet[12:14]),
		Generation:    binary.BigEndian.Uint32(packet[14:18]),
		Sequence:      binary.BigEndian.Uint32(packet[18:22]),
		FrameID:       binary.BigEndian.Uint32(packet[22:26]),
		FragmentIndex: binary.BigEndian.Uint16(packet[26:28]),
		FragmentCount: binary.BigEndian.Uint16(packet[28:30]),
		Timestamp:     binary.BigEndian.Uint64(packet[30:38]),
		PayloadLength: binary.BigEndian.Uint16(packet[38:40]),
	}
	if header.Version != MediaProtocolVersion || !validMediaType(header.Type) || header.SessionID == 0 || header.StreamID == 0 {
		return MediaHeader{}, nil, ErrMediaPacket
	}
	if header.FragmentCount == 0 || header.FragmentCount > MaxMediaFragments || header.FragmentIndex >= header.FragmentCount {
		return MediaHeader{}, nil, ErrMediaPacket
	}
	payload := packet[MediaHeaderSize:]
	if len(payload) != int(header.PayloadLength) {
		return MediaHeader{}, nil, ErrMediaPacket
	}
	return header, payload, nil
}

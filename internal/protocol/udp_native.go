package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	UDPFragmentPayload    = 1100
	UDPFragmentHeaderSize = 8
	MaxUDPFragments       = 64
)

var ErrUDPFragment = errors.New("invalid UDP fragment")

// UDPFragment belongs to one authenticated association; association IDs are
// carried by the transport envelope, never used as standalone credentials.
type UDPFragment struct {
	PacketID     uint32
	Total        uint16
	Index, Count uint8
	Payload      []byte
}

func FragmentUDP(packetID uint32, payload []byte, send func([]byte) error) error {
	if len(payload) > MaxUDPDatagramPayload {
		return ErrUDPFragment
	}
	count := max(1, (len(payload)+UDPFragmentPayload-1)/UDPFragmentPayload)
	for i := 0; i < count; i++ {
		part := payload[min(i*UDPFragmentPayload, len(payload)):min((i+1)*UDPFragmentPayload, len(payload))]
		frame := make([]byte, UDPFragmentHeaderSize+len(part))
		binary.BigEndian.PutUint32(frame[:4], packetID)
		binary.BigEndian.PutUint16(frame[4:6], uint16(len(payload)))
		frame[6], frame[7] = byte(i), byte(count)
		copy(frame[8:], part)
		if err := send(frame); err != nil {
			return err
		}
	}
	return nil
}

func DecodeUDPFragment(frame []byte) (UDPFragment, error) {
	if len(frame) < UDPFragmentHeaderSize {
		return UDPFragment{}, ErrUDPFragment
	}
	f := UDPFragment{PacketID: binary.BigEndian.Uint32(frame[:4]), Total: binary.BigEndian.Uint16(frame[4:6]), Index: frame[6], Count: frame[7], Payload: frame[8:]}
	if int(f.Total) > MaxUDPDatagramPayload {
		return UDPFragment{}, ErrUDPFragment
	}
	wantCount := max(1, (int(f.Total)+UDPFragmentPayload-1)/UDPFragmentPayload)
	if f.Count == 0 || int(f.Count) != wantCount || f.Count > MaxUDPFragments || f.Index >= f.Count {
		return UDPFragment{}, ErrUDPFragment
	}
	wantLen := min(UDPFragmentPayload, int(f.Total)-int(f.Index)*UDPFragmentPayload)
	if len(f.Payload) != wantLen {
		return UDPFragment{}, ErrUDPFragment
	}
	return f, nil
}

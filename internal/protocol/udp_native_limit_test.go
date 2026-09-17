package protocol

import (
	"encoding/binary"
	"testing"
)

func TestDecodeRejectsOversizedUDPDatagram(t *testing.T) {
	frame := make([]byte, UDPFragmentHeaderSize+UDPFragmentPayload)
	binary.BigEndian.PutUint16(frame[4:6], 65535)
	frame[7] = byte((65535 + UDPFragmentPayload - 1) / UDPFragmentPayload)
	if _, err := DecodeUDPFragment(frame); err == nil {
		t.Fatal("decoder accepted more than the agreed UDP payload limit")
	}
}

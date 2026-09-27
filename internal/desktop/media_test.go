package desktop

import (
	"errors"
	"testing"
)

func TestMediaPacketRoundTrip(t *testing.T) {
	header := MediaHeader{
		Version:       MediaProtocolVersion,
		Type:          MediaPacketVideo,
		Flags:         MediaFlagKeyFrame | MediaFlagEndOfFrame,
		SessionID:     42,
		StreamID:      1,
		Generation:    3,
		Sequence:      99,
		FrameID:       7,
		FragmentIndex: 0,
		FragmentCount: 1,
		Timestamp:     123456,
	}
	payload := []byte("frame")
	packet, err := EncodeMediaPacket(header, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet) != MediaHeaderSize+len(payload) {
		t.Fatalf("packet size = %d", len(packet))
	}
	got, gotPayload, err := DecodeMediaPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != header.SessionID || got.Sequence != header.Sequence || got.Flags != header.Flags {
		t.Fatalf("decoded header = %+v", got)
	}
	if string(gotPayload) != string(payload) {
		t.Fatalf("payload = %q", gotPayload)
	}
}

func TestDecodeMediaPacketRejectsLengthMismatch(t *testing.T) {
	packet, err := EncodeMediaPacket(MediaHeader{
		Type:          MediaPacketVideo,
		SessionID:     1,
		StreamID:      1,
		Generation:    1,
		FragmentCount: 1,
	}, []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	packet[39]++
	if _, _, err := DecodeMediaPacket(packet); !errors.Is(err, ErrMediaPacket) {
		t.Fatalf("error = %v, want ErrMediaPacket", err)
	}
}

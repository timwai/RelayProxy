package secure

import (
	"bytes"
	"testing"
)

func TestPunchAndDataAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	packet := PunchPacket{Type: PunchRequest, SessionID: 7, Nonce: 9}.Encode(key)
	decoded, err := DecodePunchPacket(packet, key)
	if err != nil || decoded.SessionID != 7 || decoded.Nonce != 9 {
		t.Fatalf("punch decode failed: %v %#v", err, decoded)
	}
	packet[24] ^= 1
	if _, err := DecodePunchPacket(packet, key); err != ErrBadMAC {
		t.Fatalf("tampered punch accepted: %v", err)
	}

	sender, err := NewDataCodec(7, key)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewDataCodec(7, key)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := sender.Encode([]byte("rdp"))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := receiver.Decode(wire)
	if err != nil || string(payload) != "rdp" {
		t.Fatalf("data decode failed: %v %q", err, payload)
	}
	if _, err := receiver.Decode(wire); err != ErrReplay {
		t.Fatalf("replayed data was accepted: %v", err)
	}
}

func TestDataCodecReusableBuffers(t *testing.T) {
	key := bytes.Repeat([]byte{0x17}, 32)
	sender, err := NewDataCodec(19, key)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewDataCodec(19, key)
	if err != nil {
		t.Fatal(err)
	}
	wireBuffer := bytes.Repeat([]byte{0xff}, dataHead+macSize+32)
	wire, err := sender.EncodeTo(wireBuffer[:0], []byte("reusable"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != dataHead+macSize+8 || &wire[0] != &wireBuffer[0] {
		t.Fatal("EncodeTo did not reuse the supplied buffer")
	}
	dst := make([]byte, 8)
	n, err := receiver.DecodeTo(wire, dst)
	if err != nil || n != len(dst) || string(dst) != "reusable" {
		t.Fatalf("DecodeTo failed: n=%d err=%v payload=%q", n, err, dst)
	}
	short := make([]byte, 7)
	second, err := sender.EncodeTo(wire[:0], []byte("too-long"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.DecodeTo(second, short); err != ErrShortBuffer {
		t.Fatalf("short destination error=%v", err)
	}
}

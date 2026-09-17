package protocol

import (
	"bytes"
	"testing"
)

func TestUDPDatagramRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello-udp")
	if err := WriteUDPDatagram(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadUDPDatagram(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q want %q", got, payload)
	}
}

func TestUDPDatagramRejectsTooLarge(t *testing.T) {
	big := make([]byte, 65536)
	if err := WriteUDPDatagram(ioDiscard{}, big); err == nil {
		t.Fatal("expected error for oversized UDP payload")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

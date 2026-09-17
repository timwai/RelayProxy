package divert

import (
	"bytes"
	"net/netip"
	"testing"
)

func TestDarwinIPCFrameRoundTrip(t *testing.T) {
	payload := []byte("transparent proxy")
	var wire bytes.Buffer
	if err := writeDarwinFrame(&wire, darwinFrameUDPDatagram, payload); err != nil {
		t.Fatal(err)
	}
	kind, got, err := readDarwinFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if kind != darwinFrameUDPDatagram || !bytes.Equal(got, payload) {
		t.Fatalf("frame = %d %q", kind, got)
	}
}

func TestDarwinDatagramRoundTrip(t *testing.T) {
	for _, endpoint := range []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.1:53"),
		netip.MustParseAddrPort("[2001:db8::1]:5353"),
	} {
		encoded, err := encodeDarwinDatagram(endpoint, []byte{1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		gotEndpoint, payload, err := decodeDarwinDatagram(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if gotEndpoint != endpoint || !bytes.Equal(payload, []byte{1, 2, 3}) {
			t.Fatalf("round trip = %s %v", gotEndpoint, payload)
		}
	}
}

func TestDarwinDatagramFrameRoundTrip(t *testing.T) {
	endpoint := netip.MustParseAddrPort("[2001:db8::7]:443")
	want := []byte("scatter-gather payload")
	var wire bytes.Buffer
	if err := writeDarwinDatagramFrame(&wire, darwinFrameUDPReply, endpoint, want); err != nil {
		t.Fatal(err)
	}
	kind, encoded, err := readDarwinFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	gotEndpoint, got, err := decodeDarwinDatagram(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if kind != darwinFrameUDPReply || gotEndpoint != endpoint || !bytes.Equal(got, want) {
		t.Fatalf("frame = %d %s %q", kind, gotEndpoint, got)
	}
}

func TestDarwinIPCRejectsOversizedFrameBeforeAllocation(t *testing.T) {
	wire := []byte{darwinFrameOpen, 0x7f, 0xff, 0xff, 0xff}
	if _, _, err := readDarwinFrame(bytes.NewReader(wire)); err == nil {
		t.Fatal("oversized frame was accepted")
	}
}

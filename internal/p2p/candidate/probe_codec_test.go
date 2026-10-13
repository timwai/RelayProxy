package candidate

import (
	"net/netip"
	"testing"
)

func TestRendezvousProbeCodecRoundTrip(t *testing.T) {
	const nonce = uint64(0x1122334455667788)
	request := EncodeProbeRequest(nonce)
	gotNonce, ok := DecodeProbeRequest(request[:])
	if !ok || gotNonce != nonce {
		t.Fatalf("decoded request nonce=%x, ok=%v", gotNonce, ok)
	}
	if _, ok := DecodeProbeRequest(request[:len(request)-1]); ok {
		t.Fatal("truncated rendezvous request accepted")
	}
	request[4]++
	if _, ok := DecodeProbeRequest(request[:]); ok {
		t.Fatal("unsupported rendezvous version accepted")
	}
	for _, original := range []netip.AddrPort{
		netip.MustParseAddrPort("192.0.2.10:47000"),
		netip.MustParseAddrPort("[2001:db8::10]:47000"),
	} {
		response, valid := EncodeProbeResponse(nonce, original)
		if !valid {
			t.Fatalf("cannot encode valid rendezvous IP %s", original)
		}
		observed, valid := DecodeProbeResponse(response[:], nonce)
		if !valid || observed != original {
			t.Fatalf("decoded response %s, valid=%v, want %s", observed, valid, original)
		}
		if _, ok := DecodeProbeResponse(response[:], nonce+1); ok {
			t.Fatal("different nonce accepted")
		}
		if _, ok := DecodeProbeResponse(response[:len(response)-1], nonce); ok {
			t.Fatal("truncated rendezvous response accepted")
		}
	}
}

func TestRendezvousProbeRejectsZeroAddressAndPort(t *testing.T) {
	for _, address := range []netip.AddrPort{
		netip.AddrPort{},
		netip.MustParseAddrPort("0.0.0.0:5000"),
		netip.MustParseAddrPort("192.0.2.1:0"),
	} {
		if _, valid := EncodeProbeResponse(42, address); valid {
			t.Fatalf("invalid reflexive address %s accepted", address)
		}
	}
}

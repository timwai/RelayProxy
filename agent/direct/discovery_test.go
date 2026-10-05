package direct

import (
	"net"
	"testing"

	"relayproxy/internal/protocol"
)

func TestEndpointCandidatesIncludeGlobalIPv6AndManual(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("2606:4700:4700::1111"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("fc00::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		&net.IPNet{IP: net.ParseIP("8.8.8.8"), Mask: net.CIDRMask(24, 32)},
	}
	candidates, err := endpointCandidates(35820, "exit.example.com:40000", addrs)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%+v", candidates)
	}
	if candidates[0].Source != protocol.PublicDirectEndpointIPv6 ||
		candidates[0].Address != "[2606:4700:4700::1111]:35820" {
		t.Fatalf("ipv6 candidate=%+v", candidates[0])
	}
	if candidates[1].Source != protocol.PublicDirectEndpointManual ||
		candidates[1].Address != "exit.example.com:40000" {
		t.Fatalf("manual candidate=%+v", candidates[1])
	}
}

func TestEndpointCandidatesRejectInvalidManualAddress(t *testing.T) {
	for _, value := range []string{"example.com", ":35820", "example.com:0", "example.com:99999"} {
		if _, err := endpointCandidates(35820, value, nil); err == nil {
			t.Fatalf("manual address %q was accepted", value)
		}
	}
}

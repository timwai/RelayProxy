package direct

import (
	"net"
	"net/netip"
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

func TestDirectEndpointFamilyPreflight(t *testing.T) {
	for _, address := range []string{"8.8.8.8:20800", "203.0.113.20:20800"} {
		if !localEndpointReachable(address) {
			t.Fatalf("IPv4 public direct endpoint was incorrectly rejected: %s", address)
		}
	}
	for _, address := range []string{
		"[::1]:20800", "[fe80::1]:20800", "[::]:20800", "not-a-socket-address",
	} {
		if localEndpointReachable(address) {
			t.Fatalf("unroutable public direct address was accepted: %s", address)
		}
	}
	for _, address := range []string{"::1", "fe80::1", "::"} {
		if isUsableIPv6Source(netip.MustParseAddr(address)) {
			t.Fatalf("IPv6 source is not publicly routable: %s", address)
		}
	}
	if !isUsableIPv6Source(netip.MustParseAddr("2001:4860:4860::8888")) {
		t.Fatal("global IPv6 source was incorrectly rejected")
	}
	if !localHasIPv6Source() &&
		localEndpointReachable("[2408:8266:501:6757:b251:8eff:feff:3735]:20800") {
		t.Fatal("IPv4-only host claimed an IPv6 direct route")
	}
}

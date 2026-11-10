package netpath

import (
	"net"
	"net/netip"
	"testing"

	"relayproxy/internal/protocol"
)

func TestClassifyEstablishedEndpoints(t *testing.T) {
	for _, tt := range []struct {
		name, local, remote, iface, prefix, want string
		flags                                    net.Flags
	}{
		{"LAN", "192.168.1.2", "192.168.1.3", "en0", "192.168.1.2/24", "lan", 0},
		{"global IPv6 on link", "2001:db8:1::2", "2001:db8:1::3", "en0", "2001:db8:1::2/64", "lan", 0},
		{"public remote", "192.168.1.2", "8.8.8.8", "en0", "192.168.1.2/24", "public", 0},
		{"private off link is not LAN", "192.168.1.2", "10.0.0.4", "en0", "192.168.1.2/24", "unknown", 0},
		{"CGNAT is not public", "192.168.1.2", "100.65.0.1", "en0", "192.168.1.2/24", "unknown", 0},
		{"VPN same subnet", "10.0.0.2", "10.0.0.3", "utun5", "10.0.0.2/24", "virtual", 0},
		{"VPN public route", "10.0.0.2", "8.8.8.8", "company", "10.0.0.2/32", "virtual", net.FlagPointToPoint},
		{"wrong interface", "192.168.2.2", "192.168.1.3", "en0", "192.168.1.2/24", "unknown", 0},
		{"loopback", "127.0.0.1", "127.0.0.1", "lo0", "127.0.0.1/8", "loopback", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := classify(netip.MustParseAddr(tt.local), netip.MustParseAddr(tt.remote), []interfaceAddress{{tt.iface, tt.flags, netip.MustParsePrefix(tt.prefix)}})
			if got != tt.want {
				t.Fatalf("scope=%s want %s", got, tt.want)
			}
		})
	}
}

func TestCandidateSelectionDoesNotDetermineNetworkScope(t *testing.T) {
	remote := &net.UDPAddr{IP: net.ParseIP("8.8.8.8"), Port: 1234}
	candidates := []protocol.P2PCandidate{{Type: "lan", Address: "192.168.1.5:1234"}, {Type: "reflexive", Address: "8.8.8.8:1234"}}
	if got := MatchCandidate(remote, candidates); got != "reflexive" {
		t.Fatal(got)
	}
	remote.Port++
	if got := MatchCandidate(remote, candidates); got != "peer_reflexive" {
		t.Fatal(got)
	}
	// An interface-address candidate is not proof of a LAN route.
	candidates = []protocol.P2PCandidate{{Type: "lan", Address: remote.String()}}
	if got := MatchCandidate(remote, candidates); got != "host" {
		t.Fatal(got)
	}
}

func TestDescribeUsesActualSocketEndpoints(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	p := Describe("p2p_quic", conn.LocalAddr(), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	if p.Network != "loopback" || p.Transport != "quic" || p.RemoteAddress != "127.0.0.1:1234" {
		t.Fatalf("%+v", p)
	}
	if !p.ValidFor("p2p_quic") {
		t.Fatalf("invalid telemetry: %+v", p)
	}
}

func TestRebindingClearsStaleScopeWithoutIO(t *testing.T) {
	p := protocol.ConnectionPath{Path: "p2p_quic", Network: "lan", Transport: "quic", LocalAddress: "192.168.1.2:3000", RemoteAddress: "192.168.1.3:4000", Interface: "en0", CandidateType: "host"}
	local := net.UDPAddrFromAddrPort(netip.MustParseAddrPort("0.0.0.0:3000"))
	remote := net.UDPAddrFromAddrPort(netip.MustParseAddrPort(p.RemoteAddress))
	if got := Revalidate(p, local, remote); got != p {
		t.Fatal("wildcard socket discarded known route")
	}
	remote = net.UDPAddrFromAddrPort(netip.MustParseAddrPort("8.8.8.8:5000"))
	got := Revalidate(p, local, remote)
	if got.Network != "unknown" || got.Interface != "" || got.CandidateType != "" || got.RemoteAddress != remote.String() || got.Path != p.Path {
		t.Fatalf("stale route after rebinding: %+v", got)
	}
}

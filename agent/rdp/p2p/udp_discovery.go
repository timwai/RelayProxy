package p2p

import (
	"context"
	"net"
	"net/netip"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/protocol"
)

// startUDPDiscovery publishes LAN hints immediately. Its worker only writes
// probes: the endpoint's sole reader forwards fixed-size discovery responses.
// stop joins the worker before a winning socket is handed to PacketConn.
func (s *Session) startUDPDiscovery(parent context.Context, conn *net.UDPConn) (handle func([]byte, netip.AddrPort) bool, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	packets := make(chan candidate.ProbeDatagram, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		local := candidate.DiscoverWithLimit(conn.LocalAddr().(*net.UDPAddr).Port, 0, candidate.MaxCandidates-2)
		s.publishCandidates(ctx, "udp", local)
		s.manager.mu.Lock()
		rendezvous := s.manager.rendezvous
		s.manager.mu.Unlock()
		reflexives, err := candidate.ProbeReflexiveAllWithPackets(ctx, rendezvous, conn, "udp", packets)
		if err != nil || ctx.Err() != nil {
			return
		}
		for _, item := range reflexives {
			valid, err := candidate.Validate([]protocol.P2PCandidate{item})
			if err == nil {
				local = append(local, valid...)
			}
		}
		s.publishCandidates(ctx, "udp", local)
	}()
	return func(raw []byte, remote netip.AddrPort) bool {
			if len(raw) != candidate.ProbeResponseSize {
				return false
			}
			var packet candidate.ProbeDatagram
			copy(packet.Data[:], raw)
			packet.Remote = remote
			select {
			case packets <- packet:
			default:
			}
			return true
		}, func() {
			cancel()
			<-done
		}
}

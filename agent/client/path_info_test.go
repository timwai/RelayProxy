package client

import (
	"context"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"testing"
)

type pathInfoSession struct {
	*transportSession
	info protocol.ConnectionPath
}

func (s *pathInfoSession) ConnectionPath() protocol.ConnectionPath { return s.info }

func TestConnectionDetailsFollowAcceptedSession(t *testing.T) {
	for _, udp := range []bool{false, true} {
		factory := func() tunnel.TunnelStream {
			if udp {
				return responseStream(protocol.OpenUDPResponse{Success: true, Mode: protocol.UDPModeStream})
			}
			return responseStream(protocol.OpenTCPResponse{Success: true})
		}
		relay := &transportSession{newScriptedSession(factory), tunnel.TransportTLS}
		info := protocol.ConnectionPath{Path: "p2p_quic", Network: "lan", Transport: "quic", RemoteAddress: "192.168.1.10:3000", CandidateType: "host"}
		direct := &pathInfoSession{&transportSession{newScriptedSession(factory), tunnel.TransportQUIC}, info}
		d := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
		d.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
		for _, mode := range []string{"auto", "relay_only"} {
			d.ConfigureDirectPolicy(mode, true)
			var source protocol.ConnectionPathProvider
			if udp {
				c, err := d.DialUDP(context.Background(), "exit", "8.8.4.4", 53)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				source = c.(protocol.ConnectionPathProvider)
			} else {
				c, err := d.DialTCP(context.Background(), "exit", "8.8.4.4", 443)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				source = c.(protocol.ConnectionPathProvider)
			}
			got := source.ConnectionPath()
			if mode == "auto" && got != info {
				t.Fatalf("lost selected details: %+v", got)
			}
			if mode == "relay_only" && (got.Path != "relay_tls" || got.CandidateType != "" || got.RemoteAddress == info.RemoteAddress) {
				t.Fatalf("stale direct details: %+v", got)
			}
		}
	}
}

package traffic

import (
	"net"
	"relayproxy/internal/protocol"
	"sync/atomic"
	"testing"
)

type detailedConn struct {
	partialConn
	info atomic.Value
}

func (c *detailedConn) ConnectionPath() protocol.ConnectionPath {
	return c.info.Load().(protocol.ConnectionPath)
}
func (c *detailedConn) ProxyPath() string { return c.ConnectionPath().Path }

type detailedPacket struct {
	net.PacketConn
	info protocol.ConnectionPath
}

func (c *detailedPacket) ConnectionPath() protocol.ConnectionPath { return c.info }
func (c *detailedPacket) ProxyPath() string                       { return c.info.Path }
func (*detailedPacket) Close() error                              { return nil }

func TestActivePathDetailsFollowRecoveryAndFreezeHistory(t *testing.T) {
	r := NewRegistry(8, 8)
	lan := protocol.ConnectionPath{Path: "p2p_quic", Network: "lan", Transport: "quic", CandidateType: "host"}
	raw := &detailedConn{}
	raw.info.Store(lan)
	if len(r.ActivePaths("exit")) != 0 {
		t.Fatal("invented active path")
	}
	c := WrapConn(raw, r.Start(Metadata{Action: "PROXY", ExitID: "exit"}))
	if got := r.ActivePaths("exit"); len(got) != 1 || got[0] != lan {
		t.Fatal(got)
	}
	if len(r.ActivePaths("other")) != 0 {
		t.Fatal("attributed to wrong exit")
	}
	relay := protocol.ConnectionPath{Path: "relay_tls", Network: "public", Transport: "tls"}
	raw.info.Store(relay)
	if got := r.ActivePaths("exit"); len(got) != 1 || got[0] != relay {
		t.Fatal(got)
	}
	_ = c.Close()
	raw.info.Store(lan)
	if len(r.ActivePaths("exit")) != 0 {
		t.Fatal("history advertised as active")
	}
	s := r.Snapshot()
	if s.Connections[0].PathInfo == nil || *s.Connections[0].PathInfo != relay {
		t.Fatal(s)
	}
	s.Connections[0].PathInfo.Network = "tampered"
	if r.Snapshot().Connections[0].PathInfo.Network != "public" {
		t.Fatal("snapshot mutated history")
	}
	pc := WrapPacketConn(&detailedPacket{info: lan}, r.Start(Metadata{Action: "PROXY", ExitID: "exit", Protocol: "udp"}))
	if got := r.ActivePaths("exit"); len(got) != 1 || got[0] != lan {
		t.Fatalf("UDP metadata lost: %+v", got)
	}
	_ = pc.Close()
}

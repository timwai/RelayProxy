package protocol

import "testing"

func TestConnectionPathValidation(t *testing.T) {
	good := ConnectionPath{Path: "p2p_quic", Network: "lan", Transport: "quic", Interface: "en0", RemoteAddress: "192.168.1.2:1234", CandidateType: "host"}
	if !good.ValidFor(good.Path) {
		t.Fatal("valid metadata rejected")
	}
	for _, mutate := range []func(*ConnectionPath){
		func(p *ConnectionPath) { p.Network = "guessed" },
		func(p *ConnectionPath) { p.Path = "relay_tls" },
		func(p *ConnectionPath) { p.Transport = "tls" },
		func(p *ConnectionPath) { p.Interface = "bad\ninterface" },
		func(p *ConnectionPath) { p.RemoteAddress = "example.com:1234" },
		func(p *ConnectionPath) { p.CandidateType = "arbitrary" },
	} {
		p := good
		mutate(&p)
		if p.ValidFor(good.Path) {
			t.Fatalf("accepted %+v", p)
		}
	}
}

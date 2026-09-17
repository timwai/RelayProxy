package rdp

import (
	"context"
	"net"
	"testing"
	"time"

	"relayproxy/internal/rdp/candidate"
)

func TestRendezvousReportsObservedUDPEndpoint(t *testing.T) {
	r, err := StartRendezvous(context.Background(), "127.0.0.1:0", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	item, err := candidate.ProbeReflexive(ctx, r.Addr().String(), conn, "udp")
	if err != nil {
		t.Fatal(err)
	}
	if item.Protocol != "udp" || item.Type != "reflexive" || item.Address == "" {
		t.Fatalf("unexpected reflexive candidate: %#v", item)
	}
}

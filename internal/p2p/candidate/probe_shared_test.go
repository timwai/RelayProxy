package candidate

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestSharedSocketProbeValidatesResponsesWithoutChangingDeadline(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Discovery must not clear or overwrite the sole reader's deadline.
	client.SetReadDeadline(time.Now().Add(-time.Second))
	packets := make(chan ProbeDatagram, 3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		candidates []string
		err        error
	}
	done := make(chan result, 1)
	go func() {
		items, err := ProbeReflexiveAllWithPackets(ctx, server.LocalAddr().String(), client, "udp", packets)
		addresses := make([]string, 0, len(items))
		for _, c := range items {
			addresses = append(addresses, c.Address)
		}
		done <- result{addresses, err}
	}()
	server.SetReadDeadline(time.Now().Add(time.Second))
	var buf [ProbeRequestSize]byte
	n, remote, err := server.ReadFromUDPAddrPort(buf[:])
	if err != nil {
		t.Fatal(err)
	}
	nonce, ok := DecodeProbeRequest(buf[:n])
	if !ok {
		t.Fatal("invalid probe request")
	}
	response, _ := EncodeProbeResponse(nonce, remote)
	wrongNonce, _ := EncodeProbeResponse(nonce+1, remote)
	serverAddr := server.LocalAddr().(*net.UDPAddr).AddrPort()
	packets <- ProbeDatagram{Data: response, Remote: netip.MustParseAddrPort("127.0.0.2:1234")}
	packets <- ProbeDatagram{Data: wrongNonce, Remote: serverAddr}
	packets <- ProbeDatagram{Data: response, Remote: serverAddr}
	select {
	case got := <-done:
		if got.err != nil || len(got.candidates) != 1 || got.candidates[0] != remote.String() {
			t.Fatalf("shared probe: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("shared probe failed to finish")
	}
	if _, _, err := client.ReadFromUDP(buf[:]); err == nil {
		t.Fatal("discovery cleared the reader's deadline")
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatal(err)
	}
}

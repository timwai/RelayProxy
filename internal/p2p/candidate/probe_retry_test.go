package candidate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestReflexiveProbeRecoversFromLostPackets(t *testing.T) {
	for _, lost := range []int{1, 3} {
		t.Run(fmt.Sprintf("lost_%d", lost), func(t *testing.T) {
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
			served := make(chan error, 1)
			go func() {
				_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
				var buffer [64]byte
				for i := 0; i <= lost; i++ {
					n, remote, err := server.ReadFromUDPAddrPort(buffer[:])
					if err != nil {
						served <- err
						return
					}
					if i < lost || n != ProbeRequestSize {
						continue
					}
					var response [ProbeResponseSize]byte
					binary.BigEndian.PutUint32(response[:4], ProbeMagic)
					response[4] = ProbeVersion
					binary.BigEndian.PutUint16(response[6:8], remote.Port())
					copy(response[8:16], buffer[8:16])
					ip := remote.Addr().As16()
					copy(response[16:32], ip[:])
					_, err = server.WriteToUDPAddrPort(response[:], remote)
					served <- err
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			candidates, err := ProbeReflexiveAll(ctx, server.LocalAddr().String(), client, "udp")
			if err != nil || len(candidates) != 1 {
				t.Fatalf("lost %d probes: candidates=%v err=%v", lost, candidates, err)
			}
			if err := <-served; err != nil {
				t.Fatal(err)
			}
			if candidates[0].Address != client.LocalAddr().String() {
				t.Fatalf("probe changed the mapped socket: %v", candidates)
			}
			// Cancelling discovery after success must not poison the socket
			// that punching and QUIC take over next.
			cancel()
			if _, err := server.WriteToUDP([]byte("next protocol"), client.LocalAddr().(*net.UDPAddr)); err != nil {
				t.Fatal(err)
			}
			var next [32]byte
			if _, _, err := client.ReadFromUDP(next[:]); err != nil {
				t.Fatalf("probe left a read deadline on the reused socket: %v", err)
			}
		})
	}
}

func TestReflexiveProbeCancellationIsBounded(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := ProbeReflexiveAll(ctx, server.LocalAddr().String(), client, "udp")
		done <- err
	}()
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	var buffer [64]byte
	if _, _, err := server.ReadFromUDP(buffer[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation returned %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancellation waited for the full discovery timeout")
	}
}

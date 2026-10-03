package p2p

import (
	"context"
	"net"
	"testing"
	"time"

	"relayproxy/internal/p2p/punch"
)

// Reuse a bound socket to measure the authenticated handshake independently
// of interface discovery and ephemeral-port allocation on the benchmark host.
func BenchmarkRDPUDPHandshake(b *testing.B) {
	session, _ := newDirectUDPPair(b)
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = conn.Close() })
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := punch.PunchResponder(context.Background(), conn, session.candidates, session.ID, session.Token, time.Second); err != nil {
			b.Fatal(err)
		}
	}
}

// Measure input-sized and screen-sized packets through the actual RDP target
// bridge, including authentication and fragmentation on both directions.
func BenchmarkRDPUDPRoundTrip(b *testing.B) {
	for _, size := range []struct {
		name string
		n    int
	}{{"64B", 64}, {"1200B", 1200}, {"8KiB", 8 << 10}} {
		b.Run(size.name, func(b *testing.B) {
			session, local := newDirectUDPPair(b)
			conn, err := session.DialUDP(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = conn.Close() })
			_ = conn.SetDeadline(time.Now().Add(time.Minute))
			done := make(chan struct{})
			go func() {
				defer close(done)
				buffer := make([]byte, size.n)
				for {
					n, source, err := local.ReadFromUDPAddrPort(buffer)
					if err != nil {
						return
					}
					if _, err := local.WriteToUDPAddrPort(buffer[:n], source); err != nil {
						return
					}
				}
			}()
			b.Cleanup(func() { _ = local.Close(); <-done })
			payload := make([]byte, size.n)
			b.SetBytes(int64(2 * size.n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := conn.WriteTo(payload, nil); err != nil {
					b.Fatal(err)
				}
				if n, _, err := conn.ReadFrom(payload); err != nil || n != size.n {
					b.Fatalf("round trip: n=%d err=%v", n, err)
				}
			}
		})
	}
}

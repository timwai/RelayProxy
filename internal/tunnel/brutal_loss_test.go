package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

// packetLossConn injects deterministic downstream UDP loss without root,
// external network dependencies, or sleeping in the send path.
// WriteTo returning a full length models a network drop rather than a
// local socket failure: QUIC must recover through its own loss detection.
type packetLossConn struct {
	net.PacketConn
	every   uint64
	sent    atomic.Uint64
	dropped atomic.Uint64
}

func (c *packetLossConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n := c.sent.Add(1)
	if c.every > 0 && n%c.every == 0 {
		c.dropped.Add(1)
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

// TestBrutalDeterministicPacketLoss is a transport regression smoke test,
// not a benchmark of real WAN goodput, jitter, or competing-flow fairness.
// Both controllers must recover complete payloads under the same conditions.
func TestBrutalDeterministicPacketLoss(t *testing.T) {
	const (
		payloadSize = 512 << 10
		brutalBPS   = 8_000_000
	)
	payload := bytes.Repeat([]byte("relayproxy-quic-loss-fixture"), payloadSize/len("relayproxy-quic-loss-fixture")+1)[:payloadSize]
	for _, loss := range []struct {
		name  string
		every uint64
	}{
		{name: "loss0", every: 0},
		{name: "loss1", every: 100},
		{name: "loss5", every: 20},
	} {
		for _, mode := range []string{"bbr", "brutal"} {
			t.Run(loss.name+"/"+mode, func(t *testing.T) {
				if err := transferWithDeterministicLoss(t, payload, loss.every, mode, brutalBPS); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func transferWithDeterministicLoss(t *testing.T, payload []byte, dropEvery uint64, mode string, targetBPS uint64) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	rawUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer rawUDP.Close()
	serverUDP := &packetLossConn{PacketConn: rawUDP, every: dropEvery}
	cert := generateSelfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{"relayproxy-quic"}}
	listener, err := quic.Listen(serverUDP, serverTLS, DefaultQUICConfig())
	if err != nil {
		return fmt.Errorf("listen QUIC: %w", err)
	}
	defer listener.Close()

	clientUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer clientUDP.Close()
	clientTLS := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"relayproxy-quic"}}

	accepted := make(chan *quic.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- conn
	}()
	conn, err := quic.Dial(ctx, clientUDP, listener.Addr(), clientTLS, DefaultQUICConfig())
	if err != nil {
		return fmt.Errorf("dial QUIC: %w", err)
	}
	client := NewQUICSession(conn)
	defer client.Close()

	var serverConn *quic.Conn
	select {
	case serverConn = <-accepted:
	case err = <-acceptErr:
		return fmt.Errorf("accept QUIC: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	server := NewQUICSession(serverConn)
	defer server.Close()
	if mode == "brutal" && !UseBrutal(server, targetBPS, false) {
		return fmt.Errorf("failed to apply Brutal")
	}
	before := DiagnoseSession(server)
	if before == nil || before.QUIC == nil {
		return fmt.Errorf("missing initial QUIC diagnostics")
	}
	expected := "bbr-aggressive"
	expectedRate := uint64(0)
	if mode == "brutal" {
		expected, expectedRate = "brutal", targetBPS
	}
	if before.QUIC.CongestionController != expected || before.QUIC.CongestionTargetBPS != expectedRate {
		return fmt.Errorf("unexpected controller: %+v", before.QUIC)
	}
	serverStream, err := server.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("open server QUIC stream: %w", err)
	}
	defer serverStream.Close()
	if err := serverStream.SetWriteDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	start := time.Now()
	written := make(chan error, 1)
	go func() {
		n, err := serverStream.Write(payload)
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = serverStream.CloseWrite()
		}
		written <- err
	}()
	clientStream, err := client.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept client QUIC stream: %w", err)
	}
	defer clientStream.Close()
	if err := clientStream.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(clientStream, got); err != nil {
		return fmt.Errorf("receive payload: %w", err)
	}
	if err := <-written; err != nil {
		return fmt.Errorf("send payload: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("received payload differs from transmitted data")
	}
	elapsed := time.Since(start)
	after := DiagnoseSession(server)
	if after == nil || after.QUIC == nil {
		return fmt.Errorf("missing final QUIC diagnostics")
	}
	if dropEvery > 0 && serverUDP.dropped.Load() == 0 {
		return fmt.Errorf("loss injection was not exercised (sent=%d)", serverUDP.sent.Load())
	}
	if dropEvery == 0 && serverUDP.dropped.Load() != 0 {
		return fmt.Errorf("zero-loss fixture dropped packets")
	}
	t.Logf("mode=%s every=%d target_bps=%d bytes=%d elapsed_ms=%d goodput_mbps=%.3f packet_sent=%d packet_lost=%d dropped_udp=%d smoothed_rtt_ms=%.3f",
		mode, dropEvery, expectedRate, len(payload), elapsed.Milliseconds(),
		float64(len(payload)*8)/elapsed.Seconds()/1e6, after.QUIC.PacketsSent,
		after.QUIC.SentPacketsLost, serverUDP.dropped.Load(), after.QUIC.SmoothedRTTMS)
	return nil
}

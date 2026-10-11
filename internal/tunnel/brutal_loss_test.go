package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
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

// deterministicPacketDrop hashes the packet index so the same loss rate does
// not systematically hit each 20th retransmission. Independent runs remain
// reproducible, without depending on process scheduling or random seeds.
func deterministicPacketDrop(index, inverseProbability uint64) bool {
	if inverseProbability == 0 {
		return false
	}
	value := uint32(index)
	value ^= value >> 16
	value *= 0x7feb352d
	value ^= value >> 15
	value *= 0x846ca68b
	value ^= value >> 16
	return uint64(value)%inverseProbability == 0
}

func (c *packetLossConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n := c.sent.Add(1)
	if deterministicPacketDrop(n, c.every) {
		c.dropped.Add(1)
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

func TestDeterministicPacketDropDistribution(t *testing.T) {
	const packets = 10_000
	for _, scenario := range []struct {
		every uint64
		want  int
	}{
		{every: 0, want: 0},
		{every: 100, want: 100},
		{every: 20, want: 500},
	} {
		drops := 0
		for i := uint64(1); i <= packets; i++ {
			if deterministicPacketDrop(i, scenario.every) {
				drops++
			}
		}
		margin := scenario.want / 5
		if drops < scenario.want-margin || drops > scenario.want+margin {
			t.Errorf("inverseProbability=%d yielded %d drops, expected %d +/- %d", scenario.every, drops, scenario.want, margin)
		}
		if scenario.every > 0 && deterministicPacketDrop(20, scenario.every) == deterministicPacketDrop(40, scenario.every) &&
			deterministicPacketDrop(40, scenario.every) == deterministicPacketDrop(60, scenario.every) &&
			deterministicPacketDrop(60, scenario.every) {
			t.Errorf("loss pattern is still synchronized to every 20th UDP packet")
		}
	}
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

// kernelFlowSample records receiver progress and local sender RTT with a
// common wall clock for offline overlap analysis. This is not end-to-end RTT.
type kernelFlowSample struct {
	At            time.Time
	ReceivedBytes uint64
	SmoothedRTTMS float64
}

type transferByteCounter struct {
	bytes atomic.Uint64
}

func (counter *transferByteCounter) Write(p []byte) (int, error) {
	counter.bytes.Add(uint64(len(p)))
	return len(p), nil
}

func transferWithDeterministicLoss(t *testing.T, payload []byte, dropEvery uint64, mode string, targetBPS uint64, observers ...func(kernelFlowSample)) error {
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
	var counter transferByteCounter
	if len(observers) > 0 {
		finished := make(chan struct{})
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case at := <-ticker.C:
					diag := DiagnoseSession(server)
					if diag != nil && diag.QUIC != nil {
						observers[0](kernelFlowSample{
							At: at, ReceivedBytes: counter.bytes.Load(),
							SmoothedRTTMS: diag.QUIC.SmoothedRTTMS,
						})
					}
				case <-finished:
					return
				}
			}
		}()
		// Keep the sampling goroutine from touching the QUIC session after
		// this function returns and closes the session.
		defer func() {
			close(finished)
			<-stopped
		}()
	}
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
	var reader io.Reader = clientStream
	if len(observers) > 0 {
		reader = io.TeeReader(clientStream, &counter)
	}
	if _, err := io.ReadFull(reader, got); err != nil {
		diag := DiagnoseSession(server)
		if diag != nil && diag.QUIC != nil {
			return fmt.Errorf("receive payload: %w; mode=%s sent_udp=%d injected_drops=%d sent_packets=%d sent_lost=%d rtt_ms=%.3f",
				err, mode, serverUDP.sent.Load(), serverUDP.dropped.Load(),
				diag.QUIC.PacketsSent, diag.QUIC.SentPacketsLost, diag.QUIC.SmoothedRTTMS)
		}
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

// TestBrutalKernelNetemDataPlane is opt-in because it needs tc netem to be
// installed outside the process. It verifies data integrity across a
// real kernel-shaped UDP path.
func TestBrutalKernelNetemDataPlane(t *testing.T) {
	if os.Getenv("RELAYPROXY_BRUTAL_NETEM") != "1" {
		t.Skip("requires externally configured Linux tc netem qdisc")
	}
	payload := bytes.Repeat([]byte("kernel-netem-test-payload"), (2<<20)/len("kernel-netem-test-payload")+1)[:2<<20]
	for _, mode := range []string{"bbr", "brutal"} {
		t.Run(mode, func(t *testing.T) {
			if err := transferWithDeterministicLoss(t, payload, 0, mode, 2_000_000); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestBrutalKernelNetemCoexistence records two independent concurrent
// QUIC sessions sharing a kernel qdisc. End-to-end completion throughput
// is *not* a time-resolved or real-WAN fairness measurement.
func TestBrutalKernelNetemCoexistence(t *testing.T) {
	if os.Getenv("RELAYPROXY_BRUTAL_NETEM") != "1" {
		t.Skip("requires externally configured Linux tc netem qdisc")
	}
	payload := bytes.Repeat([]byte("brutal-bbr-shared-bottleneck"), (4<<20)/len("brutal-bbr-shared-bottleneck")+1)[:4<<20]
	for _, scenario := range []struct {
		name      string
		targetBPS uint64
	}{
		{name: "overcommit-16mbit", targetBPS: 2_000_000},
		{name: "under-bottleneck-8mbit", targetBPS: 1_000_000},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			modes := []string{"bbr", "brutal"}
			var durations [2]time.Duration
			var results [2]error
			var samples [2][]kernelFlowSample
			var group sync.WaitGroup
			start := make(chan struct{})
			for i, mode := range modes {
				group.Add(1)
				go func(index int, controller string) {
					defer group.Done()
					<-start
					begin := time.Now()
					results[index] = transferWithDeterministicLoss(t, payload, 0, controller, scenario.targetBPS,
						func(sample kernelFlowSample) {
							samples[index] = append(samples[index], sample)
						})
					durations[index] = time.Since(begin)
				}(i, mode)
			}
			close(start)
			group.Wait()
			for index, mode := range modes {
				if results[index] != nil {
					t.Errorf("%s transfer: %v", mode, results[index])
				} else {
					t.Logf("shared_netem scenario=%s mode=%s target_bps=%d bytes=%d elapsed_ms=%d completion_goodput_mbps=%.3f",
						scenario.name, mode, scenario.targetBPS, len(payload), durations[index].Milliseconds(),
						float64(len(payload)*8)/durations[index].Seconds()/1e6)
					if len(samples[index]) < 2 {
						t.Errorf("%s produced only %d sampled points; cannot evaluate overlap", mode, len(samples[index]))
					}
					var previous kernelFlowSample
					for _, sample := range samples[index] {
						if !previous.At.IsZero() {
							seconds := sample.At.Sub(previous.At).Seconds()
							if seconds > 0 && sample.ReceivedBytes >= previous.ReceivedBytes {
								t.Logf("overlap_sample scenario=%s mode=%s wall_unix_ms=%d interval_goodput_mbps=%.3f sender_smoothed_rtt_ms=%.3f received_bytes=%d",
									scenario.name, mode, sample.At.UnixMilli(),
									float64(sample.ReceivedBytes-previous.ReceivedBytes)*8/seconds/1e6,
									sample.SmoothedRTTMS, sample.ReceivedBytes)
							}
						}
						previous = sample
					}
				}
			}
		})
	}
}

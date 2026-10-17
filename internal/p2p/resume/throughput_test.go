package resume

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestHalfCloseKeepsReverseACKProcessing(t *testing.T) {
	client, server := newEndpointPair(t, 512<<10)
	bindEndpointPair(t, client, server, 1)
	defer client.Abort(net.ErrClosed)
	defer server.Abort(net.ErrClosed)
	if _, err := client.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(server, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if n, err := server.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("half-close read=%d err=%v", n, err)
	}
	done := make(chan error, 1)
	go func() { _, err := server.Write(make([]byte, 1<<20)); done <- err }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(client, make([]byte, 1<<20))
	if n != 1<<20 || err != nil {
		t.Fatalf("download stalled: read=%d err=%v", n, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestDefaultReplayWindowAccommodatesDelayedACKs(t *testing.T) {
	measure := func(limit int) time.Duration {
		state, err := NewRandomStreamState(limit)
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := NewEndpoint(state)
		if err != nil {
			t.Fatal(err)
		}
		transport, peer := net.Pipe()
		stop := make(chan struct{})
		readerDone := make(chan struct{})
		var ackWorkers sync.WaitGroup
		var ackWrite sync.Mutex
		go func() {
			defer close(readerDone)
			for {
				frame, err := ReadFrame(peer)
				if err != nil {
					return
				}
				if frame.Type != FrameData {
					continue
				}
				offset := frame.Seq + uint64(len(frame.Payload))
				ackWorkers.Add(1)
				go func() {
					defer ackWorkers.Done()
					delay := time.NewTimer(100 * time.Millisecond)
					defer delay.Stop()
					select {
					case <-stop:
						return
					case <-delay.C:
					}
					ackWrite.Lock()
					defer ackWrite.Unlock()
					_ = WriteFrame(peer, Frame{Type: FrameAck, Ack: offset})
				}()
			}
		}()
		defer func() {
			close(stop)
			endpoint.Abort(net.ErrClosed)
			_ = peer.Close()
			<-readerDone
			ackWorkers.Wait()
		}()
		if err := endpoint.Bind(transport, 1); err != nil {
			t.Fatal(err)
		}
		_ = endpoint.SetWriteDeadline(time.Now().Add(4 * time.Second))
		payload := make([]byte, 4<<20)
		start := time.Now()
		n, err := endpoint.Write(payload)
		elapsed := time.Since(start)
		if err != nil || n != len(payload) {
			t.Fatalf("sent=%d err=%v", n, err)
		}
		t.Logf("replay limit=%d KiB, ACK delay=100ms, 4MiB transfer=%v (%.1f Mbps)", state.send.limit>>10, elapsed, float64(n)*8/elapsed.Seconds()/1e6)
		return elapsed
	}
	limited := measure(512 << 10)
	roomy := measure(0)
	if limited < 650*time.Millisecond || roomy >= limited/2 {
		t.Fatalf("default window is RTT limited: explicit 512KiB=%v default=%v", limited, roomy)
	}
}

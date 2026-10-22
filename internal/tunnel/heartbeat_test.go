package tunnel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

type heartbeatTestStream struct{ net.Conn }

func (s heartbeatTestStream) CloseWrite() error { return nil }

func TestHeartbeatRetainsPartialFrameAcrossSoftTimeouts(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		var ping protocol.PingMessage
		if err := protocol.ReadJSON(b, &ping); err != nil {
			done <- err
			return
		}
		var buf bytes.Buffer
		_ = protocol.WriteJSON(&buf, protocol.PongMessage{Timestamp: 42})
		frame := buf.Bytes()
		_, err := b.Write(frame[:2])
		if err == nil {
			time.Sleep(130 * time.Millisecond)
			_, err = b.Write(frame[2:8])
		}
		if err == nil {
			time.Sleep(130 * time.Millisecond)
			_, err = b.Write(frame[8:])
		}
		done <- err
	}()
	pong, _, err := heartbeatExchange(context.Background(), heartbeatTestStream{a}, protocol.PingMessage{}, 100*time.Millisecond, 350*time.Millisecond)
	if err != nil || pong.Timestamp != 42 {
		t.Fatalf("partial frame lost: pong=%+v err=%v", pong, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// A second frame must still be aligned after the extended first exchange.
	go func() {
		var ping protocol.PingMessage
		_ = protocol.ReadJSON(b, &ping)
		_ = protocol.WriteJSON(b, protocol.PongMessage{Timestamp: 43})
	}()
	pong, _, err = heartbeatExchange(context.Background(), heartbeatTestStream{a}, protocol.PingMessage{}, time.Second, time.Second)
	if err != nil || pong.Timestamp != 43 {
		t.Fatalf("next frame misaligned: %+v %v", pong, err)
	}
}

func TestHeartbeatBoundsTimeoutAndHonorsCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		a, b := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			var ping protocol.PingMessage
			_ = protocol.ReadJSON(b, &ping)
			if cancelEarly {
				cancel()
			}
		}()
		start := time.Now()
		_, _, err := heartbeatExchange(ctx, heartbeatTestStream{a}, protocol.PingMessage{}, 30*time.Millisecond, 90*time.Millisecond)
		cancel()
		a.Close()
		b.Close()
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("unbounded wait: %v", err)
		}
		if cancelEarly && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	}
}

func TestHeartbeatEOFIsFatal(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	go func() { var ping protocol.PingMessage; _ = protocol.ReadJSON(b, &ping); b.Close() }()
	_, _, err := heartbeatExchange(context.Background(), heartbeatTestStream{a}, protocol.PingMessage{}, time.Second, 3*time.Second)
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("EOF was retried: %v", err)
	}
}

package yamux

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestReceiveBudgetCountsCapacityAndGrowth(t *testing.T) {
	s := &Session{config: &Config{MaxSessionBufferSize: 96, MaxStreamWindowSize: 256}}
	a, b := &receiveBuffer{session: s}, &receiveBuffer{session: s}
	defer a.reset()
	defer b.reset()
	if _, err := a.readFrom(bytes.NewReader(make([]byte, 64)), 64); err != nil {
		t.Fatal(err)
	}
	if _, err := b.readFrom(bytes.NewReader(make([]byte, 33)), 33); !errors.Is(err, ErrSessionBufferExceeded) {
		t.Fatalf("aggregate overflow allocated: %v", err)
	}
	var p [32]byte
	_, _ = a.Read(p[:])
	if got := s.ReceiveBufferBytes(); got != 64 {
		t.Fatalf("partially read array charged %d instead of 64", got)
	}
	_, _ = a.Read(p[:])
	if got := s.ReceiveBufferBytes(); got != 0 {
		t.Fatalf("empty array retained %d bytes", got)
	}
	if _, err := b.readFrom(bytes.NewReader(make([]byte, 32)), 32); err != nil {
		t.Fatal(err)
	}
	// Growing 32 -> 96 requires 128 bytes while both arrays exist, exceeding
	// the 96-byte budget even though the final allocation alone would fit.
	if _, err := b.readFrom(bytes.NewReader(make([]byte, 64)), 64); !errors.Is(err, ErrSessionBufferExceeded) {
		t.Fatalf("growth ignored old backing array: %v", err)
	}
	if got := s.ReceiveBufferBytes(); got != 32 || b.Len() != 32 {
		t.Fatalf("failed reservation changed buffer: charged=%d len=%d", got, b.Len())
	}
}

func TestRemoteEOFPreservesBudgetedBufferedData(t *testing.T) {
	a, b := net.Pipe()
	cfg := DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.MaxSessionBufferSize = 64 << 10
	client, err := Client(a, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := Server(b, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	sender, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	_ = sender.SetDeadline(time.Now().Add(2 * time.Second))
	payload := bytes.Repeat([]byte{5}, 32<<10)
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}
	waitReceiveBytes(t, server, 32<<10)
	_ = client.Close()
	select {
	case <-server.CloseChan():
	case <-time.After(time.Second):
		t.Fatal("remote EOF did not close session")
	}
	// Wait until automatic shutdown has finished, making this independent of
	// the race between reading and the remote transport's EOF callback.
	server.shutdownLock.Lock()
	server.shutdownLock.Unlock()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(receiver, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("remote EOF discarded received data: %v", err)
	}
	waitReceiveBytes(t, server, 0)
}

func TestReceiveBudgetBeforeAndAfterAccept(t *testing.T) {
	for _, acceptFirst := range []bool{false, true} {
		name := "before_accept"
		if acceptFirst {
			name = "after_accept"
		}
		t.Run(name, func(t *testing.T) {
			a, b := net.Pipe()
			cfg := DefaultConfig()
			cfg.EnableKeepAlive = false
			cfg.MaxSessionBufferSize = 256 << 10
			client, err := Client(a, cfg.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			server, err := Server(b, cfg.Clone())
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			var senders, receivers []*Stream
			for i := 0; i < 2; i++ {
				stream, err := client.OpenStream()
				if err != nil {
					t.Fatal(err)
				}
				senders = append(senders, stream)
				if acceptFirst {
					r, err := server.AcceptStream()
					if err != nil {
						t.Fatal(err)
					}
					receivers = append(receivers, r)
				}
				_ = stream.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := stream.Write(bytes.Repeat([]byte{7}, 128<<10)); err != nil {
					t.Fatal(err)
				}
			}
			waitReceiveBytes(t, server, 256<<10)
			overflow, err := client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			_ = overflow.SetDeadline(time.Now().Add(2 * time.Second))
			if _, err := overflow.Write(make([]byte, 32<<10)); err != nil {
				t.Fatal(err)
			}
			var p [1]byte
			if _, err := overflow.Read(p[:]); !errors.Is(err, ErrConnectionReset) {
				t.Fatalf("over-budget stream was not reset: %v", err)
			}
			client.streamLock.Lock()
			_, leaked := client.inflight[overflow.id]
			client.streamLock.Unlock()
			if leaked {
				t.Fatal("reset stream retained an inflight SYN reservation")
			}
			waitReceiveBytes(t, server, 256<<10)
			if server.IsClosed() {
				t.Fatal("one overflow closed unrelated streams")
			}
			if !acceptFirst {
				for i := 0; i < 2; i++ {
					r, err := server.AcceptStream()
					if err != nil {
						t.Fatal(err)
					}
					receivers = append(receivers, r)
				}
			}
			_ = receivers[0].SetReadDeadline(time.Now().Add(time.Second))
			data := make([]byte, 128<<10)
			if _, err := io.ReadFull(receivers[0], data); err != nil || !bytes.Equal(data, bytes.Repeat([]byte{7}, len(data))) {
				t.Fatalf("unrelated stream lost data: %v", err)
			}
			waitReceiveBytes(t, server, 128<<10)
			// Full local close must free unread buffers and ignore future data.
			_ = receivers[1].CloseRead()
			waitReceiveBytes(t, server, 0)
			if _, err := senders[1].Write(make([]byte, 64<<10)); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Ping(); err != nil {
				t.Fatal(err)
			}
			waitReceiveBytes(t, server, 0)
			// RST and session shutdown must also release their reservations.
			if _, err := senders[0].Write(make([]byte, 64<<10)); err != nil {
				t.Fatal(err)
			}
			waitReceiveBytes(t, server, 64<<10)
			if err := senders[0].Reset(); err != nil {
				t.Fatal(err)
			}
			waitReceiveBytes(t, server, 0)
			pending, err := client.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pending.Write(make([]byte, 64<<10)); err != nil {
				t.Fatal(err)
			}
			waitReceiveBytes(t, server, 64<<10)
			_ = server.Close()
			waitReceiveBytes(t, server, 0)
		})
	}
}

func waitReceiveBytes(t *testing.T, s *Session, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for s.ReceiveBufferBytes() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := s.ReceiveBufferBytes(); got != want {
		t.Fatalf("reserved receive bytes=%d, want %d", got, want)
	}
}

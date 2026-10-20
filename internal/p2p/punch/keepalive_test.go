package punch

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/p2p/secure"
)

func newKeepaliveTestPair(t *testing.T) (*PacketConn, *net.UDPConn, []byte) {
	t.Helper()
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	controller, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	conn, err := newPacketConn(&UDPResult{Conn: controller, RemoteAddr: target.LocalAddr().(*net.UDPAddr), SessionID: 42, Key: key}, 50*time.Millisecond)
	if err != nil {
		_ = controller.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, target, key
}

func TestPacketConnDetectsSilentPathFailure(t *testing.T) {
	conn, _, _ := newKeepaliveTestPair(t)
	// The destination socket stays open: outbound writes still succeed even
	// though every datagram is silently dropped by the path.
	if _, err := conn.WriteTo([]byte("input"), nil); err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, _, err := conn.ReadFrom(make([]byte, 64)); read <- err }()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("unresponsive UDP path returned data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent UDP failure left the RDP reader blocked")
	}
	if _, err := conn.WriteTo([]byte("retry"), nil); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("failed path continued accepting writes: %v", err)
	}
}

func TestPacketConnAcknowledgementsKeepQuietPathAlive(t *testing.T) {
	conn, target, key := newKeepaliveTestPair(t)
	var responding atomic.Bool
	responding.Store(true)
	acked := make(chan struct{}, 8)
	worker := make(chan struct{})
	go func() {
		defer close(worker)
		var wire [128]byte
		for {
			n, source, err := target.ReadFromUDP(wire[:])
			if err != nil {
				return
			}
			packet, err := secure.DecodePunchPacket(wire[:n], key)
			if err == nil && responding.Load() && WritePunchAck(target, source, packet, key) == nil {
				acked <- struct{}{}
			}
		}
	}()
	defer func() { _ = target.Close(); <-worker }()
	read := make(chan error, 1)
	go func() { _, _, err := conn.ReadFrom(make([]byte, 64)); read <- err }()
	for i := 0; i < missedKeepaliveLimit+2; i++ {
		select {
		case <-acked:
		case err := <-read:
			t.Fatalf("valid acknowledgements did not keep the path alive: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("keepalive did not reach the responder")
		}
	}
	// ACKs must be consumed internally, and must not appear as RDP data.
	select {
	case err := <-read:
		t.Fatalf("quiet healthy path returned from ReadFrom: %v", err)
	default:
	}
	responding.Store(false)
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("unresponsive path returned data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("path stopped responding but the reader stayed blocked")
	}
}

func TestPacketConnInvalidOrReplayedAckCannotKeepPathAlive(t *testing.T) {
	for _, kind := range []string{"nonce", "session", "key", "source", "replay"} {
		t.Run(kind, func(t *testing.T) {
			conn, target, key := newKeepaliveTestPair(t)
			wrongSource, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer wrongSource.Close()
			worker := make(chan struct{})
			go func() {
				defer close(worker)
				var wire [128]byte
				var first []byte
				for {
					n, source, err := target.ReadFromUDP(wire[:])
					if err != nil {
						return
					}
					packet, err := secure.DecodePunchPacket(wire[:n], key)
					if err != nil {
						continue
					}
					packet.Type = secure.PunchAck
					signer, sender := key, target
					switch kind {
					case "nonce":
						packet.Nonce ^= 1 << 63
					case "session":
						packet.SessionID++
					case "key":
						signer = []byte("wrong-key-1234567890")
					case "source":
						sender = wrongSource
					}
					reply := packet.Encode(signer)
					if kind == "replay" {
						if first == nil {
							first = reply
						}
						reply = first
					}
					_, _ = sender.WriteToUDP(reply, source)
				}
			}()
			defer func() { _ = target.Close(); <-worker }()
			read := make(chan error, 1)
			go func() { _, _, err := conn.ReadFrom(make([]byte, 64)); read <- err }()
			select {
			case err := <-read:
				if err == nil {
					t.Fatal("invalid ACK was delivered as RDP data")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("invalid or replayed ACK kept an unresponsive path alive")
			}
		})
	}
}

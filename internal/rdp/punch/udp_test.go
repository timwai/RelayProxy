package punch

import (
	"bytes"
	"net"
	"testing"
	"time"

	"relayproxy/internal/rdp/secure"
)

func TestPacketConnSendsAuthenticatedKeepalive(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	controller, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	conn, err := newPacketConn(&UDPResult{Conn: controller, RemoteAddr: target.LocalAddr().(*net.UDPAddr), SessionID: 42, Key: key}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 128)
	n, source, err := target.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := secure.DecodePunchPacket(buffer[:n], key)
	if err != nil {
		t.Fatalf("decode keepalive: %v", err)
	}
	if packet.Type != secure.PunchKeep || packet.SessionID != 42 {
		t.Fatalf("unexpected keepalive: %+v", packet)
	}
	if err := WritePunchAck(target, source, packet, key); err != nil {
		t.Fatalf("acknowledge keepalive: %v", err)
	}
	_ = controller.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err = controller.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := secure.DecodePunchPacket(buffer[:n], key)
	if err != nil || ack.Type != secure.PunchAck || ack.Nonce != packet.Nonce {
		t.Fatalf("unexpected keepalive ack: packet=%+v err=%v", ack, err)
	}
}

func TestFragmentAndReassembleOutOfOrder(t *testing.T) {
	payload := bytes.Repeat([]byte("animation-frame"), 400)
	var frames [][]byte
	if err := FragmentUDP(77, payload, func(frame []byte) error {
		frames = append(frames, append([]byte(nil), frame...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(frames) < 2 {
		t.Fatalf("payload was not fragmented: %d frames", len(frames))
	}
	reassembler := NewReassembler()
	dst := make([]byte, len(payload))
	for i := len(frames) - 1; i >= 0; i-- {
		n, complete, err := reassembler.Feed(frames[i], dst)
		if err != nil {
			t.Fatal(err)
		}
		if i != 0 && complete {
			t.Fatal("reassembly completed before the final fragment")
		}
		if i == 0 {
			if !complete || !bytes.Equal(dst[:n], payload) {
				t.Fatalf("reassembled payload mismatch: n=%d complete=%v", n, complete)
			}
		}
	}
}

func TestPacketConnRoundTripsLargeDatagram(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	sender, err := newPacketConn(&UDPResult{Conn: a, RemoteAddr: b.LocalAddr().(*net.UDPAddr), SessionID: 43, Key: key}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := newPacketConn(&UDPResult{Conn: b, RemoteAddr: a.LocalAddr().(*net.UDPAddr), SessionID: 43, Key: key}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	payload := bytes.Repeat([]byte("animated-window"), 500)
	if n, err := sender.WriteTo(payload, nil); err != nil || n != len(payload) {
		t.Fatalf("write large datagram: n=%d err=%v", n, err)
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(payload))
	n, _, err := receiver.ReadFrom(got)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(payload) || !bytes.Equal(got[:n], payload) {
		t.Fatalf("large datagram mismatch: n=%d want=%d", n, len(payload))
	}
}

func TestPacketConnDesktopDomainKeepaliveIsIsolated(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	controller, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	conn, err := newPacketConn(&UDPResult{
		Conn: controller, RemoteAddr: target.LocalAddr().(*net.UDPAddr),
		SessionID: 72, Key: key, Domain: secure.DomainDesktopMedia,
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 128)
	n, source, err := target.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := secure.DecodePunchPacketWithDomain(buffer[:n], key, secure.DomainDesktopMedia)
	if err != nil {
		t.Fatalf("decode desktop keepalive: %v", err)
	}
	if _, err := secure.DecodePunchPacket(buffer[:n], key); err != secure.ErrBadMAC {
		t.Fatalf("desktop keepalive accepted by RDP domain: %v", err)
	}
	if err := WritePunchAckWithDomain(target, source, packet, key, secure.DomainDesktopMedia); err != nil {
		t.Fatalf("acknowledge desktop keepalive: %v", err)
	}
}

func TestPacketConnMeasuresAuthenticatedKeepaliveRTT(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	controller, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	conn, err := newPacketConn(&UDPResult{
		Conn: controller, RemoteAddr: target.LocalAddr().(*net.UDPAddr),
		SessionID: 73, Key: key, Domain: secure.DomainDesktopMedia,
	}, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rtts := make(chan time.Duration, 1)
	conn.SetProbeObserver(func(rtt time.Duration) {
		select {
		case rtts <- rtt:
		default:
		}
	})

	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1500)
		_, _, err := conn.ReadFrom(buffer)
		readDone <- err
	}()

	_ = target.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 128)
	n, source, err := target.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	keep, err := secure.DecodePunchPacketWithDomain(buffer[:n], key, secure.DomainDesktopMedia)
	if err != nil {
		t.Fatalf("decode desktop keepalive: %v", err)
	}
	if keep.Type != secure.PunchKeep || keep.SessionID != 73 {
		t.Fatalf("unexpected keepalive: %+v", keep)
	}

	time.Sleep(5 * time.Millisecond)
	if err := WritePunchAckWithDomain(target, source, keep, key, secure.DomainDesktopMedia); err != nil {
		t.Fatalf("acknowledge keepalive: %v", err)
	}

	select {
	case rtt := <-rtts:
		if rtt <= 0 {
			t.Fatalf("rtt=%s", rtt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct-path RTT observation")
	}

	_ = conn.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("PacketConn reader did not stop after close")
	}
}

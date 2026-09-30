package punch

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
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

func TestPunchIsSymmetric(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	left, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		value *UDPResult
		err   error
	}
	leftCh := make(chan result, 1)
	rightCh := make(chan result, 1)
	go func() {
		value, punchErr := Punch(ctx, left, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: right.LocalAddr().String(),
		}}, 99, key, time.Second)
		leftCh <- result{value: value, err: punchErr}
	}()
	go func() {
		value, punchErr := Punch(ctx, right, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: left.LocalAddr().String(),
		}}, 99, key, time.Second)
		rightCh <- result{value: value, err: punchErr}
	}()

	for name, ch := range map[string]<-chan result{"left": leftCh, "right": rightCh} {
		item := <-ch
		if item.err != nil {
			t.Fatalf("%s symmetric punch failed: %v", name, item.err)
		}
		if item.value == nil || item.value.RemoteAddr == nil || item.value.SessionID != 99 {
			t.Fatalf("%s returned incomplete punch result: %#v", name, item.value)
		}
	}
}


func TestSendAllKeepsRacingWhenOneAddressFamilyFails(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	port := receiver.LocalAddr().(*net.UDPAddr).Port
	addresses := []netip.AddrPort{
		netip.AddrPortFrom(netip.IPv6Loopback(), uint16(port)),
		netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)),
	}
	payload := []byte("candidate-race")
	if err := sendAll(sender, payload, addresses); err != nil {
		t.Fatalf("mixed-family send aborted before usable candidate: %v", err)
	}

	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 64)
	n, _, err := receiver.ReadFromUDP(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("unexpected payload %q", buffer[:n])
	}
}

func TestPunchIsSymmetricOverIPv6(t *testing.T) {
	left, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer left.Close()
	right, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer right.Close()

	key := []byte("0123456789abcdef0123456789abcdef")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		value *UDPResult
		err   error
	}
	leftCh := make(chan result, 1)
	rightCh := make(chan result, 1)
	go func() {
		value, punchErr := Punch(ctx, left, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: right.LocalAddr().String(),
		}}, 100, key, time.Second)
		leftCh <- result{value: value, err: punchErr}
	}()
	go func() {
		value, punchErr := Punch(ctx, right, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: left.LocalAddr().String(),
		}}, 100, key, time.Second)
		rightCh <- result{value: value, err: punchErr}
	}()

	for name, ch := range map[string]<-chan result{"left": leftCh, "right": rightCh} {
		item := <-ch
		if item.err != nil {
			t.Fatalf("%s IPv6 symmetric punch failed: %v", name, item.err)
		}
		if item.value == nil || item.value.RemoteAddr == nil || item.value.SessionID != 100 {
			t.Fatalf("%s returned incomplete IPv6 punch result: %#v", name, item.value)
		}
		if item.value.RemoteAddr.IP.To4() != nil {
			t.Fatalf("%s unexpectedly used IPv4 remote: %v", name, item.value.RemoteAddr)
		}
	}
}

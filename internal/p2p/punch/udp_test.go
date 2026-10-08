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

func newPassiveResponder(t testing.TB, sessionID uint64, key []byte, reply func(secure.PunchPacket) []byte) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	go func() {
		defer close(done)
		buffer := make([]byte, 1500)
		for {
			n, source, err := conn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			packet, err := secure.DecodePunchPacket(buffer[:n], key)
			if err != nil || packet.SessionID != sessionID || packet.Type != secure.PunchRequest {
				continue
			}
			packet.Type = secure.PunchAck
			_, _ = conn.WriteToUDP(reply(packet), source)
		}
	}()
	return conn
}

func TestPunchResponderValidatesAcknowledgement(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, test := range []struct {
		name  string
		reply func(secure.PunchPacket) []byte
		valid bool
	}{
		{"valid", func(p secure.PunchPacket) []byte { return p.Encode(key) }, true},
		{"wrong_nonce", func(p secure.PunchPacket) []byte { p.Nonce++; return p.Encode(key) }, false},
		{"wrong_session", func(p secure.PunchPacket) []byte { p.SessionID++; return p.Encode(key) }, false},
		{"wrong_key", func(p secure.PunchPacket) []byte { return p.Encode([]byte("abcdef0123456789abcdef0123456789")) }, false},
		{"request_without_ack", func(p secure.PunchPacket) []byte { p.Type = secure.PunchRequest; return p.Encode(key) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newPassiveResponder(t, 42, key, test.reply)
			client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			timeout := 60 * time.Millisecond
			if test.valid {
				timeout = time.Second
			}
			result, err := PunchResponder(context.Background(), client, []protocol.P2PCandidate{{
				Protocol: "udp", Address: server.LocalAddr().String(),
			}}, 42, key, timeout)
			if test.valid {
				if err != nil || result == nil || result.RemoteAddr.String() != server.LocalAddr().String() {
					t.Fatalf("valid responder: result=%+v err=%v", result, err)
				}
			} else if err == nil || result != nil {
				t.Fatalf("invalid handshake accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSymmetricPunchStillRequiresPeerRequest(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	server := newPassiveResponder(t, 42, key, func(p secure.PunchPacket) []byte { return p.Encode(key) })
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := Punch(context.Background(), client, []protocol.P2PCandidate{{
		Protocol: "udp", Address: server.LocalAddr().String(),
	}}, 42, key, 60*time.Millisecond)
	if err == nil || result != nil {
		t.Fatalf("symmetric handshake accepted an ACK alone: result=%+v err=%v", result, err)
	}
}

func TestPunchResponderKeepsMultipleCandidateSelection(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	first := newPassiveResponder(t, 42, key, func(p secure.PunchPacket) []byte {
		return p.Encode(key)
	})
	preferred := newPassiveResponder(t, 42, key, func(p secure.PunchPacket) []byte {
		// The preferred path can answer after the first usable candidate.
		time.Sleep(10 * time.Millisecond)
		return p.Encode(key)
	})
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := PunchResponder(context.Background(), client, []protocol.P2PCandidate{
		{Protocol: "udp", Address: first.LocalAddr().String(), Priority: 800},
		{Protocol: "udp", Address: preferred.LocalAddr().String(), Priority: 1200},
	}, 42, key, time.Second)
	if err != nil || result == nil || result.RemoteAddr.String() != preferred.LocalAddr().String() {
		t.Fatalf("candidate selection: result=%+v err=%v", result, err)
	}
}

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

func TestSymmetricPunchWithDualStackAndIPv4OnlyPeer(t *testing.T) {
	dual, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer dual.Close()
	ipv4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ipv4.Close()
	key := []byte("0123456789abcdef0123456789abcdef")
	port := dual.LocalAddr().(*net.UDPAddr).Port
	dualV4 := (&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}).String()
	dualV6 := (&net.UDPAddr{IP: net.IPv6loopback, Port: port}).String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		value *UDPResult
		err   error
	}
	dualResult := make(chan result, 1)
	ipv4Result := make(chan result, 1)
	go func() {
		value, punchErr := Punch(ctx, dual, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: ipv4.LocalAddr().String(),
		}}, 109, key, time.Second)
		dualResult <- result{value, punchErr}
	}()
	go func() {
		value, punchErr := Punch(ctx, ipv4, []protocol.P2PCandidate{
			{Protocol: "udp", Type: "lan", Address: dualV6, Priority: 1100},
			{Protocol: "udp", Type: "lan", Address: dualV4, Priority: 1000},
		}, 109, key, time.Second)
		ipv4Result <- result{value, punchErr}
	}()
	for name, ch := range map[string]<-chan result{"dual-stack": dualResult, "ipv4-only": ipv4Result} {
		got := <-ch
		if got.err != nil || got.value == nil {
			t.Fatalf("%s mixed family punch failed: result=%#v err=%v", name, got.value, got.err)
		}
		if got.value.RemoteAddr.IP.To4() == nil {
			t.Fatalf("%s selected unusable IPv6 address: %v", name, got.value.RemoteAddr)
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

func TestCandidateScoreBalancesPreferenceAndLatency(t *testing.T) {
	highPriority := candidateScore(1200, 80*time.Millisecond)
	lowerPriority := candidateScore(1100, 5*time.Millisecond)
	if highPriority <= lowerPriority {
		t.Fatalf("preferred LAN path score=%d, IPv6 score=%d", highPriority, lowerPriority)
	}

	fast := candidateScore(1100, 10*time.Millisecond)
	slow := candidateScore(1100, 70*time.Millisecond)
	if fast <= slow {
		t.Fatalf("same-class lower RTT was not preferred: fast=%d slow=%d", fast, slow)
	}
}

func TestBestPunchObservationDoesNotMixHandshakeAcrossCandidates(t *testing.T) {
	left := netip.MustParseAddrPort("192.0.2.10:5000")
	right := netip.MustParseAddrPort("198.51.100.10:5000")
	observations := map[netip.AddrPort]*punchObservation{
		left:  {priority: 1200, gotAck: true, rtt: 10 * time.Millisecond},
		right: {priority: 800, sawPeerRequest: true, rtt: 10 * time.Millisecond},
	}
	if _, _, ok := bestPunchObservation(observations); ok {
		t.Fatal("split punch handshake across two candidates was treated as viable")
	}

	observations[left].sawPeerRequest = true
	observations[left].ready = true
	observations[left].rtt = 40 * time.Millisecond
	observations[right].gotAck = true
	observations[right].ready = true
	observations[right].rtt = 5 * time.Millisecond
	selected, _, ok := bestPunchObservation(observations)
	if !ok || selected != left {
		t.Fatalf("selected candidate=%v ok=%v, want preferred LAN %v", selected, ok, left)
	}
}

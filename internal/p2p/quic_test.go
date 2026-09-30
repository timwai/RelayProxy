package p2p

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/p2p/punch"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestPunchedSocketCarriesPinnedQUIC(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	clientUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	exitUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	clientIdentity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	exitIdentity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type punchResult struct {
		value *punch.UDPResult
		err   error
	}
	clientPunch := make(chan punchResult, 1)
	exitPunch := make(chan punchResult, 1)
	go func() {
		value, punchErr := punch.Punch(ctx, clientUDP, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: exitUDP.LocalAddr().String(),
		}}, 123, key, time.Second)
		clientPunch <- punchResult{value: value, err: punchErr}
	}()
	go func() {
		value, punchErr := punch.Punch(ctx, exitUDP, []protocol.P2PCandidate{{
			Protocol: "udp", Type: "lan", Address: clientUDP.LocalAddr().String(),
		}}, 123, key, time.Second)
		exitPunch <- punchResult{value: value, err: punchErr}
	}()
	clientResult := <-clientPunch
	exitResult := <-exitPunch
	if clientResult.err != nil || exitResult.err != nil {
		t.Fatalf("punch failed: client=%v exit=%v", clientResult.err, exitResult.err)
	}

	serverCh := make(chan *QUICSession, 1)
	serverErr := make(chan error, 1)
	go func() {
		session, acceptErr := AcceptQUIC(ctx, exitResult.value.Conn, exitIdentity, clientIdentity.Fingerprint)
		if acceptErr != nil {
			serverErr <- acceptErr
			return
		}
		serverCh <- session
	}()
	clientSession, err := DialQUIC(ctx, clientResult.value.Conn, clientResult.value.RemoteAddr, clientIdentity, exitIdentity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	var exitSession *QUICSession
	select {
	case err := <-serverErr:
		t.Fatal(err)
	case exitSession = <-serverCh:
	}
	defer exitSession.Close()
	if !tunnel.PeerSupportsDatagrams(clientSession.QUICSession) || !tunnel.PeerSupportsDatagrams(exitSession.QUICSession) {
		t.Fatal("direct QUIC session did not advertise native UDP datagram support")
	}

	stream, err := clientSession.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("relayproxy-p2p")); err != nil {
		t.Fatal(err)
	}
	accepted, err := exitSession.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	buffer := make([]byte, len("relayproxy-p2p"))
	if _, err := io.ReadFull(accepted, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "relayproxy-p2p" {
		t.Fatalf("unexpected QUIC payload %q", buffer)
	}
}

func TestQUICRejectsUnexpectedFingerprint(t *testing.T) {
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clientTLSConfig(identity, ""); err == nil {
		t.Fatal("client TLS accepted empty peer fingerprint")
	}
	if _, err := serverTLSConfig(identity, ""); err == nil {
		t.Fatal("server TLS accepted empty peer fingerprint")
	}
}

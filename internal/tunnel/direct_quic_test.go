package tunnel

import (
	"context"
	"crypto/tls"
	"io"
	"testing"
	"time"

	"relayproxy/internal/p2p/secure"
)

func TestDirectQUICListenerCarriesStreamsAndDatagrams(t *testing.T) {
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := ListenDirectQUIC("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{identity.Certificate},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan *QUICSession, 1)
	acceptErr := make(chan error, 1)
	go func() {
		session, err := listener.Accept(ctx)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- session
	}()

	client, err := DialDirectQUIC(ctx, listener.Addr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var server *QUICSession
	select {
	case err := <-acceptErr:
		t.Fatal(err)
	case server = <-accepted:
	}
	defer server.Close()

	if !PeerSupportsDatagrams(client) || !PeerSupportsDatagrams(server) {
		t.Fatal("direct QUIC did not enable negotiated datagram support")
	}

	stream, err := client.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("direct-quic")); err != nil {
		t.Fatal(err)
	}
	peer, err := server.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	got := make([]byte, len("direct-quic"))
	if _, err := io.ReadFull(peer, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "direct-quic" {
		t.Fatalf("payload=%q", got)
	}
}

func TestDirectQUICRequiresExplicitTLS(t *testing.T) {
	if _, err := ListenDirectQUIC("127.0.0.1:0", nil, nil); err == nil {
		t.Fatal("listener accepted missing TLS config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := DialDirectQUIC(ctx, "127.0.0.1:1", nil, nil); err == nil {
		t.Fatal("dial accepted missing TLS config")
	}
}

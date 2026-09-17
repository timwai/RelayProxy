package tunnel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestManagerExplicitPlainTCPCanOpenStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		peer, err := ServerTLS(conn, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer peer.Close()
		stream, err := peer.AcceptStream(ctx)
		if err != nil {
			serverDone <- err
			return
		}
		defer stream.Close()
		_, err = io.Copy(stream, stream)
		serverDone <- err
	}()
	manager := NewTunnelManager(ManagerConfig{ServerAddress: "127.0.0.1", TCPPort: listener.Addr().(*net.TCPAddr).Port, Mode: ModeTCPOnly, PlainTCP: true}, nil)
	defer manager.Close()
	client, err := manager.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := stream.Write([]byte("plain TCP")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len("plain TCP"))
	if _, err := io.ReadFull(stream, reply); err != nil || string(reply) != "plain TCP" {
		t.Fatalf("explicit plaintext transport failed: %q, %v", reply, err)
	}
	_ = stream.CloseWrite()
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("plaintext peer did not finish")
	}
}

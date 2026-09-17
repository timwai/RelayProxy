package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
)

func sessionPair(t *testing.T, kind string) (TunnelSession, TunnelSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	var client, server TunnelSession
	if kind == "tls" {
		a, b := net.Pipe()
		cs, err := yamux.Client(a, nil)
		if err != nil {
			t.Fatal(err)
		}
		ss, err := yamux.Server(b, nil)
		if err != nil {
			t.Fatal(err)
		}
		client, server = NewTLSSession(a, cs), NewTLSSession(b, ss)
	} else {
		l, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{generateSelfSignedCert(t)}, NextProtos: []string{"relayproxy-quic"}}, &quic.Config{EnableDatagrams: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		accepted := make(chan *quic.Conn, 1)
		go func() { conn, _ := l.Accept(ctx); accepted <- conn }()
		client, err = DialQUIC(ctx, l.Addr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case c := <-accepted:
			if c == nil {
				t.Fatal("accept QUIC failed")
			}
			server = NewQUICSession(c)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		SetPeerCapabilities(client, []string{protocol.UDPModeDatagram})
		SetPeerCapabilities(server, []string{protocol.UDPModeDatagram})
	}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func streamPair(t *testing.T, client, server TunnelSession) (TunnelStream, TunnelStream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := client.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	b, err := server.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(b, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestStreamCloseUnblocksReadAndWrite(t *testing.T) {
	for _, kind := range []string{"tls", "quic"} {
		t.Run(kind, func(t *testing.T) {
			client, server := sessionPair(t, kind)
			a, _ := streamPair(t, client, server)
			conn := NewNetConnAdapter(a, client.LocalAddr(), client.RemoteAddr())
			reads, writes := make(chan error, 1), make(chan error, 1)
			go func() { _, err := conn.Read(make([]byte, 1)); reads <- err }()
			go func() { _, err := conn.Write(make([]byte, 4<<20)); writes <- err }()
			time.Sleep(10 * time.Millisecond)
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			for _, ch := range []chan error{reads, writes} {
				select {
				case err := <-ch:
					if err == nil {
						t.Fatal("closed I/O succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("Close did not unblock I/O")
				}
			}
			if err := conn.SetDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("deadline after Close: %v", err)
			}
		})
	}
}

func TestStreamHalfClosePreservesResponse(t *testing.T) {
	for _, kind := range []string{"tls", "quic"} {
		t.Run(kind, func(t *testing.T) {
			client, server := sessionPair(t, kind)
			a, b := streamPair(t, client, server)
			_ = a.SetDeadline(time.Now().Add(time.Second))
			_ = b.SetDeadline(time.Now().Add(time.Second))
			serverErr := make(chan error, 1)
			go func() {
				request, err := io.ReadAll(b)
				if err == nil && string(request) != "request" {
					err = errors.New("bad request")
				}
				if err == nil {
					_, err = b.Write([]byte("response"))
				}
				if err == nil {
					err = b.CloseWrite()
				}
				serverErr <- err
			}()
			if _, err := a.Write([]byte("request")); err != nil {
				t.Fatal(err)
			}
			if err := a.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			response, err := io.ReadAll(a)
			if err != nil || string(response) != "response" {
				t.Fatalf("response=%q err=%v", response, err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUDPStreamCallerDeadlinesAndPartialFrame(t *testing.T) {
	for _, partial := range []int{0, 2, 6} {
		t.Run(string(rune('0'+partial)), func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			c := NewUDPStreamConn(a, &net.UDPAddr{})
			defer c.Close()
			frame := []byte{0, 0, 0, 4, 'p', 'i', 'n', 'g'}
			go func() {
				if partial > 0 {
					_, _ = b.Write(frame[:partial])
				}
				time.Sleep(60 * time.Millisecond)
				_, _ = b.Write(frame[partial:])
			}()
			_ = c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
			buf := make([]byte, 32)
			if _, _, err := c.ReadFrom(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("expected caller timeout, got %v", err)
			}
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			n, _, err := c.ReadFrom(buf)
			if err != nil || string(buf[:n]) != "ping" {
				t.Fatalf("resume: %q %v", buf[:n], err)
			}
		})
	}
	t.Run("write", func(t *testing.T) {
		a, b := net.Pipe()
		defer b.Close()
		c := NewUDPStreamConn(a, &net.UDPAddr{})
		defer c.Close()
		_ = c.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
		if _, err := c.WriteTo([]byte("ping"), nil); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write: %v", err)
		}
	})
}

func TestNativeUDPFragmentsDeadlinesAndClose(t *testing.T) {
	client, server := sessionPair(t, "quic")
	a, b := streamPair(t, client, server)
	ca, err := OpenDatagramChannel(client, 0)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := OpenDatagramChannel(server, ca.ID)
	if err != nil {
		t.Fatal(err)
	}
	pcA, pcB := NewUDPDatagramConn(ca, a, &net.UDPAddr{}), NewUDPDatagramConn(cb, b, &net.UDPAddr{})
	defer pcA.Close()
	defer pcB.Close()
	_ = pcA.SetDeadline(time.Now().Add(time.Second))
	_ = pcB.SetDeadline(time.Now().Add(time.Second))
	payload := bytes.Repeat([]byte("fragment"), 512)
	if _, err := pcA.WriteTo(payload, nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65535)
	n, _, err := pcB.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], payload) {
		t.Fatalf("fragments: n=%d err=%v", n, err)
	}
	if _, err := pcB.WriteTo(nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, _, err := pcA.ReadFrom(buf); err != nil || n != 0 {
		t.Fatalf("empty UDP: n=%d err=%v", n, err)
	}
	_ = pcA.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := pcA.ReadFrom(buf); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	_ = pcA.SetReadDeadline(time.Time{})
	blocked := make(chan error, 1)
	go func() { _, _, err := pcA.ReadFrom(buf); blocked <- err }()
	_ = pcA.Close()
	select {
	case err := <-blocked:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("native Close left reader blocked")
	}
	if ca.mux.reassemblyBytes.Load() != 0 {
		t.Fatal("reassembly bytes retained")
	}
}

func TestNativeUDPAssociationIsolation(t *testing.T) {
	client, server := sessionPair(t, "quic")
	a, b := streamPair(t, client, server)
	ca, err := OpenDatagramChannel(client, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ca.Close()
	cb, err := OpenDatagramChannel(server, ca.ID+1)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.Close()
	pcA, pcB := NewUDPDatagramConn(ca, a, &net.UDPAddr{}), NewUDPDatagramConn(cb, b, &net.UDPAddr{})
	defer pcA.Close()
	defer pcB.Close()
	_ = pcB.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	_, _ = pcA.WriteTo([]byte("wrong association"), nil)
	if _, _, err := pcB.ReadFrom(make([]byte, 32)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("foreign association delivered: %v", err)
	}
}

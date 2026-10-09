package exit

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSOCKS5DomainBoundReplyNeverUsesLocalDNS(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		name := "untrusted-bound-name.invalid"
		reply := append([]byte{5, 0, 0, 3, byte(len(name))}, []byte(name)...)
		reply = append(reply, 0x1f, 0x90)
		_, err := server.Write(reply)
		done <- err
	}()
	bound, err := readSOCKS5Reply(client)
	if err != nil {
		t.Fatalf("CONNECT must accept an opaque bound hostname: %v", err)
	}
	if bound.IsValid() {
		t.Fatalf("domain bound address must remain unresolved, got %s", bound)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5UDPRejectsDomainBoundReplyWithoutDNSFallback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if err := acceptSOCKS5NoAuth(conn); err != nil {
			done <- err
			return
		}
		var associate [10]byte
		if _, err := io.ReadFull(conn, associate[:]); err != nil {
			done <- err
			return
		}
		if associate[0] != 5 || associate[1] != 3 {
			done <- errors.New("expected UDP ASSOCIATE")
			return
		}
		name := "untrusted-udp-relay.invalid"
		reply := append([]byte{5, 0, 0, 3, byte(len(name))}, []byte(name)...)
		reply = append(reply, 0x1f, 0x90)
		_, err = conn.Write(reply)
		done <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialSOCKS5UDPTo(ctx, UpstreamConfig{Mode: UpstreamSOCKS5, Address: listener.Addr().String()}, "example.org", 443)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("UDP ASSOCIATE accepted unresolved domain relay")
	}
	if err == nil || !strings.Contains(err.Error(), "refusing local DNS") {
		t.Fatalf("expected fail-closed DNS error, got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5UDPDomainSourceIsNeverLocallyResolved(t *testing.T) {
	address, err := encodeSOCKS5Address("untrusted-udp-source.invalid", 443)
	if err != nil {
		t.Fatal(err)
	}
	packet := append([]byte{0, 0, 0}, address...)
	packet = append(packet, []byte("response")...)
	if _, _, err := parseSOCKS5UDPAddress(packet, 3); err == nil || !strings.Contains(err.Error(), "requires remote DNS") {
		t.Fatalf("expected domain source to be rejected, got %v", err)
	}
	offset, err := skipSOCKS5UDPAddress(packet, 3)
	if err != nil || string(packet[offset:]) != "response" {
		t.Fatalf("opaque UDP domain reply must still be readable: offset=%d err=%v", offset, err)
	}
}

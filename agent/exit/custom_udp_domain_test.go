package exit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// This test uses an in-process SOCKS5 server rather than contacting Google.
// The proxy must receive the domain unchanged in UDP ATYP=0x03 and its
// response must not trigger local DNS resolution.
func TestCustomSOCKS5UDPDomainsStayRemote(t *testing.T) {
	relay, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	controlErr := make(chan error, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			controlErr <- err
			return
		}
		defer conn.Close()
		if err := acceptSOCKS5NoAuth(conn); err != nil {
			controlErr <- err
			return
		}
		var cmd [4]byte
		if _, err := io.ReadFull(conn, cmd[:]); err != nil {
			controlErr <- err
			return
		}
		if cmd != [4]byte{0x05, 0x03, 0x00, 0x01} {
			controlErr <- fmt.Errorf("unexpected SOCKS5 UDP ASSOCIATE: %x", cmd)
			return
		}
		if _, err := readSOCKS5Address(conn, cmd[3]); err != nil {
			controlErr <- err
			return
		}
		ap := relay.LocalAddr().(*net.UDPAddr).AddrPort()
		reply, _ := encodeSOCKS5Address(ap.Addr().String(), ap.Port())
		if _, err := conn.Write(append([]byte{0x05, 0, 0}, reply...)); err != nil {
			controlErr <- err
			return
		}
		_, err = io.Copy(io.Discard, conn)
		controlErr <- err
	}()

	udpErr := make(chan error, 1)
	go func() {
		_ = relay.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 65535)
		n, from, err := relay.ReadFromUDP(buf)
		if err != nil {
			udpErr <- err
			return
		}
		target, _ := encodeSOCKS5Address("play.google.com", 443)
		packet := append([]byte{0, 0, 0}, target...)
		packet = append(packet, []byte("hello-google")...)
		if !bytes.Equal(buf[:n], packet) {
			udpErr <- fmt.Errorf("SOCKS5 UDP datagram mismatch: got %x; want %x", buf[:n], packet)
			return
		}
		// The response uses a domain that must never be resolved locally.
		replyAddr, _ := encodeSOCKS5Address("does-not-resolve.invalid", 443)
		reply := append([]byte{0, 0, 0}, replyAddr...)
		reply = append(reply, []byte("remote-response")...)
		_, err = relay.WriteToUDP(reply, from)
		udpErr <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pc, err := DialViaUpstreamUDP(ctx, UpstreamConfig{
		Mode: UpstreamSOCKS5, Address: proxy.Addr().String(),
	}, "play.google.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	peer := (&socks5UDPTargetAddr{host: "play.google.com", port: 443}).String()
	if peer != "play.google.com:443" {
		t.Fatalf("domain peer changed: %s", peer)
	}
	if _, err := pc.WriteTo([]byte("hello-google"), nil); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, addr, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "remote-response" || addr.String() != peer {
		t.Fatalf("received %q from %s, expected the original DNS target", buf[:n], addr)
	}
	select {
	case err := <-udpErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS5 UDP relay did not receive a domain-encoded packet")
	}
	if _, err := pc.WriteTo([]byte("wrong-peer"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}); err == nil {
		t.Fatal("bound SOCKS5 UDP association accepted an unrelated destination")
	}
	_ = pc.Close()
	select {
	case err := <-controlErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS5 control stream did not close")
	}
}

func TestCustomSOCKS5UDPRejectsInvalidHostsBeforeConnect(t *testing.T) {
	for _, host := range []string{"", strings.Repeat("a", 256)} {
		pc, err := DialViaUpstreamUDP(context.Background(), UpstreamConfig{
			Mode: UpstreamSOCKS5, Address: "127.0.0.1:1",
		}, host, 443)
		if pc != nil {
			_ = pc.Close()
		}
		if err == nil {
			t.Fatalf("expected invalid host %q to fail", host)
		}
		if !strings.Contains(err.Error(), "hostname") {
			t.Fatalf("expected hostname validation error, got %v", err)
		}
	}
}

func TestSOCKS5UDPReplyAddressSkipsDomainWithoutResolving(t *testing.T) {
	addr, err := encodeSOCKS5Address("does-not-resolve.invalid", 443)
	if err != nil {
		t.Fatal(err)
	}
	packet := append([]byte{0, 0, 0}, addr...)
	packet = append(packet, []byte("udp-payload")...)
	offset, err := skipSOCKS5UDPAddress(packet, 3)
	if err != nil || string(packet[offset:]) != "udp-payload" {
		t.Fatalf("domain reply skip: offset=%d err=%v", offset, err)
	}
	_, err = skipSOCKS5UDPAddress([]byte{0, 0, 0, 0x03, 10}, 3)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated SOCKS5 domain header must fail, got %v", err)
	}
}

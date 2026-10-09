package divert

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPDNSTCPStreamUsesLocalResolverOnly(t *testing.T) {
	s := newTestServer(t, Options{Config: Config{DefaultAction: ActionProxy}})
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		done <- s.serveFakeDNSTCP(context.Background(), server)
	}()
	query := fakeDNSQuestion(t, "play.google.com", dnsmessage.TypeA)
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(query)))
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	buffer := net.Buffers{header[:], query}
	if _, err := buffer.WriteTo(client); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, header[:]); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, int(binary.BigEndian.Uint16(header[:])))
	if _, err := io.ReadFull(client, answer); err != nil {
		t.Fatal(err)
	}
	reply := fakeDNSAnswer(t, answer)
	if len(reply.Answers) != 1 || reply.RCode != dnsmessage.RCodeSuccess {
		t.Fatalf("fake DNS TCP did not answer locally: %+v", reply)
	}
	ip := netip.AddrFrom4(reply.Answers[0].Body.(*dnsmessage.AResource).A)
	if host, ok := s.fakeDNS.lookup(ip); !ok || host != "play.google.com" {
		t.Fatalf("TCP fake DNS mapping lost: %s %q %v", ip, host, ok)
	}
	_ = client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("local DNS TCP server shutdown failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("local DNS TCP server did not stop on client close")
	}
}

func TestFakeDNSTCPDoesNotAnswerAfterHotDisable(t *testing.T) {
	var enabled atomic.Bool
	enabled.Store(true)
	s := newTestServer(t, Options{
		FakeIPEnabled: enabled.Load,
	})
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- s.serveFakeDNSTCP(context.Background(), server) }()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	writeQuery := func() error {
		q := fakeDNSQuestion(t, "play.google.com", dnsmessage.TypeA)
		var prefix [2]byte
		binary.BigEndian.PutUint16(prefix[:], uint16(len(q)))
		buf := net.Buffers{prefix[:], q}
		_, err := buf.WriteTo(client)
		return err
	}
	if err := writeQuery(); err != nil {
		t.Fatal(err)
	}
	var header [2]byte
	if _, err := io.ReadFull(client, header[:]); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, binary.BigEndian.Uint16(header[:]))
	if _, err := io.ReadFull(client, answer); err != nil {
		t.Fatal(err)
	}
	if len(fakeDNSAnswer(t, answer).Answers) != 1 {
		t.Fatal("first DNS TCP response was missing")
	}
	enabled.Store(false)
	_ = writeQuery() // may race a legitimate server close; either result is safe
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("DNS TCP reader did not reject hot disable: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long-lived DNS/TCP server ignored disabled FakeIP policy")
	}
	if _, err := client.Read(header[:]); err == nil {
		t.Fatal("DNS responder remained writable after disable")
	}
}

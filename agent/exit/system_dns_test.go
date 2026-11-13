package exit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/acl"
	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
)

type exitDNSPipeStream struct {
	net.Conn
}

func (s *exitDNSPipeStream) CloseWrite() error { return nil }

func systemDNSQuestion(t *testing.T, kind dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName("example.com.")
	if err != nil {
		t.Fatal(err)
	}
	question := dnsmessage.Message{Header: dnsmessage.Header{ID: 144, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: kind, Class: dnsmessage.ClassINET}}}
	wire, err := question.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestExitSystemDNSResolvesAAndAAAAWithoutOpenProxyPort(t *testing.T) {
	for _, tc := range []struct {
		kind    dnsmessage.Type
		network string
		ip      net.IP
	}{
		{dnsmessage.TypeA, "ip4", net.ParseIP("203.0.113.23")},
		{dnsmessage.TypeAAAA, "ip6", net.ParseIP("2001:db8::23")},
	} {
		calls := 0
		h := NewHandler(HandlerConfig{SystemDNSLookup: func(_ context.Context, network, host string) ([]net.IP, error) {
			calls++
			if network != tc.network || host != "example.com" {
				t.Errorf("unexpected lookup %s %s", network, host)
			}
			return []net.IP{tc.ip}, nil
		}})
		q := systemDNSQuestion(t, tc.kind)
		resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
		if err != nil || !resp.Success || calls != 1 {
			t.Fatalf("exit DNS failed: %+v err=%v calls=%d", resp, err, calls)
		}
		ips, err := protocol.ValidateExitDNSAnswer(q, resp.Answer)
		if err != nil || len(ips) != 1 || !ips[0].Equal(tc.ip) {
			t.Fatalf("unexpected DNS answer: ips=%v err=%v", ips, err)
		}
	}
}

func TestExitSystemDNSDoesNotResolveUnsupportedOrOversizedQueries(t *testing.T) {
	used := false
	h := NewHandler(HandlerConfig{SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		used = true
		return nil, errors.New("must not run")
	}})
	for _, q := range [][]byte{systemDNSQuestion(t, dnsmessage.TypeTXT), make([]byte, protocol.MaxExitDNSQuestionBytes+1)} {
		resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
		if err == nil || resp.Success || used {
			t.Fatalf("invalid query contacted resolver: err=%v resp=%+v", err, resp)
		}
	}
}

func TestExitSystemDNSRespectsExitACLBeforeAndAfterLookup(t *testing.T) {
	policy := acl.Policy{ID: "exit-dns-policy", AllowInternet: true, AllowPrivateNetwork: false, AllowLoopback: false}
	checker, err := acl.NewChecker(policy)
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	h := NewHandler(HandlerConfig{ACLChecker: checker, SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		lookups++
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}})
	q := systemDNSQuestion(t, dnsmessage.TypeA)
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
	if err == nil || resp.Success || lookups != 1 {
		t.Fatalf("denied address disclosed or lookup not called: resp=%+v err=%v lookups=%d", resp, err, lookups)
	}
}

func TestExitSystemDNSResolverFailuresDoNotFallback(t *testing.T) {
	calls := 0
	h := NewHandler(HandlerConfig{SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		calls++
		return nil, errors.New("offline")
	}})
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: systemDNSQuestion(t, dnsmessage.TypeA)})
	if err == nil || resp.Success || calls != 1 {
		t.Fatalf("failure not closed: resp=%+v err=%v calls=%d", resp, err, calls)
	}
}

func TestExitSystemDNSBlocksRecursiveDNSCapture(t *testing.T) {
	called := false
	h := NewHandler(HandlerConfig{
		SystemDNSAllowed: func() bool { return false },
		SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
			called = true
			return []net.IP{net.ParseIP("203.0.113.23")}, nil
		},
	})
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{
		RequestID: "dns-guard", Question: systemDNSQuestion(t, dnsmessage.TypeA),
	})
	if err == nil || resp.Success || called {
		t.Fatalf("recursive DNS capture guard failed: resp=%+v err=%v called=%t", resp, err, called)
	}
}

func TestSecureRelayAcceptsAuthenticatedDirectP2POrigin(t *testing.T) {
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	exitPublic, exitPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const clientID, exitID = "client-device", "exit-device"
	clientIdentity := e2ee.Identity{DeviceID: clientID, PublicKey: clientPublic, PrivateKey: clientPrivate}
	exitIdentity := e2ee.Identity{DeviceID: exitID, PublicKey: exitPublic, PrivateKey: exitPrivate}

	checker, err := acl.NewChecker(acl.Policy{ID: "exit-policy", AllowInternet: true, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(HandlerConfig{
		ACLChecker: checker,
		SystemDNSAllowed: func() bool { return true },
		SystemDNSLookup: func(_ context.Context, network, host string) ([]net.IP, error) {
			if network != "ip4" || host != "example.com" {
				t.Fatalf("unexpected direct P2P DNS lookup: %s %s", network, host)
			}
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		},
		E2EEPublicKey: exitPublic, E2EEPrivateKey: exitPrivate,
	})
	defer handler.Close()

	clientConn, exitConn := net.Pipe()
	clientStream, exitStream := &exitDNSPipeStream{clientConn}, &exitDNSPipeStream{exitConn}
	defer clientConn.Close()
	defer exitConn.Close()
	_ = clientStream.SetDeadline(time.Now().Add(5 * time.Second))
	_ = exitStream.SetDeadline(time.Now().Add(5 * time.Second))

	policy := &acl.Policy{ID: "server-bound-p2p-policy", AllowInternet: true, AllowLoopback: true}
	exitCtx := WithDirectP2POrigin(BindRelayPolicy(context.Background(), policy), clientID, exitID)
	handlerDone := make(chan error, 1)
	go func() {
		outer, err := protocol.ReadStreamHeader(exitStream)
		if err != nil {
			handlerDone <- err
			return
		}
		handler.HandleStreamWithHeader(exitCtx, exitStream, outer)
		handlerDone <- nil
	}()

	if err := protocol.WriteStreamHeader(clientStream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeSecureRelay,
		RequestID: "p2p-e2ee-dns", ClientDeviceID: clientID, ExitDeviceID: exitID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(clientStream, protocol.SecureRelayMetadata{
		Version: protocol.SecureRelayVersion, ClientPublicKey: clientPublic,
	}); err != nil {
		t.Fatal(err)
	}
	secureClient, err := e2ee.ClientHandshake(clientStream, e2ee.ClientConfig{
		Local: clientIdentity, ExitDeviceID: exitID, ExitPublicKey: exitPublic,
	})
	if err != nil {
		t.Fatalf("direct P2P E2EE handshake: %v", err)
	}
	query := systemDNSQuestion(t, dnsmessage.TypeA)
	if err := protocol.WriteStreamHeader(secureClient, &protocol.StreamHeader{
		Type: protocol.FrameTypeExitSystemDNS, RequestID: "dns-p2p", ExitDeviceID: exitID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(secureClient, protocol.ExitDNSRequest{RequestID: "dns-p2p", Question: query}); err != nil {
		t.Fatal(err)
	}
	var response protocol.ExitDNSResponse
	if err := protocol.ReadJSON(secureClient, &response); err != nil {
		t.Fatalf("read direct P2P encrypted DNS response: %v", err)
	}
	if !response.Success || response.RequestID != "dns-p2p" {
		t.Fatalf("direct P2P encrypted DNS failed: %+v", response)
	}
	ips, err := protocol.ValidateExitDNSAnswer(query, response.Answer)
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("8.8.8.8")) {
		t.Fatalf("invalid direct P2P DNS answer: ips=%v err=%v", ips, err)
	}
	_ = secureClient.Close()
	if err := <-handlerDone; err != nil {
		t.Fatalf("direct P2P Exit handler: %v", err)
	}
}

func TestDirectP2POriginBindsAuthenticatedDevicePair(t *testing.T) {
	ctx := WithDirectP2POrigin(context.Background(), "client-a", "exit-b")
	clientID, exitID, ok := directP2POriginIDs(ctx)
	if !ok || clientID != "client-a" || exitID != "exit-b" {
		t.Fatalf("direct P2P origin identity = %q -> %q (%v)", clientID, exitID, ok)
	}
	ctx = WithDirectP2POrigin(context.Background(), "", "exit-b")
	if _, _, ok := directP2POriginIDs(ctx); ok {
		t.Fatal("direct P2P origin accepted an empty authenticated Client ID")
	}
}

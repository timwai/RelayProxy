package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type systemDNSConn struct {
	net.Conn
}

func (c *systemDNSConn) CloseWrite() error { return nil }

type systemDNSTestSession struct {
	*scriptedSession
	supported bool
}

func (s *systemDNSTestSession) PeerSupportsExitSystemDNS() bool { return s.supported }

func clientDNSQuestion(t *testing.T) []byte {
	t.Helper()
	msg := dnsmessage.Message{Header: dnsmessage.Header{ID: 17, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("internal.example."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestSystemDNSRequiredE2EEUsesAuthenticatedSecureRelay(t *testing.T) {
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	exitPublic, exitPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const clientID, exitID = "client-device", "office-exit"
	clientIdentity := e2ee.Identity{DeviceID: clientID, PublicKey: clientPublic, PrivateKey: clientPrivate}
	exitIdentity := e2ee.Identity{DeviceID: exitID, PublicKey: exitPublic, PrivateKey: exitPrivate}

	clientConn, exitConn := net.Pipe()
	clientStream := &systemDNSConn{Conn: clientConn}
	exitStream := &systemDNSConn{Conn: exitConn}
	defer clientConn.Close()
	defer exitConn.Close()

	sess := &systemDNSTestSession{
		scriptedSession: newScriptedSession(func() tunnel.TunnelStream { return clientStream }),
		supported:       true,
	}
	d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, func() string { return clientID })
	d.ConfigureE2EE(clientIdentity, func(id string) ([]byte, bool) {
		return append([]byte(nil), exitPublic...), id == exitID
	}, func(id string, key []byte) error {
		if id != exitID || !bytes.Equal(key, exitPublic) {
			return errors.New("unexpected E2EE Exit key")
		}
		return nil
	})
	d.SetE2EEServerSupported(true)
	d.ConfigureE2EEMode("required")

	serverDone := make(chan error, 1)
	go func() {
		outer, err := protocol.ReadStreamHeader(exitStream)
		if err != nil {
			serverDone <- err
			return
		}
		if outer.Type != protocol.FrameTypeSecureRelay || outer.ClientDeviceID != clientID || outer.ExitDeviceID != exitID {
			serverDone <- errors.New("Client did not open an authenticated SecureRelay stream")
			return
		}
		var metadata protocol.SecureRelayMetadata
		if err := protocol.ReadJSON(exitStream, &metadata); err != nil {
			serverDone <- err
			return
		}
		if metadata.Version != protocol.SecureRelayVersion || !bytes.Equal(metadata.ClientPublicKey, clientPublic) {
			serverDone <- errors.New("SecureRelay metadata did not bind the authenticated Client key")
			return
		}
		secure, err := e2ee.ServerHandshake(exitStream, e2ee.ServerConfig{
			Local: exitIdentity, ClientDeviceID: clientID, ClientPublicKey: clientPublic,
		})
		if err != nil {
			serverDone <- err
			return
		}
		inner, err := protocol.ReadStreamHeader(secure)
		if err != nil {
			serverDone <- err
			return
		}
		if inner.Type != protocol.FrameTypeExitSystemDNS || inner.ExitDeviceID != exitID {
			serverDone <- errors.New("Exit system DNS request was not inside E2EE")
			return
		}
		var request protocol.ExitDNSRequest
		if err := protocol.ReadJSON(secure, &request); err != nil {
			serverDone <- err
			return
		}
		question, _, err := protocol.ValidateExitDNSQuestion(request.Question)
		if err != nil {
			serverDone <- err
			return
		}
		answer, err := (&dnsmessage.Message{
			Header: dnsmessage.Header{ID: question.ID, Response: true},
			Questions: question.Questions,
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: question.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30},
				Body: &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}},
			}},
		}).Pack()
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- protocol.WriteJSON(secure, protocol.ExitDNSResponse{
			RequestID: request.RequestID, Success: true, Answer: answer,
		})
	}()

	query := clientDNSQuestion(t)
	got, err := d.ExchangeExitSystemDNS(context.Background(), exitID, query)
	if err != nil {
		t.Fatalf("required E2EE Exit system DNS exchange failed: %v", err)
	}
	if _, err := protocol.ValidateExitDNSAnswer(query, got); err != nil {
		t.Fatalf("received invalid E2EE DNS answer: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("Exit-side E2EE DNS handler: %v", err)
	}
	if sess.opens.Load() != 1 {
		t.Fatalf("required E2EE opened %d Relay streams, want 1", sess.opens.Load())
	}
}

func TestSystemDNSRequiredE2EERejectsLegacyRelayBeforeOpeningStream(t *testing.T) {
	stream := &scriptedStream{}
	sess := &systemDNSTestSession{
		scriptedSession: newScriptedSession(func() tunnel.TunnelStream { return stream }),
		supported:       true,
	}
	d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
	d.ConfigureE2EEMode("required")

	_, err := d.ExchangeExitSystemDNS(context.Background(), "office-exit", clientDNSQuestion(t))
	if err == nil || !strings.Contains(err.Error(), "E2EE is required") {
		t.Fatalf("required E2EE allowed legacy Exit system DNS: %v", err)
	}
	if sess.opens.Load() != 0 || stream.writes.Load() != 0 {
		t.Fatalf("legacy DNS stream was opened or written: opens=%d writes=%d", sess.opens.Load(), stream.writes.Load())
	}
}

func TestSystemDNSRejectsOldRelayWithoutSendingQuery(t *testing.T) {
	stream := &scriptedStream{}
	sess := &systemDNSTestSession{scriptedSession: newScriptedSession(func() tunnel.TunnelStream { return stream })}
	d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
	_, err := d.ExchangeExitSystemDNS(context.Background(), "office-exit", clientDNSQuestion(t))
	var relayErr *protocol.RelayError
	if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeDNSUnsupported || !strings.Contains(err.Error(), "升级 Relay Server") {
		t.Fatalf("missing old relay diagnosis: %v", err)
	}
	if stream.writes.Load() != 0 || !stream.closed.Load() {
		t.Fatalf("query sent to unsupported relay: writes=%d closed=%v", stream.writes.Load(), stream.closed.Load())
	}
}

func TestSystemDNSClosedStreamShowsSelectedExitAndPreservesEOF(t *testing.T) {
	for _, direct := range []bool{false, true} {
		sess := &systemDNSTestSession{scriptedSession: newScriptedSession(func() tunnel.TunnelStream { return &scriptedStream{} }), supported: !direct}
		d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
		if direct {
			d.ConfigurePathProvider(func(string) (SelectedSession, bool) {
				return SelectedSession{Session: sess, Path: protocol.ProxyPathP2PQUIC}, true
			}, nil)
		}
		_, err := d.ExchangeExitSystemDNS(context.Background(), "office-exit", clientDNSQuestion(t))
		if !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "office-exit") || !strings.Contains(err.Error(), "出口 Agent 已升级") {
			t.Fatalf("direct=%v missing EOF diagnosis: %v", direct, err)
		}
	}
}

func TestSystemDNSReadsAnswersAndPreservesRemoteErrors(t *testing.T) {
	query := clientDNSQuestion(t)
	msg, _, err := protocol.ValidateExitDNSQuestion(query)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true}, Questions: msg.Questions, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Class: dnsmessage.ClassINET, Type: dnsmessage.TypeA, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{10, 20, 30, 40}}}}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", protocol.ErrCodeDNSUnsupported, protocol.ErrCodeACLDenied} {
		response := protocol.ExitDNSResponse{RequestID: "dns_1", Success: code == "", ErrorCode: code, ErrorMessage: "upgrade exit or check ACL"}
		if code == "" {
			response.Answer = answer
		}
		stream := responseStream(response)
		sess := &systemDNSTestSession{scriptedSession: newScriptedSession(func() tunnel.TunnelStream { return stream }), supported: true}
		d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
		got, err := d.ExchangeExitSystemDNS(context.Background(), "office-exit", query)
		if code == "" {
			if err != nil || string(got) != string(answer) {
				t.Fatalf("lost system DNS answer: %v", err)
			}
		} else {
			var relayErr *protocol.RelayError
			if !errors.As(err, &relayErr) || relayErr.Code != code || relayErr.Message != response.ErrorMessage {
				t.Fatalf("lost remote error %s: %v", code, err)
			}
		}
		if sess.opens.Load() != 1 {
			t.Fatalf("unexpected DNS fallback: %d streams", sess.opens.Load())
		}
	}
}

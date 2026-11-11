package client

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

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

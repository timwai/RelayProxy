package direct

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type authRaceSession struct {
	once      sync.Once
	done      chan struct{}
	stream    *registrationStream
	openCount atomic.Int32
}

func newAuthRaceSession(t *testing.T, success bool) *authRaceSession {
	t.Helper()
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectHandshakeResponse{Success: success}); err != nil {
		t.Fatal(err)
	}
	return &authRaceSession{done: make(chan struct{}), stream: stream}
}

func (s *authRaceSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	s.openCount.Add(1)
	return s.stream, nil
}

func (s *authRaceSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, net.ErrClosed
}

func (s *authRaceSession) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *authRaceSession) RemoteAddr() net.Addr            { return &net.UDPAddr{} }
func (s *authRaceSession) LocalAddr() net.Addr             { return &net.UDPAddr{} }

func (s *authRaceSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *authRaceSession) Done() <-chan struct{} { return s.done }

func TestDialAnyAuthenticatesOnlyTransportWinner(t *testing.T) {
	winner := newAuthRaceSession(t, true)
	loser := newAuthRaceSession(t, true)
	previous := dialPublicDirectQUIC
	defer func() { dialPublicDirectQUIC = previous }()

	loserReturned := make(chan struct{})
	dialPublicDirectQUIC = func(ctx context.Context, address string, _ *tls.Config, _ *quic.Config) (tunnel.TunnelSession, error) {
		if address == "winner.example:35820" {
			return winner, nil
		}
		// The preferred endpoint starts first but completes after the fallback
		// endpoint's head-start delay, so the fallback wins the transport race.
		time.Sleep(250 * time.Millisecond)
		close(loserReturned)
		return loser, nil
	}

	ticket := []byte("single-use-ticket")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, address, err := DialAny(ctx, []DialConfig{
		{Address: "[2001:4860:4860::8888]:35820", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: ticket},
		{Address: "winner.example:35820", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: ticket},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session != winner || address != "winner.example:35820" {
		t.Fatalf("winner session=%T address=%q", session, address)
	}
	if winner.openCount.Load() != 1 {
		t.Fatalf("winner auth stream count=%d, want 1", winner.openCount.Load())
	}
	var request protocol.PublicDirectHandshakeRequest
	if err := protocol.ReadJSON(&winner.stream.write, &request); err != nil {
		t.Fatal(err)
	}
	if request.Auth == nil || string(request.Auth.Ticket) != string(ticket) {
		t.Fatalf("winner auth request=%+v", request)
	}

	select {
	case <-loserReturned:
	case <-time.After(time.Second):
		t.Fatal("loser transport dial did not finish")
	}
	select {
	case <-loser.Done():
	case <-time.After(time.Second):
		t.Fatal("loser transport session was not closed after race cancellation")
	}
	if loser.openCount.Load() != 0 {
		t.Fatalf("loser unexpectedly consumed ticket on %d auth stream(s)", loser.openCount.Load())
	}
}

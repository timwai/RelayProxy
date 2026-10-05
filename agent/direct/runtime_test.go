package direct

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type runtimeRegistrationSession struct {
	mu      sync.Mutex
	streams []*registrationStream
	done    chan struct{}
}

func (s *runtimeRegistrationSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectRegistrationResponse{Success: true}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.streams = append(s.streams, stream)
	s.mu.Unlock()
	return stream, nil
}

func (s *runtimeRegistrationSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *runtimeRegistrationSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *runtimeRegistrationSession) RemoteAddr() net.Addr            { return &net.TCPAddr{} }
func (s *runtimeRegistrationSession) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (s *runtimeRegistrationSession) Close() error                    { return nil }
func (s *runtimeRegistrationSession) Done() <-chan struct{}           { return s.done }

func (s *runtimeRegistrationSession) requests(t *testing.T) []protocol.PublicDirectRegistrationRequest {
	t.Helper()
	s.mu.Lock()
	streams := append([]*registrationStream(nil), s.streams...)
	s.mu.Unlock()
	out := make([]protocol.PublicDirectRegistrationRequest, 0, len(streams))
	for _, stream := range streams {
		if _, err := protocol.ReadStreamHeader(&stream.write); err != nil {
			t.Fatal(err)
		}
		var request protocol.PublicDirectRegistrationRequest
		if err := protocol.ReadJSON(&stream.write, &request); err != nil {
			t.Fatal(err)
		}
		out = append(out, request)
	}
	return out
}

func TestExitRuntimeReregistersAfterNetworkChange(t *testing.T) {
	session := &runtimeRegistrationSession{done: make(chan struct{})}
	runtime := &ExitRuntime{
		networkDone:  make(chan struct{}),
		listenPort:   35820,
		networkEpoch: 1,
	}
	var signatureCalls atomic.Int32
	signature := func() string {
		if signatureCalls.Add(1) == 1 {
			return "network-a"
		}
		return "network-b"
	}
	ctx, cancel := context.WithCancel(context.Background())
	go runtime.watchNetwork(ctx, session, "sha256:0000000000000000000000000000000000000000000000000000000000000000", ExitRuntimeOptions{
		ManualAdvertise:      "exit.example.com:35820",
		RegisterTimeout:      100 * time.Millisecond,
		NetworkCheckInterval: 5 * time.Millisecond,
		NetworkSignature:     signature,
	})
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		count := len(session.streams)
		session.mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-runtime.networkDone
			t.Fatal("network change did not trigger Public Direct re-registration")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-runtime.networkDone

	requests := session.requests(t)
	if len(requests) != 1 {
		t.Fatalf("registration count=%d, want 1", len(requests))
	}
	if requests[0].NetworkEpoch != 2 {
		t.Fatalf("network epoch=%d, want 2", requests[0].NetworkEpoch)
	}
	foundManual := false
	for _, candidate := range requests[0].Candidates {
		if candidate.Source == protocol.PublicDirectEndpointManual &&
			candidate.Address == "exit.example.com:35820" {
			foundManual = true
			break
		}
	}
	if !foundManual {
		t.Fatalf("manual advertise candidate missing: %+v", requests[0].Candidates)
	}
	if runtime.networkEpoch != 2 {
		t.Fatalf("runtime network epoch=%d, want 2", runtime.networkEpoch)
	}
}

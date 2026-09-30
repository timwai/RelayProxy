package p2p

import (
	"context"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func testDescription(address, fingerprint string) LocalDescription {
	return func() ([]protocol.P2PCandidate, string, error) {
		return []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: address, Priority: 100}}, fingerprint, nil
	}
}

func TestManagersExchangeOfferAndAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token := []byte("0123456789abcdef0123456789abcdef")
	var mu sync.Mutex
	var exitAnswer protocol.P2PControlMessage

	exit := NewManager(ctx, func(_ context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		mu.Lock()
		exitAnswer = message
		mu.Unlock()
		return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID, LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli()}, nil
	}, testDescription("192.0.2.20:52000", "sha256:exit"), time.Minute)
	defer exit.Close()

	client := NewManager(ctx, func(_ context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		if message.Type != protocol.P2PControlConnectRequest {
			t.Fatalf("unexpected client request: %#v", message)
		}
		return protocol.P2PControlMessage{
			Type: protocol.P2PControlLeaseAck, SessionID: 42, ClientDeviceID: "client", ExitDeviceID: "exit",
			SessionToken: token, LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer client.Close()

	clientSession, err := client.StartClient(ctx, "exit")
	if err != nil {
		t.Fatal(err)
	}
	exit.HandleControl(protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectOffer, SessionID: 42, ClientDeviceID: "client", ExitDeviceID: "exit",
		SessionToken: token, Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:51000"}},
		PeerFingerprint: "sha256:client", LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
	})

	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		answer := exitAnswer
		mu.Unlock()
		if answer.Type == protocol.P2PControlConnectAnswer {
			client.HandleControl(protocol.P2PControlMessage{
				Type: protocol.P2PControlConnectAnswer, SessionID: answer.SessionID,
				ClientDeviceID: answer.ClientDeviceID, ExitDeviceID: answer.ExitDeviceID,
				SessionToken: token, Candidates: answer.Candidates, PeerFingerprint: "sha256:exit",
				LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
			})
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exit did not produce a connect answer")
		}
		time.Sleep(time.Millisecond)
	}
	snapshot := clientSession.Snapshot()
	if snapshot.State != StateRendezvous || snapshot.PeerFingerprint != "sha256:exit" || len(snapshot.PeerCandidates) != 1 {
		t.Fatalf("client signaling session not ready: %#v", snapshot)
	}
	exitSession, ok := exit.Session(42)
	if !ok || exitSession == nil {
		t.Fatal("exit signaling session was not created")
	}
	exitSnapshot := exitSession.Snapshot()
	if exitSnapshot.State != StateRendezvous || exitSnapshot.PeerFingerprint != "sha256:client" {
		t.Fatalf("exit signaling session not ready: %#v", exitSnapshot)
	}
}

func TestRevokeClosesLocalSession(t *testing.T) {
	ctx := context.Background()
	token := []byte("0123456789abcdef0123456789abcdef")
	manager := NewManager(ctx, func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: 7, SessionToken: token}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer manager.Close()
	item := manager.newSession(7, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	if item == nil {
		t.Fatal("failed to create session")
	}
	manager.HandleControl(protocol.P2PControlMessage{Type: protocol.P2PControlRevoke, SessionID: 7})
	if _, ok := manager.Session(7); ok {
		t.Fatal("revoked session remains registered")
	}
	if item.Snapshot().State != StateClosed {
		t.Fatalf("revoked session state=%s", item.Snapshot().State)
	}
}


func TestReadyForExitIgnoresSignalingOnlySession(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	item := manager.newSession(88, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	if item == nil {
		t.Fatal("failed to create signaling session")
	}
	item.setState(StateRendezvous, "")
	if _, ok := manager.ReadyForExit("exit"); ok {
		t.Fatal("signaling-only session was exposed as a ready direct tunnel")
	}
}

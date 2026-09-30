package p2p

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/acl"
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

func TestEnsureClientDeduplicatesInFlightAttempt(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(context.Background(), func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		calls.Add(1)
		return protocol.P2PControlMessage{
			Type: protocol.P2PControlLeaseAck, SessionID: 91, ClientDeviceID: "client", ExitDeviceID: "exit",
			SessionToken: []byte("0123456789abcdef0123456789abcdef"), LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer manager.Close()

	manager.EnsureClient("exit")
	manager.EnsureClient("exit")
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("connect attempts=%d, want 1", got)
	}
}

func TestValidateRelayPolicyRequiresServerFingerprint(t *testing.T) {
	if _, err := validateRelayPolicy(&acl.Policy{AllowInternet: true}); err == nil {
		t.Fatal("Relay policy without fingerprint was accepted")
	}
	checker, err := acl.NewChecker(acl.Policy{
		ID: "relay_acl", AllowInternet: true,
		AccessMode: acl.AccessModeDeny, AccessHosts: []string{"blocked.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := checker.Policy()
	validated, err := validateRelayPolicy(&policy)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Fingerprint != policy.Fingerprint || len(validated.AccessHosts) != 1 || validated.AccessHosts[0] != "blocked.example" {
		t.Fatalf("unexpected validated policy: %#v", validated)
	}
	tampered := policy
	tampered.AccessHosts = []string{"other.example"}
	if _, err := validateRelayPolicy(&tampered); err == nil {
		t.Fatal("tampered Relay policy was accepted")
	}
}


func TestPathStatusPrefersReadyAndHidesSensitiveDetails(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	degraded := manager.newSession(101, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	ready := manager.newSession(102, "client", "exit", token, time.Now().Add(2*time.Minute).UnixMilli())
	if degraded == nil || ready == nil {
		t.Fatal("failed to create test sessions")
	}
	degraded.setState(StateDegraded, "punch timeout")
	ready.setState(StateReady, "")
	ready.setPeer([]protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:1234"}}, "sha256:peer")

	status, ok := manager.PathStatus("exit")
	if !ok {
		t.Fatal("missing path status")
	}
	if status.SessionID != 102 || status.State != StateReady || status.Path != protocol.P2PPathDirectQUIC {
		t.Fatalf("unexpected path status: %#v", status)
	}
	if status.Error != "" {
		t.Fatalf("ready path exposed unexpected error: %q", status.Error)
	}
}

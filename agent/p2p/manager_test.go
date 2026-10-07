package p2p

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestEnsureClientIgnoresServerExit(t *testing.T) {
	called := make(chan struct{}, 1)
	release := make(chan struct{})
	manager := NewManager(context.Background(), func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		called <- struct{}{}
		<-release
		return protocol.P2PControlMessage{}, errors.New("server exit must not enter P2P signaling")
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer manager.Close()
	defer close(release)

	manager.EnsureClient(protocol.ServerExitDeviceID)

	select {
	case <-called:
		t.Fatal("server exit triggered P2P signaling")
	default:
	}
	manager.mu.Lock()
	_, starting := manager.starting[protocol.ServerExitDeviceID]
	_, coolingDown := manager.cooldowns[protocol.ServerExitDeviceID]
	manager.mu.Unlock()
	if starting || coolingDown {
		t.Fatalf("server exit entered P2P state: starting=%v cooldown=%v", starting, coolingDown)
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

func TestRelayPolicyCopiesPreserveFingerprintWithEmptySlices(t *testing.T) {
	checker, err := acl.NewChecker(acl.Policy{
		ID: "relay_acl", AllowInternet: true,
		Rules: []acl.Rule{}, AccessHosts: []string{}, AccessCIDRs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := checker.Policy()
	validated, err := validateRelayPolicy(&policy)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Rules == nil || validated.AccessHosts == nil || validated.AccessCIDRs == nil {
		t.Fatalf("validated policy lost empty slices: %#v", validated)
	}

	session := &Session{}
	session.setRelayPolicy(validated)
	copied := session.RelayPolicy()
	if copied == nil {
		t.Fatal("session relay policy copy is missing")
	}
	if copied.Rules == nil || copied.AccessHosts == nil || copied.AccessCIDRs == nil {
		t.Fatalf("session policy copy lost empty slices: %#v", copied)
	}
	verified, err := acl.NewChecker(*copied)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Policy().Fingerprint != copied.Fingerprint {
		t.Fatalf("relay policy fingerprint changed during session copy: %q != %q", verified.Policy().Fingerprint, copied.Fingerprint)
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

func TestCooldownSuppressesRepeatedClientAttempts(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(context.Background(), func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		calls.Add(1)
		return protocol.P2PControlMessage{}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer manager.Close()

	manager.recordFailure("exit", "punch timeout")
	manager.EnsureClient("exit")
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("cooldown allowed %d connect attempt(s)", calls.Load())
	}
	status, ok := manager.PathStatus("exit")
	if !ok || status.State != StateCooldown || status.Error != "punch timeout" {
		t.Fatalf("unexpected cooldown status: %#v ok=%v", status, ok)
	}

	manager.mu.Lock()
	failure := manager.cooldowns["exit"]
	failure.until = time.Now().Add(-time.Millisecond)
	manager.cooldowns["exit"] = failure
	manager.mu.Unlock()
	manager.EnsureClient("exit")
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("expired cooldown attempts=%d, want 1", calls.Load())
	}
}

func TestClientSessionLimitEvictsLeastRecentlyUsed(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	manager.maxExitSessions = 2
	token := []byte("0123456789abcdef0123456789abcdef")
	for id := uint64(1); id <= 3; id++ {
		item := manager.newSession(id, "client", fmt.Sprintf("exit-%d", id), token, time.Now().Add(time.Minute).UnixMilli())
		if item == nil {
			t.Fatalf("failed to create session %d", id)
		}
		item.mu.Lock()
		item.clientRole = true
		item.state = StateReady
		item.mu.Unlock()
		item.lastUsed.Store(int64(id))
	}
	manager.enforceClientLimit(3)
	if _, ok := manager.Session(1); ok {
		t.Fatal("least recently used P2P session was not evicted")
	}
	if _, ok := manager.Session(2); !ok {
		t.Fatal("newer P2P session was evicted")
	}
	if _, ok := manager.Session(3); !ok {
		t.Fatal("kept P2P session was evicted")
	}
}

func TestFailedDirectSessionIsRemovedAndClosedOnServer(t *testing.T) {
	closed := make(chan protocol.P2PControlMessage, 1)
	manager := NewManager(context.Background(), func(_ context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		if message.Type == protocol.P2PControlClose {
			closed <- message
		}
		return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID}, nil
	}, nil, time.Minute)
	defer manager.Close()

	token := []byte("0123456789abcdef0123456789abcdef")
	item := manager.newSession(501, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	if item == nil {
		t.Fatal("failed to create session")
	}
	item.mu.Lock()
	item.clientRole = true
	item.mu.Unlock()

	item.failDirect(errors.New("punch timeout"))
	if _, ok := manager.Session(item.ID); ok {
		t.Fatal("failed P2P session remains registered")
	}
	select {
	case message := <-closed:
		if message.SessionID != item.ID || message.Reason != "punch timeout" {
			t.Fatalf("unexpected close message: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("server P2P close was not sent")
	}
	status, ok := manager.PathStatus("exit")
	if !ok || status.State != StateCooldown {
		t.Fatalf("failed client path did not enter cooldown: %#v ok=%v", status, ok)
	}
}

func TestCandidateSummaryIsCountOnly(t *testing.T) {
	summary := summarizeCandidates(
		[]protocol.P2PCandidate{
			{Protocol: "udp", Type: "lan", Address: "192.0.2.10:1000"},
			{Protocol: "udp", Type: "reflexive", Address: "198.51.100.10:2000"},
		},
		[]protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.20:3000"}},
	)
	if summary != "local:lan=1,reflexive=1;peer:lan=1,reflexive=0" {
		t.Fatalf("unexpected summary %q", summary)
	}
	if strings.Contains(summary, "192.0.2.10") || strings.Contains(summary, "198.51.100.10") {
		t.Fatalf("candidate address leaked into summary: %q", summary)
	}
}

func TestNetworkChangeInvalidatesSessionsAndCooldown(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	item := manager.newSession(700, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	if item == nil {
		t.Fatal("failed to create session")
	}
	item.mu.Lock()
	item.clientRole = true
	item.state = StateReady
	item.mu.Unlock()
	manager.recordFailure("exit", "old network failed")
	manager.mu.Lock()
	manager.starting["exit"] = 99
	manager.mu.Unlock()
	before := manager.networkEpoch.Load()

	manager.invalidateNetwork()

	if manager.networkEpoch.Load() != before+1 {
		t.Fatalf("network epoch=%d, want %d", manager.networkEpoch.Load(), before+1)
	}
	if _, ok := manager.Session(item.ID); ok {
		t.Fatal("network change kept stale P2P session")
	}
	manager.mu.Lock()
	cooldowns, starting := len(manager.cooldowns), len(manager.starting)
	manager.mu.Unlock()
	if cooldowns != 0 || starting != 0 {
		t.Fatalf("network change left cooldowns=%d starting=%d", cooldowns, starting)
	}
	select {
	case <-item.closed:
	default:
		t.Fatal("network change did not close stale session")
	}
}

func TestNetworkWatcherDetectsSignatureChange(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	var signature atomic.Value
	signature.Store("network-a")
	manager.networkSignature = func() string { return signature.Load().(string) }
	manager.networkCheckInterval = 5 * time.Millisecond
	manager.networkSig = "network-a"
	go manager.watchNetworkLoop()

	signature.Store("network-b")
	deadline := time.Now().Add(time.Second)
	for manager.networkEpoch.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.networkEpoch.Load() == 0 {
		t.Fatal("network signature change was not detected")
	}
}

func TestPowerConstrainedSuppressesPrewarmButAllowsReactiveAttempt(t *testing.T) {
	var calls atomic.Int32
	manager := NewManager(context.Background(), func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		calls.Add(1)
		return protocol.P2PControlMessage{
			Type: protocol.P2PControlLeaseAck, SessionID: 901, ClientDeviceID: "client", ExitDeviceID: "exit",
			SessionToken: []byte("0123456789abcdef0123456789abcdef"), LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	defer manager.Close()

	manager.SetPowerConstrained(true)
	manager.PrewarmClient("exit")
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("power-constrained prewarm made %d attempt(s)", calls.Load())
	}

	manager.EnsureClient("exit")
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("reactive P2P attempts=%d, want 1", calls.Load())
	}
}

func TestPowerConstrainedProfileShrinksSessionCache(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	manager.maxExitSessions = 4
	manager.lowPowerMaxSessions = 1
	manager.lowPowerIdleTimeout = 45 * time.Second

	token := []byte("0123456789abcdef0123456789abcdef")
	for id := uint64(1); id <= 3; id++ {
		item := manager.newSession(id, "client", fmt.Sprintf("exit-%d", id), token, time.Now().Add(time.Minute).UnixMilli())
		item.mu.Lock()
		item.clientRole = true
		item.state = StateReady
		item.mu.Unlock()
		item.lastUsed.Store(int64(id))
	}
	manager.SetPowerConstrained(true)

	manager.mu.Lock()
	remaining := len(manager.sessions)
	manager.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("low-power session cache=%d, want 1", remaining)
	}
	options := manager.directQUICOptions()
	if !options.DisableKeepAlive || options.MaxIdleTimeout != 45*time.Second {
		t.Fatalf("unexpected low-power QUIC options: %#v", options)
	}
}

func TestFailReadyForExitRemovesBrokenPathAndStartsCooldown(t *testing.T) {
	manager := NewManager(context.Background(), nil, nil, time.Minute)
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	item := manager.newSession(991, "client", "exit", token, time.Now().Add(time.Minute).UnixMilli())
	if item == nil {
		t.Fatal("failed to create test session")
	}
	item.mu.Lock()
	item.clientRole = true
	item.state = StateReady
	item.mu.Unlock()

	manager.FailReadyForExit("exit", "direct stream open failed")

	if _, ok := manager.Session(item.ID); ok {
		t.Fatal("broken READY session remains registered")
	}
	status, ok := manager.PathStatus("exit")
	if !ok || status.State != StateCooldown || status.Error != "direct stream open failed" {
		t.Fatalf("unexpected failed-path status: %#v ok=%v", status, ok)
	}
}


func TestP2PBrutalRatesFollowClientDirections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	token := []byte("0123456789abcdef0123456789abcdef")

	client := NewManager(ctx, func(_ context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		if message.Type != protocol.P2PControlConnectRequest {
			t.Fatalf("unexpected client request: %#v", message)
		}
		if message.BrutalUploadBPS != 12_500_000 || message.BrutalDownloadBPS != 50_000_000 {
			t.Fatalf("client request Brutal rates=%d/%d", message.BrutalUploadBPS, message.BrutalDownloadBPS)
		}
		return protocol.P2PControlMessage{
			Type: protocol.P2PControlLeaseAck, SessionID: 71, ClientDeviceID: "client", ExitDeviceID: "exit",
			SessionToken: token, LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
			BrutalUploadBPS: 12_500_000, BrutalDownloadBPS: 50_000_000,
		}, nil
	}, testDescription("192.0.2.10:51000", "sha256:client"), time.Minute)
	client.brutalUploadBPS = 12_500_000
	client.brutalDownloadBPS = 50_000_000
	defer client.Close()

	clientSession, err := client.StartClient(ctx, "exit")
	if err != nil {
		t.Fatal(err)
	}
	clientSession.mu.RLock()
	clientTx := clientSession.brutalTxBPS
	clientSession.mu.RUnlock()
	if clientTx != 12_500_000 {
		t.Fatalf("client P2P sender target=%d, want 12500000", clientTx)
	}

	exit := NewManager(ctx, func(_ context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
		return protocol.P2PControlMessage{
			Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID,
			LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		}, nil
	}, testDescription("192.0.2.20:52000", "sha256:exit"), time.Minute)
	defer exit.Close()
	exit.HandleControl(protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectOffer, SessionID: 72, ClientDeviceID: "client", ExitDeviceID: "exit",
		SessionToken: token, Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:51000"}},
		PeerFingerprint: "sha256:client", LeaseExpiresAt: time.Now().Add(time.Minute).UnixMilli(),
		BrutalUploadBPS: 12_500_000, BrutalDownloadBPS: 50_000_000,
	})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if exitSession, ok := exit.Session(72); ok {
			exitSession.mu.RLock()
			exitTx := exitSession.brutalTxBPS
			exitSession.mu.RUnlock()
			if exitTx != 50_000_000 {
				t.Fatalf("exit P2P sender target=%d, want 50000000", exitTx)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exit P2P session was not created")
}

package direct

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func testFingerprint() string {
	return "sha256:" + strings.Repeat("0", 64)
}

func TestRegistryPublishesOnlyVerifiedUnexpiredEndpoints(t *testing.T) {
	registry := NewRegistry()
	now := time.Unix(1000, 0)
	registry.now = func() time.Time { return now }

	records, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.8.8"), protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
		NetworkEpoch:    1,
		Candidates: []protocol.PublicDirectEndpointCandidate{
			{Protocol: "udp", Address: "[2606:4700:4700::1111]:35820", Source: "ipv6"},
			{Protocol: "udp", Address: "exit.example.com:35820", Source: "manual"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records=%d, want 3", len(records))
	}
	if got := registry.VerifiedEndpoints("exit"); len(got) != 0 {
		t.Fatalf("unverified endpoints published: %+v", got)
	}
	if !registry.MarkVerified("exit", "session-1", "8.8.8.8:35820", time.Minute) {
		t.Fatal("failed to mark observed endpoint verified")
	}
	got := registry.VerifiedEndpoints("exit")
	if len(got) != 1 || got[0].Address != "8.8.8.8:35820" || !got[0].Verified {
		t.Fatalf("verified endpoints=%+v", got)
	}

	now = now.Add(2 * time.Minute)
	if got := registry.VerifiedEndpoints("exit"); len(got) != 0 {
		t.Fatalf("expired endpoint still published: %+v", got)
	}
	snapshot := registry.Snapshot("exit")
	foundExpired := false
	for _, record := range snapshot {
		if record.Endpoint.Address == "8.8.8.8:35820" && record.State == StateExpired {
			foundExpired = true
		}
	}
	if !foundExpired {
		t.Fatalf("expired state missing: %+v", snapshot)
	}
}

func TestRegistryInvalidatesVerificationOnNetworkChange(t *testing.T) {
	registry := NewRegistry()
	request := protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
		NetworkEpoch:    7,
	}
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.4.4"), request); err != nil {
		t.Fatal(err)
	}
	if !registry.MarkVerified("exit", "session-1", "8.8.4.4:35820", time.Minute) {
		t.Fatal("mark verified failed")
	}
	if len(registry.VerifiedEndpoints("exit")) != 1 {
		t.Fatal("verified endpoint missing")
	}

	request.NetworkEpoch = 8
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.4.4"), request); err != nil {
		t.Fatal(err)
	}
	if got := registry.VerifiedEndpoints("exit"); len(got) != 0 {
		t.Fatalf("network change preserved verification: %+v", got)
	}
	record, ok := registry.Lookup("exit", "session-1", "8.8.4.4:35820")
	if !ok || record.State != StateUnknown {
		t.Fatalf("record after network change=%+v ok=%v", record, func TestRegistryRejectsStaleNetworkEpochRegistration(t *testing.T) {
	registry := NewRegistry()
	request := protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
		NetworkEpoch:    8,
	}
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.4.4"), request); err != nil {
		t.Fatal(err)
	}
	if !registry.MarkVerified("exit", "session-1", "8.8.4.4:35820", time.Minute) {
		t.Fatal("mark verified failed")
	}

	request.NetworkEpoch = 7
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("1.1.1.1"), request); err == nil {
		t.Fatal("stale network epoch registration was accepted")
	}
	record, ok := registry.Lookup("exit", "session-1", "8.8.4.4:35820")
	if !ok || record.NetworkEpoch != 8 || record.State != StateVerified {
		t.Fatalf("current registration was replaced by stale epoch: record=%+v ok=%v", record, ok)
	}
	if _, ok := registry.Lookup("exit", "session-1", "1.1.1.1:35820"); ok {
		t.Fatal("stale epoch published a replacement endpoint")
	}
}

func TestRegistryRejectsStaleVerificationAfterNetworkChange(t *testing.T) {
	registry := NewRegistry()
	request := protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
		NetworkEpoch:    1,
	}
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.4.4"), request); err != nil {
		t.Fatal(err)
	}
	old, ok := registry.Lookup("exit", "session-1", "8.8.4.4:35820")
	if !ok {
		t.Fatal("initial registration missing")
	}
	if !registry.markVerifyingRegistration(old) {
		t.Fatal("initial verification could not start")
	}

	request.NetworkEpoch = 2
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("8.8.4.4"), request); err != nil {
		t.Fatal(err)
	}
	if registry.markVerifiedRegistration(old, time.Minute) {
		t.Fatal("stale verification result marked the new network epoch verified")
	}
	if registry.markFailedRegistration(old, errors.New("old probe failed")) {
		t.Fatal("stale verification failure modified the new network epoch")
	}
	record, ok := registry.Lookup("exit", "session-1", "8.8.4.4:35820")
	if !ok || record.NetworkEpoch != 2 || record.State != StateUnknown || record.Endpoint.Verified {
		t.Fatalf("new network epoch was polluted by stale verification: record=%+v ok=%v", record, ok)
	}
}

ok)
	}
}

func TestRegistryRejectsNonPublicAgentCandidates(t *testing.T) {
	registry := NewRegistry()
	for _, candidate := range []protocol.PublicDirectEndpointCandidate{
		{Protocol: "udp", Address: "127.0.0.1:35820", Source: "manual"},
		{Protocol: "udp", Address: "10.0.0.1:35820", Source: "manual"},
		{Protocol: "udp", Address: "[::1]:35820", Source: "ipv6"},
		{Protocol: "udp", Address: "[fe80::1]:35820", Source: "ipv6"},
		{Protocol: "tcp", Address: "exit.example.com:35820", Source: "manual"},
		{Protocol: "udp", Address: "8.8.8.8:35820", Source: "observed"},
	} {
		_, err := registry.Register("exit", "session-1", netip.Addr{}, protocol.PublicDirectRegistrationRequest{
			CertFingerprint: testFingerprint(),
			Candidates:      []protocol.PublicDirectEndpointCandidate{candidate},
		})
		if err == nil {
			t.Fatalf("candidate unexpectedly accepted: %+v", candidate)
		}
	}
}

func TestRegistryRejectsStaleSessionUpdates(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Register("exit", "new-session", netip.MustParseAddr("1.1.1.1"), protocol.PublicDirectRegistrationRequest{
		ListenerPort: 35820, CertFingerprint: testFingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	if registry.MarkVerified("exit", "old-session", "1.1.1.1:35820", time.Minute) {
		t.Fatal("stale session marked endpoint verified")
	}
	registry.InvalidateSession("exit", "old-session")
	if _, ok := registry.Lookup("exit", "new-session", "1.1.1.1:35820"); !ok {
		t.Fatal("stale session invalidation removed current registration")
	}
	registry.InvalidateSession("exit", "new-session")
	if _, ok := registry.Lookup("exit", "new-session", "1.1.1.1:35820"); ok {
		t.Fatal("current session invalidation kept endpoint")
	}
}

func TestRegistryServerReconnectRequiresReverification(t *testing.T) {
	registry := NewRegistry()
	request := protocol.PublicDirectRegistrationRequest{
		ListenerPort: 35820, CertFingerprint: testFingerprint(), NetworkEpoch: 1,
	}
	if _, err := registry.Register("exit", "session-1", netip.MustParseAddr("1.1.1.1"), request); err != nil {
		t.Fatal(err)
	}
	if !registry.MarkVerified("exit", "session-1", "1.1.1.1:35820", time.Minute) {
		t.Fatal("initial endpoint verification failed")
	}
	if got := registry.VerifiedEndpoints("exit"); len(got) != 1 {
		t.Fatalf("initial verified endpoints=%+v", got)
	}

	if _, err := registry.Register("exit", "session-2", netip.MustParseAddr("1.1.1.1"), request); err != nil {
		t.Fatal(err)
	}
	if got := registry.VerifiedEndpoints("exit"); len(got) != 0 {
		t.Fatalf("server reconnect reused verification from previous session: %+v", got)
	}
	if registry.MarkVerified("exit", "session-1", "1.1.1.1:35820", time.Minute) {
		t.Fatal("stale verification result from previous session was accepted")
	}
	if !registry.MarkVerified("exit", "session-2", "1.1.1.1:35820", time.Minute) {
		t.Fatal("new session could not verify endpoint")
	}
}

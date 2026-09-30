package p2p

import (
	"testing"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func newTestDevice(id, owner string, grants ...string) *session.DeviceSession {
	return &session.DeviceSession{
		DeviceID: id, OwnerUserID: owner, Grants: grants,
		Capabilities: []string{protocol.CapabilityProxyP2P},
	}
}

func TestCoordinatorOfferAnswerFlow(t *testing.T) {
	manager := session.NewManager()
	client := newTestDevice("client", "owner", protocol.CapabilityProxyClient)
	exit := newTestDevice("exit", "owner", protocol.CapabilityProxyExit)
	manager.Register(client)
	manager.Register(exit)

	c := NewCoordinator(manager, func(clientID, exitID string) (bool, error) {
		return clientID == "client" && exitID == "exit", nil
	}, time.Minute, "relay.example.com:3478", 8)

	type delivery struct {
		device string
		msg    protocol.P2PControlMessage
	}
	var deliveries []delivery
	c.send = func(device *session.DeviceSession, msg protocol.P2PControlMessage) error {
		deliveries = append(deliveries, delivery{device: device.DeviceID, msg: msg})
		return nil
	}

	ack := c.connect(client, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectRequest, ExitDeviceID: exit.DeviceID,
		CertFingerprint: "sha256:client",
		Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:52133", Priority: 100}},
	})
	if ack.Type != protocol.P2PControlLeaseAck || ack.SessionID == 0 || len(ack.SessionToken) != 32 {
		t.Fatalf("unexpected connect ack: %#v", ack)
	}
	if len(deliveries) != 1 || deliveries[0].device != exit.DeviceID || deliveries[0].msg.Type != protocol.P2PControlConnectOffer {
		t.Fatalf("offer was not delivered to exit: %#v", deliveries)
	}
	offer := deliveries[0].msg
	if offer.PeerFingerprint != "sha256:client" || offer.RendezvousAddress != "relay.example.com:3478" {
		t.Fatalf("unexpected offer: %#v", offer)
	}

	answerAck := c.answer(exit, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectAnswer, SessionID: offer.SessionID, SessionToken: offer.SessionToken,
		CertFingerprint: "sha256:exit",
		Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "reflexive", Address: "198.51.100.2:41001", Priority: 10}},
	})
	if answerAck.Type != protocol.P2PControlLeaseAck {
		t.Fatalf("unexpected answer ack: %#v", answerAck)
	}
	if len(deliveries) != 2 || deliveries[1].device != client.DeviceID || deliveries[1].msg.Type != protocol.P2PControlConnectAnswer {
		t.Fatalf("answer was not delivered to client: %#v", deliveries)
	}
	if deliveries[1].msg.PeerFingerprint != "sha256:exit" {
		t.Fatalf("exit fingerprint not forwarded: %#v", deliveries[1].msg)
	}
}

func TestCoordinatorRequiresBothP2PCapabilities(t *testing.T) {
	manager := session.NewManager()
	client := newTestDevice("client", "owner", protocol.CapabilityProxyClient)
	exit := newTestDevice("exit", "owner", protocol.CapabilityProxyExit)
	exit.Capabilities = nil
	manager.Register(client)
	manager.Register(exit)
	c := NewCoordinator(manager, func(string, string) (bool, error) { return true, nil }, time.Minute, "", 8)
	c.send = func(*session.DeviceSession, protocol.P2PControlMessage) error {
		t.Fatal("unsupported peer must not receive an offer")
		return nil
	}
	response := c.connect(client, protocol.P2PControlMessage{
		ExitDeviceID: "exit", CertFingerprint: "sha256:client",
		Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:1234"}},
	})
	if response.Type != protocol.P2PControlError || response.ErrorCode != "UNSUPPORTED" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestCoordinatorRevokesLeaseWhenAuthorizationChanges(t *testing.T) {
	manager := session.NewManager()
	client := newTestDevice("client", "owner", protocol.CapabilityProxyClient)
	exit := newTestDevice("exit", "owner", protocol.CapabilityProxyExit)
	manager.Register(client)
	manager.Register(exit)
	allowed := true
	c := NewCoordinator(manager, func(string, string) (bool, error) { return allowed, nil }, time.Minute, "", 8)

	var delivered []protocol.P2PControlMessage
	c.send = func(_ *session.DeviceSession, msg protocol.P2PControlMessage) error {
		delivered = append(delivered, msg)
		return nil
	}
	ack := c.connect(client, protocol.P2PControlMessage{
		ExitDeviceID: "exit", CertFingerprint: "sha256:client",
		Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:1234"}},
	})
	if ack.Type != protocol.P2PControlLeaseAck {
		t.Fatalf("connect failed: %#v", ack)
	}
	allowed = false
	renew := c.renew(client, protocol.P2PControlMessage{SessionID: ack.SessionID, SessionToken: ack.SessionToken})
	if renew.Type != protocol.P2PControlError {
		t.Fatalf("renew unexpectedly succeeded: %#v", renew)
	}
	if c.ActiveSessions() != 0 {
		t.Fatalf("revoked session still active: %d", c.ActiveSessions())
	}
	foundRevoke := false
	for _, msg := range delivered {
		if msg.Type == protocol.P2PControlRevoke && msg.SessionID == ack.SessionID {
			foundRevoke = true
		}
	}
	if !foundRevoke {
		t.Fatalf("revoke notification not delivered: %#v", delivered)
	}
}


func TestCoordinatorRejectsNonUDPCandidates(t *testing.T) {
	manager := session.NewManager()
	client := newTestDevice("client", "owner", protocol.CapabilityProxyClient)
	exit := newTestDevice("exit", "owner", protocol.CapabilityProxyExit)
	manager.Register(client)
	manager.Register(exit)
	c := NewCoordinator(manager, func(string, string) (bool, error) { return true, nil }, time.Minute, "", 8)
	c.send = func(*session.DeviceSession, protocol.P2PControlMessage) error {
		t.Fatal("invalid candidate must not be forwarded")
		return nil
	}
	response := c.connect(client, protocol.P2PControlMessage{
		ExitDeviceID: "exit", CertFingerprint: "sha256:client",
		Candidates: []protocol.P2PCandidate{{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:1234"}},
	})
	if response.Type != protocol.P2PControlError || response.ErrorCode != "INVALID_CANDIDATES" {
		t.Fatalf("unexpected response: %#v", response)
	}
}


func TestCoordinatorBindsRelayPolicyToExitOffer(t *testing.T) {
	manager := session.NewManager()
	client := newTestDevice("client", "owner", protocol.CapabilityProxyClient)
	exit := newTestDevice("exit", "owner", protocol.CapabilityProxyExit)
	manager.Register(client)
	manager.Register(exit)
	policy := acl.Policy{
		ID: "relay_acl", AllowInternet: true,
		AccessMode: acl.AccessModeDeny, AccessHosts: []string{"blocked.example"},
	}
	c := NewCoordinator(manager, func(string, string) (bool, error) { return true, nil }, time.Minute, "", 8, policy)
	var offer protocol.P2PControlMessage
	c.send = func(device *session.DeviceSession, msg protocol.P2PControlMessage) error {
		if device.DeviceID == "exit" {
			offer = msg
		}
		return nil
	}
	response := c.connect(client, protocol.P2PControlMessage{
		ExitDeviceID: "exit", CertFingerprint: "sha256:client",
		Candidates: []protocol.P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:1234"}},
	})
	if response.Type != protocol.P2PControlLeaseAck {
		t.Fatalf("connect failed: %#v", response)
	}
	if offer.Type != protocol.P2PControlConnectOffer || offer.RelayPolicy == nil {
		t.Fatalf("relay policy missing from exit offer: %#v", offer)
	}
	if offer.RelayPolicy.AccessMode != acl.AccessModeDeny || len(offer.RelayPolicy.AccessHosts) != 1 ||
		offer.RelayPolicy.AccessHosts[0] != "blocked.example" {
		t.Fatalf("unexpected relay policy: %#v", offer.RelayPolicy)
	}
}

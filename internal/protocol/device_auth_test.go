package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeviceAuthPayloadBindsChallengeAndIdentity(t *testing.T) {
	hello := DeviceHello{ProtocolVersion: DeviceProtocolVersion, InstallationID: "install", PublicKey: []byte("key"), ClientNonce: []byte("client")}
	challenge := AuthChallenge{ServerInstanceID: "server", ServerNonce: []byte("server-nonce")}
	base := DeviceAuthPayload(hello, challenge)
	challenge.ServerNonce = []byte("other")
	if bytes.Equal(base, DeviceAuthPayload(hello, challenge)) {
		t.Fatal("payload did not bind the server nonce")
	}
}

func TestDeviceAuthPayloadV4BindsAccessKeyAndCapabilities(t *testing.T) {
	hello := DeviceHello{
		ProtocolVersion: IdentityDeviceProtocolVersion,
		AccessKey:       "rpk_test_key",
		InstallationID:  "install", PublicKey: []byte("key"), ClientNonce: []byte("client"),
		RequestedCapabilities: []string{CapabilityProxyClient, CapabilityProxyExit},
		TransportCapabilities: []string{UDPModeStream},
	}
	challenge := AuthChallenge{ProtocolVersion: IdentityDeviceProtocolVersion, ServerInstanceID: "server", ServerNonce: []byte("server-nonce")}
	base := DeviceAuthPayload(hello, challenge)

	changedKey := hello
	changedKey.AccessKey = "rpk_other_key"
	if bytes.Equal(base, DeviceAuthPayload(changedKey, challenge)) {
		t.Fatal("v4 payload did not bind the identity access key")
	}
	changedCapability := hello
	changedCapability.RequestedCapabilities = []string{CapabilityProxyClient}
	if bytes.Equal(base, DeviceAuthPayload(changedCapability, challenge)) {
		t.Fatal("v4 payload did not bind requested capabilities")
	}
	changedTransport := hello
	changedTransport.TransportCapabilities = []string{UDPModeStream, UDPModeDatagram}
	if bytes.Equal(base, DeviceAuthPayload(changedTransport, challenge)) {
		t.Fatal("v4 payload did not bind transport capabilities")
	}
}

func TestDeviceAuthPayloadV3KeepsLegacyWireSemantics(t *testing.T) {
	hello := DeviceHello{
		ProtocolVersion: LegacyDeviceProtocolVersion,
		InstallationID:  "install", PublicKey: []byte("key"), ClientNonce: []byte("client"),
		RequestedCapabilities: []string{CapabilityProxyClient},
	}
	challenge := AuthChallenge{ProtocolVersion: LegacyDeviceProtocolVersion, ServerInstanceID: "server", ServerNonce: []byte("server-nonce")}
	base := DeviceAuthPayload(hello, challenge)
	hello.AccessKey = "ignored-by-v3"
	hello.RequestedCapabilities = append(hello.RequestedCapabilities, CapabilityProxyExit)
	if !bytes.Equal(base, DeviceAuthPayload(hello, challenge)) {
		t.Fatal("v3 payload changed when v4-only fields changed")
	}
}

func TestPongRDPTargetsDistinguishesRefreshFromNoUpdate(t *testing.T) {
	empty := []RDPTarget{}
	encoded, err := json.Marshal(PongMessage{Timestamp: 1, RDPTargets: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"rdpTargets":[]`) {
		t.Fatalf("empty refresh was omitted: %s", encoded)
	}
	var decoded PongMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RDPTargets == nil || len(*decoded.RDPTargets) != 0 {
		t.Fatalf("empty refresh did not round-trip: %+v", decoded.RDPTargets)
	}

	encoded, err = json.Marshal(PongMessage{Timestamp: 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "rdpTargets") {
		t.Fatalf("no-update pong unexpectedly included RDP targets: %s", encoded)
	}
}

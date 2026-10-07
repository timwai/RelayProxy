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

func TestDeviceAuthPayloadV5BindsIdentityIDAndCapabilities(t *testing.T) {
	hello := DeviceHello{
		ProtocolVersion: IdentityDeviceProtocolVersion,
		IdentityID:      "a1b2c3d4e5f6g7h8",
		InstallationID:  "install", PublicKey: []byte("key"), ClientNonce: []byte("client"),
		RequestedCapabilities: []string{CapabilityProxyClient, CapabilityProxyExit},
		TransportCapabilities: []string{UDPModeStream},
	}
	challenge := AuthChallenge{ProtocolVersion: IdentityDeviceProtocolVersion, ServerInstanceID: "server", ServerNonce: []byte("server-nonce")}
	base := DeviceAuthPayload(hello, challenge)

	changedIdentity := hello
	changedIdentity.IdentityID = "other-team"
	if bytes.Equal(base, DeviceAuthPayload(changedIdentity, challenge)) {
		t.Fatal("v5 payload did not bind the identity id")
	}
	changedCapability := hello
	changedCapability.RequestedCapabilities = []string{CapabilityProxyClient}
	if bytes.Equal(base, DeviceAuthPayload(changedCapability, challenge)) {
		t.Fatal("v5 payload did not bind requested capabilities")
	}
	changedTransport := hello
	changedTransport.TransportCapabilities = []string{UDPModeStream, UDPModeDatagram}
	if bytes.Equal(base, DeviceAuthPayload(changedTransport, challenge)) {
		t.Fatal("v5 payload did not bind transport capabilities")
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
	hello.IdentityID = "ignored-by-v3"
	hello.RequestedCapabilities = append(hello.RequestedCapabilities, CapabilityProxyExit)
	if !bytes.Equal(base, DeviceAuthPayload(hello, challenge)) {
		t.Fatal("v3 payload changed when v5-only fields changed")
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

func TestPongProxyExitsDistinguishesRefreshFromNoUpdate(t *testing.T) {
	empty := []ProxyExit{}
	encoded, err := json.Marshal(PongMessage{Timestamp: 1, ProxyExits: &empty, ProxyExitRevision: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"proxyExits":[]`) || !strings.Contains(string(encoded), `"proxyExitRevision":7`) {
		t.Fatalf("empty proxy exit refresh was omitted: %s", encoded)
	}
	var decoded PongMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProxyExits == nil || len(*decoded.ProxyExits) != 0 || decoded.ProxyExitRevision != 7 {
		t.Fatalf("proxy exit refresh did not round-trip: %+v", decoded)
	}
}

func TestResourceInventoryEmptyProxyExitsRoundTrip(t *testing.T) {
	empty := []ProxyExit{}
	encoded, err := json.Marshal(ResourceInventory{ProxyExits: &empty, ProxyExitRevision: 12})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"proxyExits":[]`) {
		t.Fatalf("explicit empty inventory was omitted: %s", encoded)
	}
	var decoded ResourceInventory
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProxyExits == nil || len(*decoded.ProxyExits) != 0 || decoded.ProxyExitRevision != 12 {
		t.Fatalf("resource inventory did not round-trip: %+v", decoded)
	}
}

func TestProxyExitPublicDirectInventoryRoundTrip(t *testing.T) {
	in := ProxyExit{
		DeviceID: "exit-1", Name: "Exit", Online: true,
		Direct: &ProxyDirectPaths{Public: &ProxyPublicDirectPath{
			Available: true, Transport: "quic",
			Endpoints: []PublicDirectEndpoint{{
				Protocol: PublicDirectEndpointProtocolUDP,
				Address:  "203.0.113.20:35820",
				Source:   PublicDirectEndpointManual,
				Verified: true,
			}},
		}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ProxyExit
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Direct == nil || out.Direct.Public == nil || !out.Direct.Public.Available ||
		out.Direct.Public.Transport != "quic" || len(out.Direct.Public.Endpoints) != 1 ||
		!out.Direct.Public.Endpoints[0].Verified {
		t.Fatalf("public direct inventory did not round-trip: %+v", out)
	}
}


func TestDeviceAuthPayloadDoesNotBindBrutalPerformanceHints(t *testing.T) {
	hello := DeviceHello{
		ProtocolVersion: IdentityDeviceProtocolVersion,
		IdentityID:      "a1b2c3d4e5f6g7h8",
		InstallationID:  "install",
		PublicKey:       []byte("key"),
		ClientNonce:     []byte("client"),
		RequestedCapabilities: []string{CapabilityProxyClient},
		TransportCapabilities: []string{UDPModeStream},
	}
	challenge := AuthChallenge{
		ProtocolVersion: IdentityDeviceProtocolVersion,
		ServerInstanceID: "server",
		ServerNonce: []byte("server-nonce"),
	}
	base := DeviceAuthPayload(hello, challenge)
	hello.BrutalUploadBPS = 12_500_000
	hello.BrutalDownloadBPS = 50_000_000
	if !bytes.Equal(base, DeviceAuthPayload(hello, challenge)) {
		t.Fatal("performance-only Brutal hints changed the device authorization signature payload")
	}
}

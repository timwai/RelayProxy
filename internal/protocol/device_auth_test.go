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

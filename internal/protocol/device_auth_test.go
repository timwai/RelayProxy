package protocol

import (
	"bytes"
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

package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
)

const (
	// LegacyDeviceProtocolVersion is the former administrator-approved v3
	// enrollment protocol. Current production servers reject it; the constant
	// remains so upgraded clients receive a framed migration error.
	LegacyDeviceProtocolVersion = 3
	// IdentityDeviceProtocolVersion authenticates a server-issued identity
	// access key in addition to the installation Ed25519 identity.
	IdentityDeviceProtocolVersion = 4
	// DeviceProtocolVersion remains the value sent by a client without an access
	// key so the server can return a precise identity-migration error.
	DeviceProtocolVersion = LegacyDeviceProtocolVersion

	CapabilityProxyClient = "proxy.client"
	CapabilityProxyExit   = "proxy.exit"
	CapabilityRDPClient   = "rdp.controller"
	CapabilityRDPHost     = "rdp.host"
	CapabilityRDPPublic   = "rdp.public"

	ErrCodeApprovalPending  = "APPROVAL_PENDING"
	ErrCodeDeviceRejected   = "DEVICE_REJECTED"
	ErrCodeDeviceRevoked    = "DEVICE_REVOKED"
	ErrCodeProtocolMismatch = "PROTOCOL_MISMATCH"
	ErrCodeAccessKeyInvalid = "ACCESS_KEY_INVALID"
	ErrCodeIdentityConflict = "IDENTITY_CONFLICT"
)

// RDPTarget is the server-approved target list sent to an RDP controller. The
// local target address is deliberately absent: the target Agent always dials
// its own configured RDP service.
type RDPTarget struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
}

// ProxyExit is the client-visible, online exit inventory. Authorization is
// checked again by the gateway for every opened stream.
type ProxyExit struct {
	DeviceID            string `json:"deviceId"`
	Name                string `json:"name"`
	IdentityName        string `json:"identityName,omitempty"`
	AuthorizationSource string `json:"authorizationSource,omitempty"`
	Online              bool   `json:"online"`
}

type DeviceHello struct {
	ProtocolVersion int `json:"protocolVersion"`
	// AccessKey is present only in v4. The server resolves identity from this
	// credential; clients never submit or choose an identity ID.
	AccessKey             string   `json:"accessKey,omitempty"`
	InstallationID        string   `json:"installationId"`
	PublicKey             []byte   `json:"publicKey"`
	ClientNonce           []byte   `json:"clientNonce"`
	DeviceName            string   `json:"deviceName"`
	Platform              string   `json:"platform"`
	Arch                  string   `json:"arch"`
	ClientVersion         string   `json:"clientVersion"`
	RequestedCapabilities []string `json:"requestedCapabilities"`
	TransportCapabilities []string `json:"transportCapabilities,omitempty"`
}

type AuthChallenge struct {
	ProtocolVersion  int    `json:"protocolVersion"`
	ChallengeID      string `json:"challengeId"`
	ServerInstanceID string `json:"serverInstanceId"`
	ServerNonce      []byte `json:"serverNonce"`
	ExpiresAt        int64  `json:"expiresAt"`
}

type AuthProof struct {
	ChallengeID string `json:"challengeId"`
	Signature   []byte `json:"signature"`
}

type DeviceAccepted struct {
	Success               bool         `json:"success"`
	State                 string       `json:"state"`
	DeviceID              string       `json:"deviceId,omitempty"`
	IdentityName          string       `json:"identityName,omitempty"`
	PolicyRevision        int64        `json:"policyRevision,omitempty"`
	ApprovedCapabilities  []string     `json:"approvedCapabilities,omitempty"`
	RDPTargets            []RDPTarget  `json:"rdpTargets,omitempty"`
	ProxyExits            *[]ProxyExit `json:"proxyExits,omitempty"`
	RendezvousAddress     string       `json:"rendezvousAddress,omitempty"`
	RDPLeaseSec           int          `json:"rdpLeaseSec,omitempty"`
	P2PRendezvousAddress  string       `json:"p2pRendezvousAddress,omitempty"`
	P2PLeaseSec           int          `json:"p2pLeaseSec,omitempty"`
	SessionID             string       `json:"sessionId,omitempty"`
	HeartbeatSec          int          `json:"heartbeat,omitempty"`
	MaxConnections        int          `json:"maxConnections,omitempty"`
	ServerTime            int64        `json:"serverTime"`
	RetryAfterSec         int          `json:"retryAfterSec,omitempty"`
	TransportCapabilities []string     `json:"transportCapabilities,omitempty"`
	ErrorCode             string       `json:"errorCode,omitempty"`
	ErrorMessage          string       `json:"errorMessage,omitempty"`
}

// DeviceAuthPayload returns an unambiguous length-prefixed signature payload.
// Both peers use the same domain separator so signatures cannot be replayed as
// signatures for another RelayProxy protocol message.
func DeviceAuthPayload(hello DeviceHello, challenge AuthChallenge) []byte {
	var out bytes.Buffer
	out.WriteString("RelayProxy device authentication\x00")
	_ = binary.Write(&out, binary.BigEndian, uint32(hello.ProtocolVersion))
	writeAuthField(&out, []byte(challenge.ServerInstanceID))
	writeAuthField(&out, challenge.ServerNonce)
	writeAuthField(&out, hello.ClientNonce)
	writeAuthField(&out, []byte(hello.InstallationID))
	keyHash := sha256.Sum256(hello.PublicKey)
	writeAuthField(&out, keyHash[:])
	if hello.ProtocolVersion >= IdentityDeviceProtocolVersion {
		// Bind the credential without signing or logging the plaintext twice.
		// The server has already resolved the key before issuing the challenge.
		accessKeyHash := sha256.Sum256([]byte(hello.AccessKey))
		writeAuthField(&out, accessKeyHash[:])
		_ = binary.Write(&out, binary.BigEndian, uint32(len(hello.RequestedCapabilities)))
		for _, capability := range hello.RequestedCapabilities {
			writeAuthField(&out, []byte(capability))
		}
		_ = binary.Write(&out, binary.BigEndian, uint32(len(hello.TransportCapabilities)))
		for _, capability := range hello.TransportCapabilities {
			writeAuthField(&out, []byte(capability))
		}
	}
	return out.Bytes()
}

func writeAuthField(out *bytes.Buffer, value []byte) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(value)))
	out.Write(value)
}

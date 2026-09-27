package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
)

const (
	DeviceProtocolVersion = 3

	CapabilityProxyClient       = "proxy.client"
	CapabilityProxyExit         = "proxy.exit"
	CapabilityRDPClient         = "rdp.controller"
	CapabilityRDPHost           = "rdp.host"
	CapabilityRDPPublic         = "rdp.public"
	CapabilityDesktopController = "desktop.controller"
	CapabilityDesktopHost       = "desktop.host"

	ErrCodeApprovalPending  = "APPROVAL_PENDING"
	ErrCodeDeviceRejected   = "DEVICE_REJECTED"
	ErrCodeDeviceRevoked    = "DEVICE_REVOKED"
	ErrCodeProtocolMismatch = "PROTOCOL_MISMATCH"
)

// RDPTarget is the server-approved target list sent to an RDP controller. The
// local target address is deliberately absent: the target Agent always dials
// its own configured RDP service.
type RDPTarget struct {
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Online   bool   `json:"online"`
}

type DeviceHello struct {
	ProtocolVersion       int                  `json:"protocolVersion"`
	InstallationID        string               `json:"installationId"`
	PublicKey             []byte               `json:"publicKey"`
	ClientNonce           []byte               `json:"clientNonce"`
	DeviceName            string               `json:"deviceName"`
	Platform              string               `json:"platform"`
	Arch                  string               `json:"arch"`
	ClientVersion         string               `json:"clientVersion"`
	RequestedCapabilities []string             `json:"requestedCapabilities"`
	TransportCapabilities []string             `json:"transportCapabilities,omitempty"`
	DesktopCapabilities   *DesktopCapabilities `json:"desktopCapabilities,omitempty"`
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
	Success               bool                  `json:"success"`
	State                 string                `json:"state"`
	DeviceID              string                `json:"deviceId,omitempty"`
	ApprovedCapabilities  []string              `json:"approvedCapabilities,omitempty"`
	RDPTargets            []RDPTarget           `json:"rdpTargets,omitempty"`
	RemoteDesktopTargets  []RemoteDesktopTarget `json:"remoteDesktopTargets,omitempty"`
	RendezvousAddress     string                `json:"rendezvousAddress,omitempty"`
	RDPLeaseSec           int                   `json:"rdpLeaseSec,omitempty"`
	SessionID             string                `json:"sessionId,omitempty"`
	HeartbeatSec          int                   `json:"heartbeat,omitempty"`
	MaxConnections        int                   `json:"maxConnections,omitempty"`
	ServerTime            int64                 `json:"serverTime"`
	RetryAfterSec         int                   `json:"retryAfterSec,omitempty"`
	TransportCapabilities []string              `json:"transportCapabilities,omitempty"`
	ErrorCode             string                `json:"errorCode,omitempty"`
	ErrorMessage          string                `json:"errorMessage,omitempty"`
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
	return out.Bytes()
}

func writeAuthField(out *bytes.Buffer, value []byte) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(value)))
	out.Write(value)
}

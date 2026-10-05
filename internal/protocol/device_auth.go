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
	// IdentityDeviceProtocolVersion binds the public identity ID to the
	// installation Ed25519 proof. Device admission remains server-approved.
	IdentityDeviceProtocolVersion = 5
	DeviceProtocolVersion         = IdentityDeviceProtocolVersion

	CapabilityProxyClient = "proxy.client"
	CapabilityProxyExit   = "proxy.exit"
	// CapabilityRuntimeState tells the server that this client distinguishes
	// approved device capabilities from the capabilities currently running.
	// The active flags keep a control-only session from being advertised as an
	// online exit while the user has not started its data plane.
	CapabilityRuntimeState          = "runtime.capabilities"
	CapabilityResourceInventoryPush = "resource.inventory.push_v1"
	CapabilityProxyClientActive     = "proxy.client.active"
	CapabilityProxyExitActive       = "proxy.exit.active"
	CapabilityRDPClient             = "rdp.controller"
	CapabilityRDPHost               = "rdp.host"
	CapabilityRDPPublic             = "rdp.public"

	ErrCodeApprovalPending  = "APPROVAL_PENDING"
	ErrCodeDeviceRejected   = "DEVICE_REJECTED"
	ErrCodeDeviceRevoked    = "DEVICE_REVOKED"
	ErrCodeProtocolMismatch = "PROTOCOL_MISMATCH"
	ErrCodeIdentityInvalid  = "IDENTITY_INVALID"
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
type ProxyPublicDirectPath struct {
	Available       bool                   `json:"available"`
	Transport       string                 `json:"transport,omitempty"`
	Endpoints       []PublicDirectEndpoint `json:"endpoints,omitempty"`
	Ticket          []byte                 `json:"ticket,omitempty"`
	TicketExpiresAt int64                  `json:"ticketExpiresAt,omitempty"`
}

type ProxyDirectPaths struct {
	Public *ProxyPublicDirectPath `json:"public,omitempty"`
}

type ProxyExit struct {
	DeviceID            string            `json:"deviceId"`
	Name                string            `json:"name"`
	IdentityName        string            `json:"identityName,omitempty"`
	AuthorizationSource string            `json:"authorizationSource,omitempty"`
	Online              bool              `json:"online"`
	Direct              *ProxyDirectPaths `json:"direct,omitempty"`
}

type DeviceHello struct {
	ProtocolVersion       int      `json:"protocolVersion"`
	IdentityID            string   `json:"identityId"`
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
	ProxyExitRevision     uint64       `json:"proxyExitRevision,omitempty"`
	RendezvousAddress     string       `json:"rendezvousAddress,omitempty"`
	RDPLeaseSec           int          `json:"rdpLeaseSec,omitempty"`
	P2PRendezvousAddress  string       `json:"p2pRendezvousAddress,omitempty"`
	P2PLeaseSec           int          `json:"p2pLeaseSec,omitempty"`
	P2PPortStart          int          `json:"p2pPortStart,omitempty"`
	P2PPortEnd            int          `json:"p2pPortEnd,omitempty"`
	P2PUPnPEnabled             bool         `json:"p2pUPnPEnabled,omitempty"`
	PublicDirectTicketIssuer  string       `json:"publicDirectTicketIssuer,omitempty"`
	PublicDirectTicketKey     []byte       `json:"publicDirectTicketKey,omitempty"`
	SessionID                  string       `json:"sessionId,omitempty"`
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
		writeAuthField(&out, []byte(hello.IdentityID))
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

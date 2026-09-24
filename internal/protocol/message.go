package protocol

import "relayproxy/internal/acl"

// FrameType identifies the kind of stream or message frame
type FrameType uint8

const (
	FrameTypeControl          FrameType = 0x01
	FrameTypeOpenTCP          FrameType = 0x02
	FrameTypeOpenTCPResp      FrameType = 0x03
	FrameTypeData             FrameType = 0x04
	FrameTypePing             FrameType = 0x05
	FrameTypePong             FrameType = 0x06
	FrameTypeGoAway           FrameType = 0x07
	FrameTypeOpenUDP          FrameType = 0x08
	FrameTypeOpenUDPResp      FrameType = 0x09
	FrameTypeOpenRDP          FrameType = 0x0A
	FrameTypeOpenRDPUDP       FrameType = 0x0B
	FrameTypeRDPControl       FrameType = 0x0C
	FrameTypeDesktopControl   FrameType = 0x0D
	FrameTypeOpenDesktopMedia FrameType = 0x0E
)

// StreamHeader is sent at the beginning of each multiplexed stream
type StreamHeader struct {
	Magic          uint16    `json:"magic"`
	Version        uint8     `json:"version"`
	Type           FrameType `json:"type"`
	RequestID      string    `json:"requestId"`
	ClientDeviceID string    `json:"clientDeviceId"`
	ExitDeviceID   string    `json:"exitDeviceId"`
}

// OpenTCPRequest is sent from Client -> Relay -> Exit to request a TCP connection
type OpenTCPRequest struct {
	RequestID   string      `json:"requestId"`
	Host        string      `json:"host"`
	Port        uint16      `json:"port"`
	TimeoutMs   int         `json:"timeout"` // Timeout in milliseconds
	RelayPolicy *acl.Policy `json:"relayPolicy,omitempty"`
}

// OpenTCPResponse is returned from Exit -> Relay -> Client
type OpenTCPResponse struct {
	RequestID    string `json:"requestId"`
	Success      bool   `json:"success"`
	RemoteIP     string `json:"remoteIp,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// OpenUDPRequest is sent from Client -> Relay -> Exit to request a UDP association
type OpenUDPRequest struct {
	RequestID        string      `json:"requestId"`
	Host             string      `json:"host"`
	Port             uint16      `json:"port"`
	TimeoutMs        int         `json:"timeout"`
	Mode             string      `json:"mode,omitempty"`
	AssociationID    uint64      `json:"associationId,omitempty"`
	DatagramRequired bool        `json:"datagramRequired,omitempty"`
	RelayPolicy      *acl.Policy `json:"relayPolicy,omitempty"`
}

// OpenUDPResponse is returned from Exit -> Relay -> Client
type OpenUDPResponse struct {
	RequestID     string `json:"requestId"`
	Success       bool   `json:"success"`
	RemoteIP      string `json:"remoteIp,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	Mode          string `json:"mode,omitempty"`
	AssociationID uint64 `json:"associationId,omitempty"`
}

// OpenRDPRequest contains no host or port by design. The relay resolves the
// target device from the header and the target Agent uses its fixed local RDP
// endpoint, preventing an RDP grant from becoming arbitrary port forwarding.
type OpenRDPRequest struct {
	RequestID        string `json:"requestId"`
	TimeoutMs        int    `json:"timeout"`
	Mode             string `json:"mode,omitempty"`
	AssociationID    uint64 `json:"associationId,omitempty"`
	DatagramRequired bool   `json:"datagramRequired,omitempty"`
}

const DesktopMediaModeDatagram = "desktop_datagram_v1"

// OpenDesktopMediaRequest establishes one authenticated Relay Desktop media
// association. The reliable stream remains open as the association lifetime
// signal; encoded media itself flows only over native QUIC datagrams.
type OpenDesktopMediaRequest struct {
	RequestID        string                       `json:"requestId"`
	TimeoutMs        int                          `json:"timeout"`
	Mode             string                       `json:"mode"`
	AssociationID    uint64                       `json:"associationId"`
	DesktopSessionID string                       `json:"desktopSessionId,omitempty"`
	Options          *RemoteDesktopConnectOptions `json:"options,omitempty"`
}

type OpenDesktopMediaResponse struct {
	RequestID     string `json:"requestId"`
	Success       bool   `json:"success"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
	Mode          string `json:"mode,omitempty"`
	AssociationID uint64 `json:"associationId,omitempty"`
}

// RDPControlType values are exchanged over short-lived, authenticated
// FrameTypeRDPControl streams. A stream carries one request and at most one
// response; server-pushed notifications use the same frame without a reply.
const (
	RDPControlRegister        = "register"
	RDPControlRegisterAck     = "register_ack"
	RDPControlConnectRequest  = "connect_request"
	RDPControlConnectResponse = "connect_response"
	RDPControlConnectNotify   = "connect_notify"
	RDPControlCandidateUpdate = "candidate_update"
	RDPControlLeaseRenew      = "lease_renew"
	RDPControlLeaseAck        = "lease_ack"
	RDPControlSessionClose    = "session_close"
	RDPControlError           = "error"
)

// RDPCandidate describes one protocol-specific endpoint. The address is an
// IP literal plus port; hostnames, unspecified and multicast addresses are
// rejected before the candidate reaches the signaling layer.
type RDPCandidate struct {
	Protocol string `json:"protocol"` // tcp or udp
	Type     string `json:"type"`     // lan or reflexive
	Address  string `json:"address"`
	Priority uint32 `json:"priority"`
}

const (
	P2PPurposeRDP          = "rdp"
	P2PPurposeDesktopMedia = "desktop_media"
)

// RDPControlMessage binds signaling to a server-issued session. Device IDs in
// requests are advisory only; the server derives the controller from the
// authenticated tunnel session and validates the target against its grant.
// SessionToken is memory-only and is never persisted or logged.
type RDPControlMessage struct {
	Type              string         `json:"type"`
	Purpose           string         `json:"purpose,omitempty"`
	SessionID         uint64         `json:"sessionId,omitempty"`
	DesktopSessionID  string         `json:"desktopSessionId,omitempty"`
	ControllerID      string         `json:"controllerId,omitempty"`
	TargetID          string         `json:"targetId,omitempty"`
	SessionToken      []byte         `json:"sessionToken,omitempty"`
	Candidates        []RDPCandidate `json:"candidates,omitempty"`
	LeaseExpiresAt    int64          `json:"leaseExpiresAt,omitempty"`
	RendezvousAddress string         `json:"rendezvousAddress,omitempty"`
	LeaseSec          int            `json:"leaseSec,omitempty"`
	Path              string         `json:"path,omitempty"`
	ErrorCode         string         `json:"errorCode,omitempty"`
	ErrorMessage      string         `json:"errorMessage,omitempty"`
	RDPOnline         bool           `json:"rdpOnline,omitempty"`
}

const (
	UDPModeStream       = "udp_stream_v1"
	UDPModeDatagram     = "udp_datagram_v1"
	CapabilityTargetACL = "target_acl_v1"
)

// PingMessage for heartbeat
type PingMessage struct {
	Timestamp int64 `json:"timestamp"`
}

// PongMessage for heartbeat response
type PongMessage struct {
	Timestamp int64 `json:"timestamp"`
}

// GoAwayMessage notifies graceful shutdown
type GoAwayMessage struct {
	Reason string `json:"reason"`
}

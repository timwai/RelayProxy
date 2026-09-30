package protocol

import "relayproxy/internal/acl"

const (
	// CapabilityProxyP2P is advertised as a transport capability. The server
	// only coordinates a direct proxy path when both authenticated peers
	// advertise it and retain their normal proxy client/exit grants.
	CapabilityProxyP2P = "proxy_p2p_v1"

	P2PControlConnectRequest  = "connect_request"
	P2PControlConnectOffer    = "connect_offer"
	P2PControlConnectAnswer   = "connect_answer"
	P2PControlCandidateUpdate = "candidate_update"
	P2PControlLeaseRenew      = "lease_renew"
	P2PControlLeaseAck        = "lease_ack"
	P2PControlClose           = "close"
	P2PControlRevoke          = "revoke"
	P2PControlPathReport      = "path_report"
	P2PControlError           = "error"

	P2PPathDirectQUIC = "p2p_quic"
	P2PPathRelayQUIC  = "relay_quic"
	P2PPathRelayTLS   = "relay_tls"
)

// P2PControlMessage carries proxy direct-path signaling over the authenticated
// Relay control plane. SessionToken and certificate fingerprints are ephemeral
// and must never be persisted or logged.
type P2PControlMessage struct {
	Type              string         `json:"type"`
	SessionID         uint64         `json:"sessionId,omitempty"`
	ClientDeviceID    string         `json:"clientDeviceId,omitempty"`
	ExitDeviceID      string         `json:"exitDeviceId,omitempty"`
	SessionToken      []byte         `json:"sessionToken,omitempty"`
	Candidates        []P2PCandidate `json:"candidates,omitempty"`
	CertFingerprint   string         `json:"certFingerprint,omitempty"`
	PeerFingerprint   string         `json:"peerFingerprint,omitempty"`
	RelayPolicy       *acl.Policy    `json:"relayPolicy,omitempty"`
	LeaseExpiresAt    int64          `json:"leaseExpiresAt,omitempty"`
	RendezvousAddress string         `json:"rendezvousAddress,omitempty"`
	LeaseSec          int            `json:"leaseSec,omitempty"`
	Path              string         `json:"path,omitempty"`
	ActiveStreams     int            `json:"activeStreams,omitempty"`
	BytesUp           uint64         `json:"bytesUp,omitempty"`
	BytesDown         uint64         `json:"bytesDown,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	ErrorCode         string         `json:"errorCode,omitempty"`
	ErrorMessage      string         `json:"errorMessage,omitempty"`
}

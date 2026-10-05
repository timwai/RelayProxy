package protocol

import "relayproxy/internal/acl"

const (
	// CapabilityProxyP2P is advertised as a transport capability. The server
	// only coordinates a direct proxy path when both authenticated peers
	// advertise it and retain their normal proxy client/exit grants.
	CapabilityProxyP2P = "proxy_p2p_v1"

	// CapabilityProxyStreamResume enables the resumable TCP overlay used by
	// established P2P streams. It is effective only when the authenticated Relay
	// confirms that both Client and Exit advertise it for the concrete P2P session.
	CapabilityProxyStreamResume = "proxy_stream_resume_v1"
	CapabilitySpeedTest         = "speed_test_v1"

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

	// Backward-compatible aliases for the original P2P-oriented path names.
	// New code should use ProxyPath* so Public Direct shares the same path model.
	P2PPathDirectQUIC = proxyPathP2PQUIC
	P2PPathRelayQUIC  = proxyPathRelayQUIC
	P2PPathRelayTLS   = proxyPathRelayTLS
)

// P2PControlMessage carries proxy direct-path signaling over the authenticated
// Relay control plane. SessionToken and certificate fingerprints are ephemeral
// and must never be persisted or logged.
type P2PControlMessage struct {
	Type            string         `json:"type"`
	SessionID       uint64         `json:"sessionId,omitempty"`
	ClientDeviceID  string         `json:"clientDeviceId,omitempty"`
	ExitDeviceID    string         `json:"exitDeviceId,omitempty"`
	SessionToken    []byte         `json:"sessionToken,omitempty"`
	Candidates      []P2PCandidate `json:"candidates,omitempty"`
	CertFingerprint string         `json:"certFingerprint,omitempty"`
	PeerFingerprint string         `json:"peerFingerprint,omitempty"`
	// PeerCapabilities are injected by the authenticated Relay coordinator.
	// Peers must never trust capabilities echoed from the remote P2P endpoint.
	PeerCapabilities  []string    `json:"peerCapabilities,omitempty"`
	RelayPolicy       *acl.Policy `json:"relayPolicy,omitempty"`
	LeaseExpiresAt    int64       `json:"leaseExpiresAt,omitempty"`
	RendezvousAddress string      `json:"rendezvousAddress,omitempty"`
	LeaseSec          int         `json:"leaseSec,omitempty"`
	Path              string      `json:"path,omitempty"`
	RTTMs             int64       `json:"rttMs,omitempty"`
	CandidateSummary  string      `json:"candidateSummary,omitempty"`
	FallbackCount     uint64      `json:"fallbackCount,omitempty"`
	ActiveStreams     int         `json:"activeStreams,omitempty"`
	BytesUp           uint64      `json:"bytesUp,omitempty"`
	BytesDown         uint64      `json:"bytesDown,omitempty"`
	Reason            string      `json:"reason,omitempty"`
	ErrorCode         string      `json:"errorCode,omitempty"`
	ErrorMessage      string      `json:"errorMessage,omitempty"`
}

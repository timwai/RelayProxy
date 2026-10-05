package protocol

// ProxyPath identifies the concrete transport path used to reach a selected
// proxy Exit. Routing still selects the Exit; ProxyPath only describes how the
// Client reaches that Exit.
type ProxyPath string

const (
	ProxyPathPublicDirectQUIC ProxyPath = "public_direct_quic"
	ProxyPathP2PQUIC          ProxyPath = "p2p_quic"
	ProxyPathRelayQUIC        ProxyPath = "relay_quic"
	ProxyPathRelayTLS         ProxyPath = "relay_tls"

	PublicDirectALPN = "relayproxy-public-direct-v1"

	PublicDirectEndpointProtocolUDP = "udp"

	PublicDirectEndpointSourceObserved = "observed"
	PublicDirectEndpointSourceIPv6     = "ipv6"
	PublicDirectEndpointSourceManual   = "manual"

	PublicDirectControlRegister      = "register"
	PublicDirectControlRegisterAck   = "register_ack"
	PublicDirectControlUnregister    = "unregister"
	PublicDirectControlUnregisterAck = "unregister_ack"
	PublicDirectControlError         = "error"

	PublicDirectHandshakeVerify         = "verify"
	PublicDirectHandshakeVerifyResponse = "verify_response"
)

type PublicDirectEndpoint struct {
	Protocol   string `json:"protocol"`
	Address    string `json:"address"`
	Source     string `json:"source"`
	Verified   bool   `json:"verified"`
	VerifiedAt int64  `json:"verifiedAt,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

// PublicDirectControlMessage is exchanged only over the already-authenticated
// Relay control tunnel. VerificationSecret is ephemeral, memory-only material
// used to bind reachability probes to the authenticated Exit session.
type PublicDirectControlMessage struct {
	Type               string                 `json:"type"`
	RegistrationID     string                 `json:"registrationId,omitempty"`
	ListenerPort       int                    `json:"listenerPort,omitempty"`
	Candidates         []PublicDirectEndpoint `json:"candidates,omitempty"`
	VerificationSecret []byte                 `json:"verificationSecret,omitempty"`
	Endpoints          []PublicDirectEndpoint `json:"endpoints,omitempty"`
	ExpiresAt          int64                  `json:"expiresAt,omitempty"`
	ErrorCode          string                 `json:"errorCode,omitempty"`
	ErrorMessage       string                 `json:"errorMessage,omitempty"`
}

// PublicDirectHandshake is the first application message on a Public Direct
// QUIC connection. Phase 3 uses verify / verify_response. Phase 4 extends the
// same authenticated handshake layer with Direct Access Tickets.
type PublicDirectHandshake struct {
	Type           string `json:"type"`
	RegistrationID string `json:"registrationId,omitempty"`
	Nonce          []byte `json:"nonce,omitempty"`
	Proof          []byte `json:"proof,omitempty"`
}

func (p ProxyPath) String() string { return string(p) }

// IsDirect reports whether traffic reaches the Exit without traversing the
// Relay data plane.
func (p ProxyPath) IsDirect() bool {
	switch p {
	case ProxyPathPublicDirectQUIC, ProxyPathP2PQUIC:
		return true
	default:
		return false
	}
}

// IsRelay reports whether proxy data traverses the Relay Server.
func (p ProxyPath) IsRelay() bool {
	switch p {
	case ProxyPathRelayQUIC, ProxyPathRelayTLS:
		return true
	default:
		return false
	}
}

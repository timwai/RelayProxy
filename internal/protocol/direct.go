package protocol

// ProxyPath identifies the concrete transport used to reach a selected proxy
// Exit. Routing still chooses the Exit; this value describes only the data path.
type ProxyPath string

const (
	proxyPathPublicDirectQUIC = "public_direct_quic"
	proxyPathP2PQUIC          = "p2p_quic"
	proxyPathRelayQUIC        = "relay_quic"
	proxyPathRelayTLS         = "relay_tls"

	ProxyPathPublicDirectQUIC ProxyPath = proxyPathPublicDirectQUIC
	ProxyPathP2PQUIC          ProxyPath = proxyPathP2PQUIC
	ProxyPathRelayQUIC        ProxyPath = proxyPathRelayQUIC
	ProxyPathRelayTLS         ProxyPath = proxyPathRelayTLS
)

func (p ProxyPath) String() string { return string(p) }

func (p ProxyPath) IsDirect() bool {
	return p == ProxyPathPublicDirectQUIC || p == ProxyPathP2PQUIC
}

func (p ProxyPath) IsRelay() bool {
	return p == ProxyPathRelayQUIC || p == ProxyPathRelayTLS
}

const PublicDirectAuthVersion = 1

// PublicDirectAuthRequest authenticates a newly established Public Direct
// connection before any proxy stream is accepted. Ticket is intentionally
// opaque here so Phase 4 can replace development test credentials with
// Server-signed short-lived tickets without changing the transport handshake.
type PublicDirectAuthRequest struct {
	Version        int    `json:"version"`
	ClientDeviceID string `json:"clientDeviceId"`
	ExitDeviceID   string `json:"exitDeviceId"`
	Ticket         []byte `json:"ticket"`
}

type PublicDirectAuthResponse struct {
	Success      bool   `json:"success"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

const (
	CapabilityProxyPublicDirect = "proxy_public_direct_v1"

	PublicDirectEndpointProtocolUDP = "udp"
	PublicDirectEndpointObserved    = "observed"
	PublicDirectEndpointIPv6        = "ipv6"
	PublicDirectEndpointManual      = "manual"

	PublicDirectHandshakeAuth  = "auth"
	PublicDirectHandshakeProbe = "probe"
)

type PublicDirectEndpointCandidate struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Source   string `json:"source"`
}

type PublicDirectEndpoint struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

type PublicDirectRegistrationRequest struct {
	ListenerPort    uint16                          `json:"listenerPort"`
	CertFingerprint string                          `json:"certFingerprint"`
	NetworkEpoch    uint64                          `json:"networkEpoch,omitempty"`
	Candidates      []PublicDirectEndpointCandidate `json:"candidates,omitempty"`
}

type PublicDirectRegistrationResponse struct {
	Success      bool                   `json:"success"`
	Endpoints    []PublicDirectEndpoint `json:"endpoints,omitempty"`
	ErrorCode    string                 `json:"errorCode,omitempty"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
}

type PublicDirectProbeRequest struct {
	Nonce []byte `json:"nonce"`
}

type PublicDirectProbeResponse struct {
	Nonce []byte `json:"nonce"`
}

type PublicDirectHandshakeRequest struct {
	Type  string                    `json:"type"`
	Auth  *PublicDirectAuthRequest  `json:"auth,omitempty"`
	Probe *PublicDirectProbeRequest `json:"probe,omitempty"`
}

type PublicDirectHandshakeResponse struct {
	Success      bool                       `json:"success"`
	ErrorCode    string                     `json:"errorCode,omitempty"`
	ErrorMessage string                     `json:"errorMessage,omitempty"`
	Probe        *PublicDirectProbeResponse `json:"probe,omitempty"`
}

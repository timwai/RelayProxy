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

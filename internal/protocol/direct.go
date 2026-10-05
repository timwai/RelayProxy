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

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
)

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

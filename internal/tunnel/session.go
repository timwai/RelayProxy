package tunnel

import (
	"context"
	"net"
)

// TransportType identifies the underlying transport protocol
type TransportType string

const (
	TransportQUIC TransportType = "quic"
	TransportTLS  TransportType = "tls"
)

// TunnelSession represents a persistent multiplexed tunnel connection between two endpoints
type TunnelSession interface {
	// OpenStream creates a new bidirectional multiplexed stream
	OpenStream(ctx context.Context) (TunnelStream, error)

	// AcceptStream waits for and returns the next incoming multiplexed stream
	AcceptStream(ctx context.Context) (TunnelStream, error)

	// Transport returns the transport type (quic or tls)
	Transport() TransportType

	// RemoteAddr returns the remote network address
	RemoteAddr() net.Addr

	// LocalAddr returns the local network address
	LocalAddr() net.Addr

	// Close closes the underlying session and all associated streams
	Close() error

	// Done is closed when the underlying session is no longer usable.
	// Callers (e.g. TunnelManager.reconnectLoop) must watch this to detect silent death.
	Done() <-chan struct{}
}

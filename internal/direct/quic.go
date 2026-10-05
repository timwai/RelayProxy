package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// QUICOptions controls the long-lived direct QUIC transport profile. It is
// shared by Public Direct and P2P; authentication remains owned by each path.
type QUICOptions struct {
	KeepAlivePeriod  time.Duration
	MaxIdleTimeout   time.Duration
	DisableKeepAlive bool
}

// QUICConfig returns the direct-path QUIC profile. Datagram support is always
// enabled because RelayProxy UDP proxying may negotiate native QUIC datagrams.
func QUICConfig(options ...QUICOptions) *quic.Config {
	config := tunnel.DefaultQUICConfig()
	config.KeepAlivePeriod = 10 * time.Second
	config.MaxIdleTimeout = 120 * time.Second
	if len(options) > 0 {
		if options[0].DisableKeepAlive {
			config.KeepAlivePeriod = 0
		} else if options[0].KeepAlivePeriod > 0 {
			config.KeepAlivePeriod = options[0].KeepAlivePeriod
		}
		if options[0].MaxIdleTimeout > 0 {
			config.MaxIdleTimeout = options[0].MaxIdleTimeout
		}
	}
	config.EnableDatagrams = true
	return config
}

func normalizeTLSConfig(config *tls.Config) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("direct QUIC TLS config is required")
	}
	config = config.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	if len(config.NextProtos) == 0 {
		return nil, errors.New("direct QUIC ALPN is required")
	}
	return config, nil
}

// DialAddr establishes a direct QUIC session over a normal UDP socket. It does
// not define an authentication model; callers must supply a TLS config that
// authenticates the peer according to the selected direct-path mechanism.
func DialAddr(ctx context.Context, address string, tlsConfig *tls.Config, options ...QUICOptions) (tunnel.TunnelSession, error) {
	if address == "" {
		return nil, errors.New("direct QUIC address is required")
	}
	tlsConfig, err := normalizeTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}
	session, err := tunnel.DialQUIC(ctx, address, tlsConfig, QUICConfig(options...))
	if err != nil {
		return nil, fmt.Errorf("direct QUIC dial failed: %w", err)
	}
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

// Listener owns a long-lived QUIC listener suitable for accepting multiple
// Public Direct sessions. Unlike P2P's punched socket, it is not session-scoped.
type Listener struct {
	inner *quic.Listener
}

func ListenAddr(address string, tlsConfig *tls.Config, options ...QUICOptions) (*Listener, error) {
	if address == "" {
		return nil, errors.New("direct QUIC listen address is required")
	}
	tlsConfig, err := normalizeTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}
	inner, err := quic.ListenAddr(address, tlsConfig, QUICConfig(options...))
	if err != nil {
		return nil, fmt.Errorf("direct QUIC listen failed: %w", err)
	}
	return &Listener{inner: inner}, nil
}

func (l *Listener) Accept(ctx context.Context) (tunnel.TunnelSession, error) {
	if l == nil || l.inner == nil {
		return nil, net.ErrClosed
	}
	conn, err := l.inner.Accept(ctx)
	if err != nil {
		return nil, err
	}
	session := tunnel.NewQUICSession(conn)
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

func (l *Listener) Addr() net.Addr {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Addr()
}

func (l *Listener) Close() error {
	if l == nil || l.inner == nil {
		return nil
	}
	return l.inner.Close()
}

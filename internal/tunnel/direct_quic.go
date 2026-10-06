package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/apernet/quic-go"
	"relayproxy/internal/protocol"
)

const (
	DirectQUICALPN = "relayproxy-direct-v1"

	directInitialStreamReceiveWindow     = 16 << 20
	directMaxStreamReceiveWindow         = 64 << 20
	directInitialConnectionReceiveWindow = 32 << 20
	directMaxConnectionReceiveWindow     = 256 << 20
)

// DirectQUICConfig returns the generic QUIC profile shared by direct transports.
// Authentication is deliberately owned by the caller; this helper only defines
// transport behavior.
func DirectQUICConfig(config *quic.Config) *quic.Config {
	if config == nil {
		config = DefaultQUICConfig()
	} else {
		config = config.Clone()
	}

	// Public Direct is expected to carry high-BDP WAN traffic. Treat these
	// values as minimums rather than nil-only defaults so a generic QUIC config
	// can't accidentally downgrade the direct path. Larger explicit values are
	// preserved.
	config.InitialStreamReceiveWindow = max(config.InitialStreamReceiveWindow, uint64(directInitialStreamReceiveWindow))
	config.MaxStreamReceiveWindow = max(config.MaxStreamReceiveWindow, uint64(directMaxStreamReceiveWindow))
	config.InitialConnectionReceiveWindow = max(config.InitialConnectionReceiveWindow, uint64(directInitialConnectionReceiveWindow))
	config.MaxConnectionReceiveWindow = max(config.MaxConnectionReceiveWindow, uint64(directMaxConnectionReceiveWindow))
	config.EnableDatagrams = true
	return config
}

func directTLSConfig(config *tls.Config, server bool) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("direct QUIC requires an explicit TLS configuration")
	}
	config = config.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	if !slices.Contains(config.NextProtos, DirectQUICALPN) {
		config.NextProtos = append(config.NextProtos, DirectQUICALPN)
	}
	if server && len(config.Certificates) == 0 && config.GetCertificate == nil {
		return nil, errors.New("direct QUIC listener requires a TLS certificate")
	}
	return config, nil
}

// DirectQUICListener is a long-lived QUIC listener suitable for transports
// whose UDP endpoint is stable across many client sessions.
type DirectQUICListener struct {
	listener   *quic.Listener
	packetConn *net.UDPConn
	closeOnce  sync.Once
	closeErr   error
}

func ListenDirectQUIC(address string, tlsConfig *tls.Config, quicConfig *quic.Config) (*DirectQUICListener, error) {
	tlsConfig, err := directTLSConfig(tlsConfig, true)
	if err != nil {
		return nil, err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve direct QUIC listen address: %w", err)
	}
	packetConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("direct QUIC UDP listen failed: %w", err)
	}
	TuneDirectQUICUDPConn(packetConn)
	listener, err := quic.Listen(packetConn, tlsConfig, DirectQUICConfig(quicConfig))
	if err != nil {
		_ = packetConn.Close()
		return nil, fmt.Errorf("direct QUIC listen failed: %w", err)
	}
	return &DirectQUICListener{listener: listener, packetConn: packetConn}, nil
}

func (l *DirectQUICListener) Accept(ctx context.Context) (*QUICSession, error) {
	if l == nil || l.listener == nil {
		return nil, net.ErrClosed
	}
	conn, err := l.listener.Accept(ctx)
	if err != nil {
		return nil, err
	}
	session := NewQUICSession(conn)
	session.setUDPSocketBufferSizes(l.packetConn)
	SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

func (l *DirectQUICListener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *DirectQUICListener) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		if l.listener != nil {
			l.closeErr = l.listener.Close()
		}
		if l.packetConn != nil {
			if err := l.packetConn.Close(); l.closeErr == nil && !errors.Is(err, net.ErrClosed) {
				l.closeErr = err
			}
		}
	})
	return l.closeErr
}

func DialDirectQUIC(ctx context.Context, address string, tlsConfig *tls.Config, quicConfig *quic.Config) (*QUICSession, error) {
	tlsConfig, err := directTLSConfig(tlsConfig, false)
	if err != nil {
		return nil, err
	}
	remoteAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, fmt.Errorf("resolve direct QUIC address: %w", err)
	}
	if tlsConfig.ServerName == "" {
		if host, _, splitErr := net.SplitHostPort(address); splitErr == nil {
			tlsConfig.ServerName = strings.Trim(host, "[]")
		}
	}

	network := "udp"
	localAddr := &net.UDPAddr{Port: 0}
	if remoteAddr.IP.To4() != nil {
		network = "udp4"
		localAddr.IP = net.IPv4zero
	} else if remoteAddr.IP.To16() != nil {
		network = "udp6"
		localAddr.IP = net.IPv6unspecified
	}
	packetConn, err := net.ListenUDP(network, localAddr)
	if err != nil {
		return nil, fmt.Errorf("direct QUIC UDP socket failed: %w", err)
	}
	TuneDirectQUICUDPConn(packetConn)

	conn, err := quic.Dial(ctx, packetConn, remoteAddr, tlsConfig, DirectQUICConfig(quicConfig))
	if err != nil {
		_ = packetConn.Close()
		return nil, fmt.Errorf("direct QUIC dial failed: %w", err)
	}
	session := newOwnedQUICSession(conn, packetConn)
	SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

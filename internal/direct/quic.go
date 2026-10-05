package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/tunnel"
)

type QUICOptions struct {
	KeepAlivePeriod  time.Duration
	MaxIdleTimeout   time.Duration
	DisableKeepAlive bool
}

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

type QUICStats struct {
	RTT           time.Duration
	BytesSent     uint64
	BytesReceived uint64
}

// PacketSession owns the quic.Transport used when QUIC takes over a caller-
// supplied packet socket. P2P uses this form so punching and QUIC share the
// exact same UDP mapping.
type PacketSession struct {
	*tunnel.QUICSession
	conn      *quic.Conn
	transport *quic.Transport
	closeOnce sync.Once
	closeErr  error
}

func DialPacket(
	ctx context.Context,
	conn net.PacketConn,
	remote net.Addr,
	tlsConfig *tls.Config,
	quicConfig *quic.Config,
) (*PacketSession, error) {
	if conn == nil || remote == nil || tlsConfig == nil {
		return nil, errors.New("direct QUIC dial requires packet socket, remote address and TLS config")
	}
	transport := &quic.Transport{Conn: conn}
	qconn, err := transport.Dial(ctx, remote, tlsConfig, cloneQUICConfig(quicConfig))
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("direct QUIC dial failed: %w", err)
	}
	return &PacketSession{
		QUICSession: tunnel.NewQUICSession(qconn),
		conn:        qconn,
		transport:   transport,
	}, nil
}

func AcceptPacket(
	ctx context.Context,
	conn net.PacketConn,
	tlsConfig *tls.Config,
	quicConfig *quic.Config,
) (*PacketSession, error) {
	if conn == nil || tlsConfig == nil {
		return nil, errors.New("direct QUIC listen requires packet socket and TLS config")
	}
	transport := &quic.Transport{Conn: conn}
	listener, err := transport.Listen(tlsConfig, cloneQUICConfig(quicConfig))
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("direct QUIC listen failed: %w", err)
	}
	qconn, err := listener.Accept(ctx)
	if err != nil {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("direct QUIC accept failed: %w", err)
	}
	// A supplied packet socket belongs to one direct session. Stop accepting
	// additional connections while preserving the accepted connection.
	_ = listener.Close()
	return &PacketSession{
		QUICSession: tunnel.NewQUICSession(qconn),
		conn:        qconn,
		transport:   transport,
	}, nil
}

func (s *PacketSession) Stats() QUICStats {
	if s == nil || s.conn == nil {
		return QUICStats{}
	}
	stats := s.conn.ConnectionStats()
	return QUICStats{
		RTT:           stats.SmoothedRTT,
		BytesSent:     stats.BytesSent,
		BytesReceived: stats.BytesReceived,
	}
}

func (s *PacketSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.QUICSession != nil {
			s.closeErr = s.QUICSession.Close()
		}
		if s.transport != nil {
			if err := s.transport.Close(); s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

// Listener owns a stable UDP listener and can accept many independent direct
// QUIC sessions. Individual session closes do not close the listener.
type Listener struct {
	listener *quic.Listener
}

func ListenAddr(addr string, tlsConfig *tls.Config, quicConfig *quic.Config) (*Listener, error) {
	if addr == "" || tlsConfig == nil {
		return nil, errors.New("direct QUIC listener requires address and TLS config")
	}
	listener, err := quic.ListenAddr(addr, tlsConfig, cloneQUICConfig(quicConfig))
	if err != nil {
		return nil, fmt.Errorf("direct QUIC listen failed: %w", err)
	}
	return &Listener{listener: listener}, nil
}

func (l *Listener) Accept(ctx context.Context) (tunnel.TunnelSession, error) {
	if l == nil || l.listener == nil {
		return nil, net.ErrClosed
	}
	conn, err := l.listener.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return tunnel.NewQUICSession(conn), nil
}

func (l *Listener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *Listener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func DialAddr(ctx context.Context, addr string, tlsConfig *tls.Config, quicConfig *quic.Config) (tunnel.TunnelSession, error) {
	if addr == "" || tlsConfig == nil {
		return nil, errors.New("direct QUIC dial requires address and TLS config")
	}
	return tunnel.DialQUIC(ctx, addr, tlsConfig, cloneQUICConfig(quicConfig))
}

func cloneQUICConfig(config *quic.Config) *quic.Config {
	if config == nil {
		return QUICConfig()
	}
	clone := config.Clone()
	clone.EnableDatagrams = true
	return clone
}

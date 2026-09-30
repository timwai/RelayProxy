package p2p

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const QUICALPN = "relayproxy-p2p-v1"

type QUICOptions struct {
	KeepAlivePeriod time.Duration
	MaxIdleTimeout  time.Duration
	DisableKeepAlive bool
}

// QUICSession owns the quic-go Transport that took over the punched UDP socket.
// The embedded RelayProxy session keeps the existing stream/datagram interface.
type QUICSession struct {
	*tunnel.QUICSession
	conn      *quic.Conn
	transport *quic.Transport
	closeOnce sync.Once
	closeErr  error
}

func DirectQUICConfig(options ...QUICOptions) *quic.Config {
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

func DialQUIC(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, identity *secure.TLSIdentity, expectedPeerFingerprint string, options ...QUICOptions) (*QUICSession, error) {
	if conn == nil || remote == nil || identity == nil {
		return nil, errors.New("P2P QUIC dial requires a punched socket, peer address and TLS identity")
	}
	tlsConfig, err := clientTLSConfig(identity, expectedPeerFingerprint)
	if err != nil {
		return nil, err
	}
	transport := &quic.Transport{Conn: conn}
	qconn, err := transport.Dial(ctx, remote, tlsConfig, DirectQUICConfig(options...))
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("P2P QUIC dial failed: %w", err)
	}
	session := tunnel.NewQUICSession(qconn)
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return &QUICSession{QUICSession: session, conn: qconn, transport: transport}, nil
}

func AcceptQUIC(ctx context.Context, conn *net.UDPConn, identity *secure.TLSIdentity, expectedPeerFingerprint string, options ...QUICOptions) (*QUICSession, error) {
	if conn == nil || identity == nil {
		return nil, errors.New("P2P QUIC listen requires a punched socket and TLS identity")
	}
	tlsConfig, err := serverTLSConfig(identity, expectedPeerFingerprint)
	if err != nil {
		return nil, err
	}
	transport := &quic.Transport{Conn: conn}
	listener, err := transport.Listen(tlsConfig, DirectQUICConfig(options...))
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("P2P QUIC listen failed: %w", err)
	}
	qconn, err := listener.Accept(ctx)
	if err != nil {
		_ = listener.Close()
		_ = transport.Close()
		return nil, fmt.Errorf("P2P QUIC accept failed: %w", err)
	}
	// One Endpoint belongs to one Client/Exit P2P session. Stop accepting
	// additional connections while preserving the accepted connection.
	_ = listener.Close()
	session := tunnel.NewQUICSession(qconn)
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return &QUICSession{QUICSession: session, conn: qconn, transport: transport}, nil
}

type QUICStats struct {
	RTT           time.Duration
	BytesSent     uint64
	BytesReceived uint64
}

func (s *QUICSession) Stats() QUICStats {
	if s == nil || s.conn == nil {
		return QUICStats{}
	}
	stats := s.conn.ConnectionStats()
	return QUICStats{RTT: stats.SmoothedRTT, BytesSent: stats.BytesSent, BytesReceived: stats.BytesReceived}
}

func (s *QUICSession) Close() error {
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

func clientTLSConfig(identity *secure.TLSIdentity, expected string) (*tls.Config, error) {
	if identity == nil || len(identity.Certificate.Certificate) == 0 || expected == "" {
		return nil, errors.New("P2P QUIC client TLS identity or peer fingerprint is missing")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{QUICALPN},
		Certificates:       []tls.Certificate{identity.Certificate},
		InsecureSkipVerify: true, // authenticated out-of-band fingerprint pinning below replaces PKI hostname verification
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return secure.VerifyPeerFingerprint(rawCerts, expected)
		},
	}, nil
}

func serverTLSConfig(identity *secure.TLSIdentity, expected string) (*tls.Config, error) {
	if identity == nil || len(identity.Certificate.Certificate) == 0 || expected == "" {
		return nil, errors.New("P2P QUIC server TLS identity or peer fingerprint is missing")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{QUICALPN},
		Certificates: []tls.Certificate{identity.Certificate},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return secure.VerifyPeerFingerprint(rawCerts, expected)
		},
	}, nil
}

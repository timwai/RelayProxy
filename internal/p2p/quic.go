package p2p

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"github.com/quic-go/quic-go"
	direct "relayproxy/internal/direct"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const QUICALPN = "relayproxy-p2p-v1"

type QUICOptions = direct.QUICOptions
type QUICSession = direct.PacketSession
type QUICStats = direct.QUICStats

func DirectQUICConfig(options ...QUICOptions) *quic.Config {
	return direct.QUICConfig(options...)
}

func DialQUIC(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, identity *secure.TLSIdentity, expectedPeerFingerprint string, options ...QUICOptions) (*QUICSession, error) {
	if conn == nil || remote == nil || identity == nil {
		return nil, errors.New("P2P QUIC dial requires a punched socket, peer address and TLS identity")
	}
	tlsConfig, err := clientTLSConfig(identity, expectedPeerFingerprint)
	if err != nil {
		return nil, err
	}
	session, err := direct.DialPacket(ctx, conn, remote, tlsConfig, direct.QUICConfig(options...))
	if err != nil {
		return nil, err
	}
	tunnel.SetPeerCapabilities(session.QUICSession, []string{protocol.UDPModeDatagram})
	return session, nil
}

func AcceptQUIC(ctx context.Context, conn *net.UDPConn, identity *secure.TLSIdentity, expectedPeerFingerprint string, options ...QUICOptions) (*QUICSession, error) {
	if conn == nil || identity == nil {
		return nil, errors.New("P2P QUIC listen requires a punched socket and TLS identity")
	}
	tlsConfig, err := serverTLSConfig(identity, expectedPeerFingerprint)
	if err != nil {
		return nil, err
	}
	session, err := direct.AcceptPacket(ctx, conn, tlsConfig, direct.QUICConfig(options...))
	if err != nil {
		return nil, err
	}
	tunnel.SetPeerCapabilities(session.QUICSession, []string{protocol.UDPModeDatagram})
	return session, nil
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

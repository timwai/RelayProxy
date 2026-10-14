package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/apernet/quic-go"
	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/upnp"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const fileDirectQUICALPN = "relayproxy-file-direct-quic-v1"

// Each QUIC connection is file-scoped and has exactly one authenticated
// bidirectional stream. It cannot be reused for Proxy, RDP, or browser access.
type fileQUICStream struct {
	tunnel.TunnelStream
	session *tunnel.QUICSession
}

func (s *fileQUICStream) Close() error {
	streamErr := s.TunnelStream.Close()
	sessionErr := s.session.Close()
	return errors.Join(streamErr, sessionErr)
}

func (s *fileQUICStream) FileTransferPath() string { return "direct_quic" }

func fileDirectQUICServerTLS(config *tls.Config) *tls.Config {
	cfg := config.Clone()
	cfg.NextProtos = []string{fileDirectQUICALPN}
	cfg.MinVersion = tls.VersionTLS13
	return cfg
}

func fileDirectQUICConfig() *quic.Config {
	config := tunnel.DefaultQUICConfig()
	config.MaxIncomingStreams = 2
	config.MaxIncomingUniStreams = 0
	config.MaxIdleTimeout = 90 * time.Second
	config.KeepAlivePeriod = 10 * time.Second
	config.EnableDatagrams = false
	return config
}

// quicCandidates advertises the bound listener plus an optional valid UDP
// UPnP mapping. UPnP is not NAT hole punching and cannot bypass CGNAT.
func (r *fileDirectRuntime) quicCandidates() []string {
	if r.quicListener == nil {
		return nil
	}
	addr, ok := r.quicListener.Addr().(*net.UDPAddr)
	if !ok || addr.Port <= 0 {
		return nil
	}
	result := make([]string, 0, 16)
	// offer() calls this under r.mu. A mapped endpoint is only advertised
	// while its lease is healthy and the router reports public IPv4.
	if r.udpUPnPAddress.IsValid() && upnp.IsPublicWANIPv4(r.udpUPnPAddress.Addr()) {
		result = append(result, r.udpUPnPAddress.String())
	}
	for _, c := range candidate.Discover(addr.Port, 0) {
		if c.Protocol == "udp" {
			result = append(result, c.Address)
		}
	}
	if len(result) > 15 {
		result = result[:15]
	}
	// Explicit loopback is useful for two-Agent automated integration on a
	// single CI host; a remote peer still has to prove possession of a
	// Server-issued one-use ticket and pass the pinned TLS handshake.
	return append(result, net.JoinHostPort("127.0.0.1", fmt.Sprint(addr.Port)))
}

func (r *fileDirectRuntime) beginDirectHandler() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.wg.Add(1)
	return true
}

func (r *fileDirectRuntime) acceptQUIC() {
	defer r.wg.Done()
	const maxActiveQUIC = 16
	quota := make(chan struct{}, maxActiveQUIC)
	for {
		ctx := r.a.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		conn, err := r.quicListener.Accept(ctx)
		if err != nil {
			return
		}
		select {
		case quota <- struct{}{}:
			if !r.beginDirectHandler() {
				<-quota
				_ = conn.CloseWithError(0, "file direct host closed")
				return
			}
			go func() {
				defer r.wg.Done()
				defer func() { <-quota }()
				r.serveQUICConnection(conn)
			}()
		default:
			_ = conn.CloseWithError(0, "file direct host busy")
		}
	}
}

func (r *fileDirectRuntime) serveQUICConnection(conn *quic.Conn) {
	defer conn.CloseWithError(0, "file direct stream finished")
	ctx, cancel := context.WithTimeout(r.a.ctx, 5*time.Second)
	defer cancel()
	session := tunnel.NewQUICSession(conn)
	stream, err := session.AcceptStream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	var auth protocol.FileDirectAuth
	if err := protocol.ReadJSON(stream, &auth); err != nil {
		return
	}
	clientID, ok := r.consume(auth.Token)
	if !ok {
		_ = protocol.WriteJSON(stream, protocol.FileDirectAuthResult{Error: "file direct token rejected"})
		return
	}
	// All QUIC authorization is independent of the QUIC connection's TLS
	// authentication: the one-use ticket is attached to the Server-known ID.
	if !r.a.checkFileDirectGrant(r.a.ctx, clientID) {
		_ = protocol.WriteJSON(stream, protocol.FileDirectAuthResult{Error: "file direct approval revoked"})
		return
	}
	if err := protocol.WriteJSON(stream, protocol.FileDirectAuthResult{Allowed: true}); err != nil {
		return
	}
	r.quicAccepted.Add(1)
	_ = stream.SetDeadline(time.Time{})
	// A stale Server approval or lost control channel terminates the direct
	// connection even while a large file is being uploaded or downloaded.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-r.a.ctx.Done():
				_ = conn.CloseWithError(0, "file host disconnected")
				return
			case <-ticker.C:
				if !r.a.checkFileDirectGrant(r.a.ctx, clientID) {
					_ = conn.CloseWithError(0, "file direct authorization revoked")
					return
				}
			}
		}
	}()
	r.a.mu.RLock()
	store := r.a.fileShareStore
	r.a.mu.RUnlock()
	if store == nil {
		return
	}
	store.Serve(r.a.ctx, stream, clientID)
	// store.Serve closes the file stream after its final receipt is queued.
	// Do not immediately close the entire QUIC connection: that would
	// discard the queued file bytes/receipt before the remote reader has
	// consumed them. Wait for the client to close its session after receipt,
	// while bounding abandoned connections to 30 seconds.
	wait := time.NewTimer(30 * time.Second)
	defer wait.Stop()
	select {
	case <-conn.Context().Done():
	case <-r.a.ctx.Done():
	case <-wait.C:
	}
}

// tryFileQUIC consumes the same short-lived, Server-scoped ticket as direct
// TCP. When UDP is blocked it returns a transport error, allowing TCP and
// ultimately Relay to preserve connectivity without weakening permissions.
func (a *Agent) tryFileQUIC(ctx context.Context, offer protocol.FileDirectOffer, req protocol.FileShareRequest) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	var lastErr error = errors.New("no direct QUIC candidate reachable")
	for _, endpoint := range prioritizeFileDirectEndpoints(offer.UDPEndpoints, candidate.LocalInterfaceSubnets()) {
		if err := ctx.Err(); err != nil {
			return nil, protocol.FileShareResponse{}, err
		}
		addr, err := netip.ParseAddrPort(endpoint)
		if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() ||
			addr.Addr().IsMulticast() || addr.Addr().IsLinkLocalUnicast() {
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		tlsConfig := &tls.Config{
			MinVersion:         tls.VersionTLS13,
			ServerName:         "relayproxy-file-direct",
			NextProtos:         []string{fileDirectQUICALPN},
			InsecureSkipVerify: true, // pinned SHA-256 below, never skip authenticity
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) != 1 ||
					!validateFileDirectFingerprint(cs.PeerCertificates[0], offer.Fingerprint) ||
					cs.NegotiatedProtocol != fileDirectQUICALPN {
					return errors.New("file QUIC TLS certificate fingerprint mismatch")
				}
				return nil
			},
		}
		conn, err := quic.DialAddr(attemptCtx, endpoint, tlsConfig, fileDirectQUICConfig())
		if err != nil {
			lastErr = err
			cancel()
			continue
		}
		session := tunnel.NewQUICSession(conn)
		stream, err := session.OpenStream(attemptCtx)
		cancel()
		if err != nil {
			lastErr = err
			_ = session.Close()
			continue
		}
		_ = stream.SetDeadline(fileDirectHandshakeDeadline(ctx, 5*time.Second))
		if err := protocol.WriteJSON(stream, protocol.FileDirectAuth{Token: offer.Token}); err != nil {
			lastErr = err
			_ = stream.Close()
			_ = session.Close()
			continue
		}
		var approved protocol.FileDirectAuthResult
		if err := protocol.ReadJSON(stream, &approved); err != nil || !approved.Allowed {
			if err == nil {
				err = errors.New("file QUIC direct authorization denied")
			}
			lastErr = err
			_ = stream.Close()
			_ = session.Close()
			continue
		}
		if req.Path == "" {
			req.Path = "."
		}
		if err := protocol.WriteJSON(stream, req); err != nil {
			lastErr = err
			_ = stream.Close()
			_ = session.Close()
			continue
		}
		var response protocol.FileShareResponse
		if err := protocol.ReadJSON(stream, &response); err != nil || !response.Success {
			if err == nil {
				err = fmt.Errorf("file QUIC request rejected: %s", response.Error)
			}
			lastErr = err
			_ = stream.Close()
			_ = session.Close()
			continue
		}
		_ = stream.SetDeadline(time.Time{})
		return &fileQUICStream{TunnelStream: stream, session: session}, response, nil
	}
	return nil, protocol.FileShareResponse{}, lastErr
}

var _ tunnel.TunnelStream = (*fileQUICStream)(nil)

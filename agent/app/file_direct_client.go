package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// tryFileDirect negotiates fresh file-scoped credentials over the already
// authenticated Relay session. TLS pinning + one-time tickets authenticate
// the destination independently of proxy, RDP or Browser Sync privileges.
// Short directory/metadata reads should never spend four seconds probing
// an unreachable NAT. Reserve socket-per-transfer P2P for actual file bytes.
func fileDirectNeedsPunch(action string) bool {
	return action == protocol.FileShareDownload || action == protocol.FileShareUpload
}

func (a *Agent) tryFileDirect(ctx context.Context, hostID string, req protocol.FileShareRequest) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	return a.tryFileDirectMode(ctx, hostID, req, fileDirectNeedsPunch(req.Action))
}

func (a *Agent) tryFileDirectMode(ctx context.Context, hostID string, req protocol.FileShareRequest, allowPunch bool) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	session, _, err := a.fileSharingSession()
	if err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	offerCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	control, err := session.OpenStream(offerCtx)
	if err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	defer control.Close()
	_ = control.SetDeadline(time.Now().Add(3 * time.Second))
	var punchConn *net.UDPConn
	var punchCandidates []protocol.P2PCandidate
	if allowPunch {
		punchConn, punchCandidates = a.prepareFilePunch(offerCtx)
	}
	defer func() {
		if punchConn != nil {
			_ = punchConn.Close()
		}
	}()
	frameType := protocol.FrameTypeFileDirectPrepare
	if len(punchCandidates) > 0 {
		frameType = protocol.FrameTypeFileDirectPunchPrepare
	}
	if err := protocol.WriteStreamHeader(control, &protocol.StreamHeader{
		Type: frameType, ExitDeviceID: hostID,
	}); err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	if frameType == protocol.FrameTypeFileDirectPunchPrepare {
		if err := protocol.WriteJSON(control, protocol.FileDirectPunchPrepare{Candidates: punchCandidates}); err != nil {
			return nil, protocol.FileShareResponse{}, err
		}
	}
	var offer protocol.FileDirectOffer
	if err := protocol.ReadJSON(control, &offer); err != nil {
		// Mixed-version Servers do not recognize the new punch prepare frame.
		// Retry the established file-only direct protocol once, never proxy P2P.
		if len(punchCandidates) > 0 {
			return a.tryFileDirectMode(ctx, hostID, req, false)
		}
		return nil, protocol.FileShareResponse{}, err
	}
	if offer.Error != "" {
		if len(punchCandidates) > 0 {
			return a.tryFileDirectMode(ctx, hostID, req, false)
		}
		return nil, protocol.FileShareResponse{}, errors.New(offer.Error)
	}
	if len(offer.Token) != 64 || len(offer.Fingerprint) != 64 ||
		len(offer.Endpoints) == 0 || len(offer.Endpoints) > 16 ||
		len(offer.UDPEndpoints) > 16 || len(offer.PunchCandidates) > candidate.MaxCandidates ||
		offer.ExpiresAt <= time.Now().UnixMilli() {
		return nil, protocol.FileShareResponse{}, errors.New("invalid file direct offer")
	}
	if punchConn != nil && len(offer.PunchCandidates) > 0 && offer.PunchSessionID != 0 &&
		len(offer.PunchKey) == 32 {
		punchCtx, stopPunch := fileDirectStageContext(ctx, offer.ExpiresAt, 4*time.Second)
		stream, response, punchErr := a.tryPunchedFileQUIC(punchCtx, punchConn, offer, req)
		stopPunch()
		if punchErr == nil {
			// The authenticated file P2P QUIC transport now owns this socket.
			punchConn = nil
			return stream, response, nil
		}
	}
	// A single 20-second Server ticket must cover QUIC, TCP and fallback.
	// Sequentially trying up to 16 unreachable UDP candidates could otherwise
	// consume the entire ticket before the TCP path gets a chance.
	// Independent, short stage budgets keep the Relay path responsive.
	if len(offer.UDPEndpoints) > 0 {
		quicCtx, cancelQUIC := fileDirectStageContext(ctx, offer.ExpiresAt, 4*time.Second)
		stream, response, quicErr := a.tryFileQUIC(quicCtx, offer, req)
		cancelQUIC()
		if quicErr == nil {
			return stream, response, nil
		}
	}
	tcpCtx, cancelTCP := fileDirectStageContext(ctx, offer.ExpiresAt, 4*time.Second)
	defer cancelTCP()
	var lastErr error
	for _, endpoint := range prioritizeFileDirectEndpoints(offer.Endpoints, candidate.LocalInterfaceSubnets()) {
		if err := tcpCtx.Err(); err != nil {
			return nil, protocol.FileShareResponse{}, err
		}
		dialCtx, stop := context.WithTimeout(tcpCtx, 800*time.Millisecond)
		tcpConn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", endpoint)
		if err != nil {
			lastErr = err
			stop()
			continue
		}
		tlsCfg := &tls.Config{
			ServerName:         "relayproxy-file-direct",
			MinVersion:         tls.VersionTLS13,
			NextProtos:         []string{fileDirectALPN},
			InsecureSkipVerify: true, // the Server-attested SHA-256 fingerprint is checked below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) != 1 ||
					!validateFileDirectFingerprint(cs.PeerCertificates[0], offer.Fingerprint) ||
					cs.NegotiatedProtocol != fileDirectALPN {
					return errors.New("untrusted file direct TLS certificate")
				}
				return nil
			},
		}
		conn := tls.Client(tcpConn, tlsCfg)
		err = conn.HandshakeContext(dialCtx)
		stop()
		if err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		_ = conn.SetDeadline(fileDirectHandshakeDeadline(tcpCtx, 4*time.Second))
		auth := protocol.FileDirectAuth{Token: offer.Token}
		if err = protocol.WriteJSON(conn, auth); err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		var approved protocol.FileDirectAuthResult
		if err = protocol.ReadJSON(conn, &approved); err != nil || !approved.Allowed {
			if err == nil {
				err = errors.New("file direct host denied authorization")
			}
			lastErr = err
			_ = conn.Close()
			continue
		}
		if req.Path == "" {
			req.Path = "."
		}
		if err = protocol.WriteJSON(conn, req); err != nil {
			lastErr = err
			_ = conn.Close()
			continue
		}
		var response protocol.FileShareResponse
		if err = protocol.ReadJSON(conn, &response); err != nil || !response.Success {
			if err == nil {
				err = fmt.Errorf("file direct request rejected: %s", response.Error)
			}
			lastErr = err
			_ = conn.Close()
			continue
		}
		_ = conn.SetDeadline(time.Time{})
		return &fileDirectStream{conn}, response, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no file direct candidate reachable")
	}
	return nil, protocol.FileShareResponse{}, lastErr
}

// Compile-time assurance: no unrestricted net.Conn can be used without an
// authenticated TLS session and file-scoped direct protocol adapter.
var _ tunnel.TunnelStream = (*fileDirectStream)(nil)

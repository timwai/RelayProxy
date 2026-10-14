package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/apernet/quic-go"
	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/punch"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const filePunchTimeout = 4 * time.Second

// Each file gets its own UDP socket: rendezvous, authenticated HMAC punching
// and QUIC all use that SAME socket/NAT binding, never the proxy P2P endpoint.
func (a *Agent) prepareFilePunch(ctx context.Context) (*net.UDPConn, []protocol.P2PCandidate) {
	a.mu.RLock()
	rendezvous := a.fileP2PRendezvous
	a.mu.RUnlock()
	if rendezvous == "" {
		return nil, nil
	}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, nil
	}
	tunnel.TuneUDPConn(conn)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	items := candidate.DiscoverWithLimit(port, 0, candidate.MaxCandidates-2)
	probeCtx, cancel := context.WithTimeout(ctx, 850*time.Millisecond)
	reflexive, err := candidate.ProbeReflexiveAll(probeCtx, rendezvous, conn, "udp")
	cancel()
	if err == nil {
		items = mergeFilePunchCandidates(items, reflexive)
	}
	validated, err := candidate.Validate(items)
	if err != nil || len(validated) == 0 {
		_ = conn.Close()
		return nil, nil
	}
	return conn, validated
}

// Ignore individual invalid reflection responses (e.g. 127.0.0.1 from a
// localhost integration rendezvous) without losing the valid LAN candidates.
// A bad IP family or CGNAT hint must not disable a reachable same-LAN route.
func mergeFilePunchCandidates(local, reflexive []protocol.P2PCandidate) []protocol.P2PCandidate {
	result := append([]protocol.P2PCandidate(nil), local...)
	for _, item := range reflexive {
		valid, err := candidate.Validate([]protocol.P2PCandidate{item})
		if err == nil && len(valid) == 1 && len(result) < candidate.MaxCandidates {
			result = append(result, valid[0])
		}
	}
	return result
}

func (r *fileDirectRuntime) startFilePunch(grant protocol.FileDirectGrant) ([]protocol.P2PCandidate, error) {
	if grant.PunchSessionID == 0 || len(grant.PunchKey) != 32 {
		return nil, errors.New("invalid file-only punch authorization")
	}
	// A host must not trust unvalidated endpoint metadata received through a
	// Relay stream. It uses HMAC packets but still bounds network probing.
	peers, err := candidate.Validate(grant.PeerCandidates)
	if err != nil || len(peers) == 0 {
		return nil, errors.New("invalid file peer candidates")
	}
	parent := r.a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.closed || len(r.punchCancel) >= 4 || time.Now().UnixMilli() >= grant.ExpiresAt {
		r.mu.Unlock()
		cancel()
		return nil, errors.New("file punch runtime unavailable")
	}
	r.punchCancel[grant.Token] = cancel
	r.wg.Add(1)
	r.mu.Unlock()
	conn, candidates := r.a.prepareFilePunch(ctx)
	if conn == nil {
		r.mu.Lock()
		delete(r.punchCancel, grant.Token)
		r.mu.Unlock()
		r.wg.Done()
		cancel()
		return nil, errors.New("file punch rendezvous unavailable")
	}
	go func() {
		defer r.wg.Done()
		defer cancel()
		defer conn.Close()
		defer func() {
			r.mu.Lock()
			delete(r.punchCancel, grant.Token)
			r.mu.Unlock()
		}()
		r.runPunchedFileQUIC(ctx, conn, peers, grant)
	}()
	return candidates, nil
}

func (r *fileDirectRuntime) runPunchedFileQUIC(ctx context.Context, socket *net.UDPConn, peers []protocol.P2PCandidate, grant protocol.FileDirectGrant) {
	punchCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(grant.ExpiresAt))
	defer cancel()
	result, err := punch.Punch(punchCtx, socket, peers, grant.PunchSessionID, grant.PunchKey, filePunchTimeout)
	if err != nil {
		return
	}
	transport := &quic.Transport{Conn: result.Conn}
	defer transport.Close()
	listener, err := transport.Listen(fileDirectQUICServerTLS(r.tlsConfig), fileDirectQUICConfig())
	if err != nil {
		return
	}
	conn, err := listener.Accept(punchCtx)
	_ = listener.Close()
	if err != nil {
		return
	}
	// Runtime shutdown must close an already-established file P2P QUIC
	// connection, not only stop future HMAC punches and ticket admission.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.CloseWithError(0, "file punch runtime stopped")
		case <-conn.Context().Done():
		case <-stopWatch:
		}
	}()
	r.punchAccepted.Add(1)
	r.serveQUICConnection(conn)
}

type filePunchedQUICStream struct {
	*fileQUICStream
	transport *quic.Transport
}

func (s *filePunchedQUICStream) FileTransferPath() string { return "p2p_quic" }

func (s *filePunchedQUICStream) Close() error {
	return errors.Join(s.fileQUICStream.Close(), s.transport.Close())
}

// QUIC's TLS certificate is pinned to the Server-attested file-host leaf,
// and the same one-use token and local ACL are verified as regular file QUIC.
func (a *Agent) tryPunchedFileQUIC(ctx context.Context, socket *net.UDPConn, offer protocol.FileDirectOffer, req protocol.FileShareRequest) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
	peers, err := candidate.Validate(offer.PunchCandidates)
	if err != nil || len(peers) == 0 {
		return nil, protocol.FileShareResponse{}, errors.New("invalid file peer punch candidates")
	}
	result, err := punch.Punch(ctx, socket, peers, offer.PunchSessionID, offer.PunchKey, filePunchTimeout)
	if err != nil {
		return nil, protocol.FileShareResponse{}, err
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         "relayproxy-file-direct",
		NextProtos:         []string{fileDirectQUICALPN},
		InsecureSkipVerify: true, // leaf SHA-256 is pinned below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 ||
				!validateFileDirectFingerprint(cs.PeerCertificates[0], offer.Fingerprint) ||
				cs.NegotiatedProtocol != fileDirectQUICALPN {
				return errors.New("untrusted punched file QUIC peer")
			}
			return nil
		},
	}
	transport := &quic.Transport{Conn: result.Conn}
	qconn, err := transport.Dial(ctx, result.RemoteAddr, tlsCfg, fileDirectQUICConfig())
	if err != nil {
		_ = transport.Close()
		return nil, protocol.FileShareResponse{}, err
	}
	session := tunnel.NewQUICSession(qconn)
	stream, err := session.OpenStream(ctx)
	if err != nil {
		_ = session.Close()
		_ = transport.Close()
		return nil, protocol.FileShareResponse{}, err
	}
	fail := func(err error) (tunnel.TunnelStream, protocol.FileShareResponse, error) {
		_ = stream.Close()
		_ = session.Close()
		_ = transport.Close()
		return nil, protocol.FileShareResponse{}, err
	}
	_ = stream.SetDeadline(fileDirectHandshakeDeadline(ctx, 5*time.Second))
	if err := protocol.WriteJSON(stream, protocol.FileDirectAuth{Token: offer.Token}); err != nil {
		return fail(err)
	}
	var approved protocol.FileDirectAuthResult
	if err := protocol.ReadJSON(stream, &approved); err != nil {
		return fail(err)
	}
	if !approved.Allowed {
		return fail(errors.New("file-only P2P authorization denied"))
	}
	if req.Path == "" {
		req.Path = "."
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return fail(err)
	}
	var response protocol.FileShareResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return fail(err)
	}
	if !response.Success {
		return fail(fmt.Errorf("punched file request rejected: %s", response.Error))
	}
	_ = stream.SetDeadline(time.Time{})
	return &filePunchedQUICStream{
		fileQUICStream: &fileQUICStream{TunnelStream: stream, session: session},
		transport:      transport,
	}, response, nil
}

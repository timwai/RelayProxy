package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type GatewayConfig struct {
	TCPAddr                 string // e.g. ":443"
	QUICAddr                string // e.g. ":443"
	TLSConfig               *tls.Config
	ServerInstanceID        string
	AuthorizeDevice         func(fingerprint string, hello protocol.DeviceHello) (DeviceAuthorization, error)
	RecheckDevice           func(fingerprint, deviceID string) bool
	OnDeviceConnected       func(deviceID string)
	OnDeviceDisconnected    func(deviceID string)
	MaxConnections          int // global tunnel connection limit
	MaxConnectionsPerDevice int // per-device concurrent streams (also sent in Welcome)
	HeartbeatSec            int
	RendezvousAddress       string
	RDPLeaseSec             int
	HandshakeTimeout        time.Duration // covers control stream/header/Hello/Welcome
}

type DeviceAuthorization struct {
	State                string
	DeviceID             string
	OwnerUserID          string
	ApprovedCapabilities []string
	RDPTargets           []protocol.RDPTarget
	RemoteDesktopTargets []protocol.RemoteDesktopTarget
}

const deviceRejectionDrainTimeout = time.Second

// writeDeviceRejection half-closes the control stream after the framed
// rejection is fully queued, then gives the peer a short bounded window to
// observe it and finish its in-flight write before the enclosing session is
// torn down. This avoids turning a protocol-level rejection into an occasional
// transport-level "session shutdown" race.
func writeDeviceRejection(stream tunnel.TunnelStream, response protocol.DeviceAccepted) {
	if stream == nil {
		return
	}
	_ = stream.SetWriteDeadline(time.Now().Add(deviceRejectionDrainTimeout))
	if err := protocol.WriteJSON(stream, response); err != nil {
		return
	}
	_ = stream.CloseWrite()
	_ = stream.SetReadDeadline(time.Now().Add(deviceRejectionDrainTimeout))
	var one [1]byte
	_, _ = stream.Read(one[:])
}

type Gateway struct {
	cfg          GatewayConfig
	sessions     *session.Manager
	router       *StreamRouter
	tcpListener  net.Listener
	quicListener *quic.Listener
	closed       atomic.Bool
	activeConns  atomic.Int64
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	started      bool
	closeOnce    sync.Once
	resources    map[io.Closer]struct{} // includes transports that are not authenticated yet
}

func NewGateway(cfg GatewayConfig, sessions *session.Manager, router *StreamRouter) *Gateway {
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 2048
	}
	if cfg.MaxConnectionsPerDevice <= 0 {
		cfg.MaxConnectionsPerDevice = 1024
	}
	if cfg.HeartbeatSec <= 0 {
		cfg.HeartbeatSec = 15
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 15 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Gateway{
		cfg:       cfg,
		sessions:  sessions,
		router:    router,
		ctx:       ctx,
		cancel:    cancel,
		resources: make(map[io.Closer]struct{}),
	}
}

func (g *Gateway) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed.Load() {
		return errors.New("gateway closed")
	}
	if g.started {
		return errors.New("gateway already started")
	}
	if g.cfg.TLSConfig == nil && g.cfg.QUICAddr != "" {
		return errors.New("TLSConfig is required for QUIC listeners")
	}
	if g.cfg.TCPAddr == "" && g.cfg.QUICAddr == "" {
		return errors.New("at least one gateway listener must be configured")
	}
	if g.cfg.TLSConfig != nil {
		g.cfg.TLSConfig = g.cfg.TLSConfig.Clone()
		g.cfg.TLSConfig.MinVersion = tls.VersionTLS13
	}

	// 1. Start TCP / TLS Listener
	if g.cfg.TCPAddr != "" {
		l, err := net.Listen("tcp", g.cfg.TCPAddr)
		if err != nil {
			return fmt.Errorf("tcp listen failed: %w", err)
		}
		g.tcpListener = l
		log.Printf("[Gateway] TLS TCP Gateway listening on %s", l.Addr().String())

	}

	// 2. Start QUIC Listener
	if g.cfg.QUICAddr != "" {
		quicTLS := g.cfg.TLSConfig.Clone()
		if len(quicTLS.NextProtos) == 0 {
			quicTLS.NextProtos = []string{"relayproxy-quic"}
		}
		quicCfg := tunnel.DefaultQUICConfig()
		ql, err := quic.ListenAddr(g.cfg.QUICAddr, quicTLS, quicCfg)
		if err != nil {
			if g.tcpListener != nil {
				_ = g.tcpListener.Close()
				g.tcpListener = nil
			}
			return fmt.Errorf("quic listen failed: %w", err)
		} else {
			g.quicListener = ql
			log.Printf("[Gateway] QUIC Gateway listening on %s", ql.Addr().String())

		}
	}
	g.started = true
	if g.tcpListener != nil {
		g.wg.Add(1)
		go g.serveTCP()
	}
	if g.quicListener != nil {
		g.wg.Add(1)
		go g.serveQUIC()
	}

	return nil
}

func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		g.mu.Lock()
		g.closed.Store(true)
		g.cancel()
		resources := make([]io.Closer, 0, len(g.resources))
		for resource := range g.resources {
			resources = append(resources, resource)
		}
		tcpListener, quicListener := g.tcpListener, g.quicListener
		g.mu.Unlock()
		if tcpListener != nil {
			_ = tcpListener.Close()
		}
		if quicListener != nil {
			_ = quicListener.Close()
		}
		for _, resource := range resources {
			_ = resource.Close()
		}
		g.sessions.CloseAll()
		// All control handlers, request handlers and their audit producers finish
		// before the caller is allowed to close its audit channel or database.
		g.wg.Wait()
	})
	return nil
}

// TCPAddr and QUICAddr expose the actual bound addresses, including dynamic ports.
func (g *Gateway) TCPAddr() net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tcpListener == nil {
		return nil
	}
	return g.tcpListener.Addr()
}

func (g *Gateway) QUICAddr() net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.quicListener == nil {
		return nil
	}
	return g.quicListener.Addr()
}

func (g *Gateway) track(resource io.Closer) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed.Load() {
		return false
	}
	g.resources[resource] = struct{}{}
	return true
}

func (g *Gateway) untrack(resource io.Closer) {
	g.mu.Lock()
	delete(g.resources, resource)
	g.mu.Unlock()
}

func (g *Gateway) reserveConnection() bool {
	if g.activeConns.Add(1) > int64(g.cfg.MaxConnections) {
		g.activeConns.Add(-1)
		return false
	}
	return true
}

func (g *Gateway) serveTCP() {
	defer g.wg.Done()
	for {
		rawConn, err := g.tcpListener.Accept()
		if err != nil {
			if g.closed.Load() {
				return
			}
			log.Printf("[Gateway] TCP accept error: %v", err)
			continue
		}
		tunnel.TuneTCPConn(rawConn)

		if !g.reserveConnection() {
			log.Printf("[Gateway] Connection limit reached (%d), rejecting %s", g.cfg.MaxConnections, rawConn.RemoteAddr())
			_ = rawConn.Close()
			continue
		}

		if !g.track(rawConn) {
			g.activeConns.Add(-1)
			_ = rawConn.Close()
			return
		}
		g.wg.Add(1)
		go func(c net.Conn) {
			defer g.wg.Done()
			defer g.activeConns.Add(-1)
			defer g.untrack(c)
			defer c.Close()

			var sessionConn net.Conn = c
			if g.cfg.TLSConfig != nil {
				tlsConn := tls.Server(c, g.cfg.TLSConfig)
				handshakeCtx, cancel := context.WithTimeout(g.ctx, g.cfg.HandshakeTimeout)
				err := tlsConn.HandshakeContext(handshakeCtx)
				cancel()
				if err != nil {
					_ = c.Close()
					return
				}
				sessionConn = tlsConn
			}

			session, err := tunnel.ServerTLS(sessionConn, nil)
			if err != nil {
				log.Printf("[Gateway] TLS session init failed: %v", err)
				_ = sessionConn.Close()
				return
			}
			g.handleSession(session)
		}(rawConn)
	}
}

func (g *Gateway) serveQUIC() {
	defer g.wg.Done()
	for {
		conn, err := g.quicListener.Accept(g.ctx)
		if err != nil {
			if g.closed.Load() {
				return
			}
			log.Printf("[Gateway] QUIC accept error: %v", err)
			continue
		}

		if !g.reserveConnection() {
			log.Printf("[Gateway] Connection limit reached (%d), rejecting QUIC %s", g.cfg.MaxConnections, conn.RemoteAddr())
			_ = conn.CloseWithError(1, "connection limit reached")
			continue
		}

		g.wg.Add(1)
		go func(c *quic.Conn) {
			defer g.wg.Done()
			defer g.activeConns.Add(-1)
			session := tunnel.NewQUICSession(c)
			g.handleSession(session)
		}(conn)
	}
}

func (g *Gateway) handleSession(sess tunnel.TunnelSession) {
	defer sess.Close()
	if !g.track(sess) {
		return
	}
	defer g.untrack(sess)
	sessionCtx, cancelSession := context.WithCancel(g.ctx)
	sessionWatcherDone := make(chan struct{})
	go func() {
		defer close(sessionWatcherDone)
		select {
		case <-sess.Done():
			cancelSession()
		case <-sessionCtx.Done():
		}
	}()
	defer func() {
		cancelSession()
		<-sessionWatcherDone
	}()
	handshakeDeadline := time.Now().Add(g.cfg.HandshakeTimeout)

	// 1. Accept control stream identified by FrameTypeControl (not arrival order)
	ctrlStream, err := g.acceptControlStream(sess, handshakeDeadline)
	if err != nil {
		log.Printf("[Gateway] Failed to accept control stream from %s: %v", sess.RemoteAddr(), err)
		return
	}

	// 2. Read the installation identity. This protocol intentionally has no
	// device ID, token, pairing code, or downgrade path.
	var hello protocol.DeviceHello
	if err := protocol.ReadJSON(ctrlStream, &hello); err != nil {
		log.Printf("[Gateway] Failed to read DeviceHello from %s: %v", sess.RemoteAddr(), err)
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "invalid device hello",
		})
		return
	}
	if hello.ProtocolVersion != protocol.DeviceProtocolVersion {
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeProtocolMismatch, ErrorMessage: "unsupported device protocol version",
		})
		return
	}
	if hello.InstallationID == "" || len(hello.PublicKey) != ed25519.PublicKeySize || len(hello.ClientNonce) != 32 {
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "incomplete device identity",
		})
		return
	}
	if g.cfg.ServerInstanceID == "" || g.cfg.AuthorizeDevice == nil {
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeInternalError, ErrorMessage: "device authentication is not configured",
		})
		return
	}
	serverNonce := make([]byte, 32)
	if _, err := rand.Read(serverNonce); err != nil {
		return
	}
	challenge := protocol.AuthChallenge{
		ProtocolVersion: protocol.DeviceProtocolVersion, ChallengeID: uuid.NewString(),
		ServerInstanceID: g.cfg.ServerInstanceID, ServerNonce: serverNonce,
		ExpiresAt: time.Now().Add(30 * time.Second).Unix(),
	}
	if err := protocol.WriteJSON(ctrlStream, challenge); err != nil {
		return
	}
	var proof protocol.AuthProof
	if err := protocol.ReadJSON(ctrlStream, &proof); err != nil || proof.ChallengeID != challenge.ChallengeID ||
		time.Now().Unix() > challenge.ExpiresAt || len(proof.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(hello.PublicKey), protocol.DeviceAuthPayload(hello, challenge), proof.Signature) {
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeAuthFailed, ErrorMessage: "device signature verification failed",
		})
		return
	}
	keyHash := sha256.Sum256(hello.PublicKey)
	fingerprint := hex.EncodeToString(keyHash[:])
	authorization, err := g.cfg.AuthorizeDevice(fingerprint, hello)
	if err != nil {
		log.Printf("[Gateway] Failed to resolve device approval for %s: %v", fingerprint, err)
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "rejected", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeInternalError, ErrorMessage: "failed to resolve device approval",
		})
		return
	}
	if authorization.State != "approved" {
		errorCode, message, retry := protocol.ErrCodeDeviceRejected, "device enrollment was rejected", 0
		if authorization.State == "pending" {
			errorCode, message, retry = protocol.ErrCodeApprovalPending, "waiting for server administrator approval", 15
		} else if authorization.State == "revoked" {
			errorCode, message = protocol.ErrCodeDeviceRevoked, "device access was revoked"
		}
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: authorization.State, ServerTime: time.Now().Unix(), RetryAfterSec: retry,
			ErrorCode: errorCode, ErrorMessage: message,
		})
		return
	}

	// 3. The server-selected grant controls runtime capabilities.
	tunnel.SetPeerCapabilities(sess, hello.TransportCapabilities)
	capabilities := []string{protocol.UDPModeStream}
	if tunnel.SupportsDatagrams(sess) {
		capabilities = append(capabilities, protocol.UDPModeDatagram)
	}
	sessionID := "sess_" + uuid.New().String()
	welcome := protocol.DeviceAccepted{
		State:                 "approved",
		DeviceID:              authorization.DeviceID,
		ApprovedCapabilities:  authorization.ApprovedCapabilities,
		RDPTargets:            authorization.RDPTargets,
		RemoteDesktopTargets:  authorization.RemoteDesktopTargets,
		SessionID:             sessionID,
		HeartbeatSec:          g.cfg.HeartbeatSec,
		MaxConnections:        g.cfg.MaxConnectionsPerDevice,
		ServerTime:            time.Now().Unix(),
		Success:               true,
		TransportCapabilities: capabilities,
		RendezvousAddress:     g.cfg.RendezvousAddress,
		RDPLeaseSec:           g.cfg.RDPLeaseSec,
	}

	desktopCapabilities := protocol.DesktopCapabilities{}
	if hello.DesktopCapabilities != nil {
		desktopCapabilities = *hello.DesktopCapabilities
		desktopCapabilities.Captures = append([]protocol.DesktopCaptureCapability(nil), hello.DesktopCapabilities.Captures...)
		desktopCapabilities.Codecs = append([]protocol.DesktopCodecCapability(nil), hello.DesktopCapabilities.Codecs...)
		desktopCapabilities.AudioCodecs = append([]string(nil), hello.DesktopCapabilities.AudioCodecs...)
		desktopCapabilities.Displays = append([]protocol.DesktopDisplayCapability(nil), hello.DesktopCapabilities.Displays...)
	}
	deviceSession := &session.DeviceSession{
		DeviceID:            authorization.DeviceID,
		DeviceName:          hello.DeviceName,
		OwnerUserID:         authorization.OwnerUserID,
		Mode:                modeForCapabilities(authorization.ApprovedCapabilities),
		Capabilities:        hello.TransportCapabilities,
		Grants:              authorization.ApprovedCapabilities,
		DesktopCapabilities: desktopCapabilities,
		Transport:           sess.Transport(),
		Tunnel:              sess,
		ControlStream:       ctrlStream,
		ConnectedAt:         time.Now(),
	}

	g.mu.Lock()
	if g.closed.Load() {
		g.mu.Unlock()
		return
	}
	registered := g.sessions.RegisterAuthenticated(deviceSession, func() bool {
		return g.cfg.RecheckDevice == nil || g.cfg.RecheckDevice(fingerprint, authorization.DeviceID)
	})
	g.mu.Unlock()
	if !registered {
		writeDeviceRejection(ctrlStream, protocol.DeviceAccepted{
			Success: false, State: "revoked", ServerTime: time.Now().Unix(),
			ErrorCode: protocol.ErrCodeDeviceRevoked, ErrorMessage: "device approval was revoked during handshake",
		})
		return
	}
	ready := false
	defer func() {
		removed := g.sessions.UnregisterSession(deviceSession)
		if ready && removed && g.cfg.OnDeviceDisconnected != nil {
			g.cfg.OnDeviceDisconnected(deviceSession.DeviceID)
		}
		log.Printf("[Gateway] Device disconnected: id=%s", authorization.DeviceID)
	}()
	// Welcome means the authenticated session is already routable. Failed writes
	// roll registration back through the session-specific defer above.
	if err := protocol.WriteJSON(ctrlStream, welcome); err != nil {
		log.Printf("[Gateway] Failed to send acceptance to %s: %v", authorization.DeviceID, err)
		return
	}
	if err := ctrlStream.SetDeadline(time.Time{}); err != nil {
		return
	}
	ready = true
	log.Printf("[Gateway] Device connected: id=%s name=%q capabilities=%v transport=%s remote=%s",
		authorization.DeviceID, hello.DeviceName, authorization.ApprovedCapabilities, sess.Transport(), sess.RemoteAddr())
	if g.cfg.OnDeviceConnected != nil {
		g.cfg.OnDeviceConnected(deviceSession.DeviceID)
	}

	// 4. Run control channel monitor in background
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.handleControlChannel(deviceSession)
	}()

	// 5. Accept data / request streams from this session
	var admittedStreams atomic.Int64
	for {
		stream, err := sess.AcceptStream(sessionCtx)
		if err != nil {
			return
		}

		if admittedStreams.Add(1) > int64(g.cfg.MaxConnectionsPerDevice) {
			admittedStreams.Add(-1)
			log.Printf("[Gateway] Per-device stream limit reached for %s (%d), closing stream",
				deviceSession.DeviceID, g.cfg.MaxConnectionsPerDevice)
			_ = stream.Close()
			continue
		}

		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			defer admittedStreams.Add(-1)
			if g.router == nil {
				_ = stream.Close()
				return
			}
			g.router.HandleClientStream(sessionCtx, stream, deviceSession)
		}()
	}
}

func containsCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func modeForCapabilities(capabilities []string) string {
	client := containsCapability(capabilities, protocol.CapabilityProxyClient)
	exit := containsCapability(capabilities, protocol.CapabilityProxyExit)
	if client && exit {
		return "BOTH"
	}
	if exit {
		return "EXIT"
	}
	return "CLIENT"
}

// acceptControlStream loops AcceptStream until a FrameTypeControl header is seen.
// Premature data streams (race before Hello) are closed and discarded.
func (g *Gateway) acceptControlStream(sess tunnel.TunnelSession, deadline time.Time) (tunnel.TunnelStream, error) {
	for {
		if g.closed.Load() {
			return nil, errors.New("gateway closed")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errors.New("timeout waiting for control stream")
		}
		ctx, cancel := context.WithTimeout(g.ctx, remaining)
		stream, err := sess.AcceptStream(ctx)
		cancel()
		if err != nil {
			return nil, err
		}
		if err := stream.SetDeadline(deadline); err != nil {
			_ = stream.Close()
			return nil, err
		}

		header, err := protocol.ReadStreamHeader(stream)
		if err != nil {
			log.Printf("[Gateway] Discarding stream with invalid header from %s: %v", sess.RemoteAddr(), err)
			_ = stream.Close()
			continue
		}
		if header.Type == protocol.FrameTypeControl {
			return stream, nil
		}
		log.Printf("[Gateway] Discarding premature data stream (type=%d) before handshake from %s",
			header.Type, sess.RemoteAddr())
		_ = stream.Close()
	}
}

func (g *Gateway) handleControlChannel(dev *session.DeviceSession) {
	defer dev.Tunnel.Close()
	heartbeatTimeout := 3*time.Duration(g.cfg.HeartbeatSec)*time.Second + 5*time.Second
	for {
		_ = dev.ControlStream.SetReadDeadline(time.Now().Add(heartbeatTimeout))
		var ping protocol.PingMessage
		if err := protocol.ReadJSON(dev.ControlStream, &ping); err != nil {
			return
		}
		dev.TouchHeartbeat()

		// Respond with Pong
		_ = dev.ControlStream.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := protocol.WriteJSON(dev.ControlStream, protocol.PongMessage{
			Timestamp: time.Now().Unix(),
		}); err != nil {
			return
		}
	}
}

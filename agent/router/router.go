package router

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/agent/exit"
	proxyp2p "relayproxy/agent/p2p"
	"relayproxy/internal/acl"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type Config struct {
	ServerAddress  string
	ServerName     string
	TCPPort        int
	IdentityID     string
	DeviceName     string
	Identity       *deviceidentity.Identity
	TLSRootCAs     *x509.CertPool
	ConnectTimeout time.Duration
	MaxConnections int
	P2PUPnPAllowed bool // Explicit local opt-in for temporary UPnP UDP mapping
}

type Router struct {
	cfg     Config
	handler *exit.Handler
	running atomic.Bool
}

func New(cfg Config) (*Router, error) {
	cfg.ServerAddress = strings.TrimSpace(cfg.ServerAddress)
	cfg.ServerName = strings.TrimSpace(cfg.ServerName)
	cfg.IdentityID = strings.ToLower(strings.TrimSpace(cfg.IdentityID))
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	if cfg.ServerAddress == "" || cfg.IdentityID == "" || cfg.DeviceName == "" {
		return nil, errors.New("server address, identity ID and device name are required")
	}
	if cfg.Identity == nil || len(cfg.Identity.PublicKey) == 0 || len(cfg.Identity.PrivateKey) == 0 || cfg.Identity.InstallationID == "" {
		return nil, errors.New("device identity is incomplete")
	}
	if cfg.TCPPort < 1 || cfg.TCPPort > 65535 {
		return nil, fmt.Errorf("invalid TCP port %d", cfg.TCPPort)
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = 64
	}
	if cfg.MaxConnections > 256 {
		return nil, errors.New("max connections must be between 1 and 256")
	}
	if cfg.ServerName == "" {
		cfg.ServerName = cfg.ServerAddress
	}
	if net.ParseIP(cfg.ServerAddress) != nil && cfg.ServerName == cfg.ServerAddress {
		return nil, errors.New("TLS server name is required when the Relay Server address is an IP")
	}
	checker, err := acl.NewChecker(acl.Policy{
		ID: "router_exit_policy", Name: "Router Exit Policy",
		AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: false,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Router ACL: %w", err)
	}
	handler := exit.NewRouterHandler(exit.HandlerConfig{
		ACLChecker: checker, ConnectTimeout: cfg.ConnectTimeout,
		SystemDNSAllowed: func() bool { return true },
		ResumeEnabled:    false, AllowBrowserNetwork: false,
	})
	return &Router{cfg: cfg, handler: handler}, nil
}

// containsCapability checks the server-negotiated transport feature set.
func containsCapability(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Capabilities returns the deliberately small Router protocol profile.
func Capabilities() (requested, transport []string) {
	return []string{protocol.CapabilityProxyExit}, []string{
		"tcp", "quic", "tls", protocol.UDPModeStream,
		protocol.CapabilityTargetACL, protocol.CapabilityExitSystemDNS, protocol.CapabilityProxyP2P,
		protocol.CapabilityProxyP2PStreamOnly,
	}
}

func (r *Router) Run(ctx context.Context) error {
	if !r.running.CompareAndSwap(false, true) {
		return errors.New("Router is already running")
	}
	defer r.running.Store(false)
	backoffs := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		addr := net.JoinHostPort(r.cfg.ServerAddress, fmt.Sprint(r.cfg.TCPPort))
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: r.cfg.ServerName, RootCAs: r.cfg.TLSRootCAs}
		dialCtx, cancel := context.WithTimeout(ctx, r.cfg.ConnectTimeout)
		sess, err := tunnel.DialTLS(dialCtx, addr, tlsConfig, nil)
		cancel()
		if err == nil {
			log.Printf("[Router] TLS session established to %s", addr)
			err = r.serveSession(ctx, sess)
			_ = sess.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = errors.New("Relay session ended")
		}
		log.Printf("[Router] Relay session ended: %v", err)
		delay := backoffs[min(attempt, len(backoffs)-1)]
		if attempt < len(backoffs)-1 {
			attempt++
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (r *Router) Close() error {
	if r == nil || r.handler == nil {
		return nil
	}
	return r.handler.Close()
}

func (r *Router) serveSession(parent context.Context, sess tunnel.TunnelSession) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stopClose()

	openCtx, openCancel := context.WithTimeout(ctx, r.cfg.ConnectTimeout)
	ctrl, err := sess.OpenStream(openCtx)
	openCancel()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	defer ctrl.Close()
	if err := ctrl.SetDeadline(time.Now().Add(r.cfg.ConnectTimeout)); err != nil {
		return err
	}
	if err := protocol.WriteStreamHeader(ctrl, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeControl}); err != nil {
		return fmt.Errorf("write control header: %w", err)
	}
	requested, transport := Capabilities()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate client nonce: %w", err)
	}
	hello := protocol.DeviceHello{
		ProtocolVersion: protocol.IdentityDeviceProtocolVersion,
		IdentityID:      r.cfg.IdentityID, InstallationID: r.cfg.Identity.InstallationID,
		PublicKey: append([]byte(nil), r.cfg.Identity.PublicKey...), ClientNonce: nonce,
		DeviceName: r.cfg.DeviceName, Platform: runtime.GOOS, Arch: runtime.GOARCH,
		ClientVersion: "2.0.0", RequestedCapabilities: requested, TransportCapabilities: transport,
	}
	if err := protocol.WriteJSON(ctrl, hello); err != nil {
		return fmt.Errorf("send device hello: %w", err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(ctrl, &challenge); err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}
	if challenge.ProtocolVersion != protocol.IdentityDeviceProtocolVersion || challenge.ChallengeID == "" ||
		challenge.ServerInstanceID == "" || len(challenge.ServerNonce) != 32 || time.Now().Unix() > challenge.ExpiresAt {
		return errors.New("server returned an invalid authentication challenge")
	}
	proof := protocol.AuthProof{ChallengeID: challenge.ChallengeID, Signature: r.cfg.Identity.Sign(protocol.DeviceAuthPayload(hello, challenge))}
	if err := protocol.WriteJSON(ctrl, proof); err != nil {
		return fmt.Errorf("send authentication proof: %w", err)
	}
	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(ctrl, &accepted); err != nil {
		return fmt.Errorf("read device approval: %w", err)
	}
	if !accepted.Success {
		return fmt.Errorf("server rejected connection: [%s] %s", accepted.ErrorCode, accepted.ErrorMessage)
	}
	if accepted.State != "approved" || accepted.DeviceID == "" {
		return errors.New("server returned incomplete device approval")
	}
	if err := ctrl.SetDeadline(time.Time{}); err != nil {
		return err
	}
	allowExit := false
	for _, capability := range accepted.ApprovedCapabilities {
		if capability == protocol.CapabilityProxyExit {
			allowExit = true
			break
		}
	}
	if !allowExit {
		log.Printf("[Router] device approved, but proxy.exit capability is not granted yet")
	}
	log.Printf("[Router] approved device=%s identity=%s heartbeat=%ds exit=%v", accepted.DeviceID, r.cfg.IdentityID, accepted.HeartbeatSec, allowExit)

	maxStreams := r.cfg.MaxConnections
	if accepted.MaxConnections > 0 && accepted.MaxConnections < maxStreams {
		maxStreams = accepted.MaxConnections
	}
	var p2pManager *proxyp2p.Manager
	if allowExit && containsCapability(accepted.TransportCapabilities, protocol.CapabilityProxyP2P) &&
		containsCapability(accepted.TransportCapabilities, protocol.CapabilityProxyP2PStreamOnly) {
		lease := time.Duration(accepted.P2PLeaseSec) * time.Second
		if lease <= 0 {
			lease = 60 * time.Second
		}
		upnpEnabled := accepted.P2PUPnPEnabled && r.cfg.P2PUPnPAllowed
		p2pManager = proxyp2p.NewQUICManagerWithOptions(ctx, func(controlCtx context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
			return r.sendP2PControlRequest(controlCtx, sess, message)
		}, accepted.P2PRendezvousAddress, lease, proxyp2p.QUICManagerOptions{
			PunchTimeout:      5 * time.Second,
			KeepAlive:         10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxExitSessions:   4,
			PortStart:         accepted.P2PPortStart,
			PortEnd:           accepted.P2PPortEnd,
			UPnPEnabled:       upnpEnabled,
			BrutalUploadBPS:   accepted.BrutalUploadBPS,
			BrutalDownloadBPS: accepted.BrutalDownloadBPS,
		})
		log.Printf("[Router] P2P exit enabled rendezvous=%q udp_ports=%d-%d upnp=%v", accepted.P2PRendezvousAddress, accepted.P2PPortStart, accepted.P2PPortEnd, upnpEnabled)
	} else if allowExit {
		log.Printf("[Router] P2P direct path not negotiated by the server; Relay fallback remains available")
	}
	if accepted.HeartbeatSec <= 0 {
		accepted.HeartbeatSec = 15
	}
	var workers sync.WaitGroup
	loopErrors := make(chan error, 2)
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		if err := r.acceptStreams(ctx, sess, maxStreams, allowExit, p2pManager, &workers); err != nil && ctx.Err() == nil {
			select {
			case loopErrors <- err:
			default:
			}
		}
	}()
	if p2pManager != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.serveProxyP2PExit(ctx, p2pManager, maxStreams)
		}()
	}
	go func() {
		defer loops.Done()
		if err := r.heartbeat(ctx, ctrl, sess, time.Duration(accepted.HeartbeatSec)*time.Second); err != nil && ctx.Err() == nil {
			select {
			case loopErrors <- err:
			default:
			}
		}
	}()
	var result error
	select {
	case <-ctx.Done():
	case <-sess.Done():
		result = errors.New("Relay transport closed")
	case result = <-loopErrors:
	}
	cancel()
	if p2pManager != nil {
		_ = p2pManager.Close()
	}
	_ = sess.Close()
	_ = ctrl.Close()
	loops.Wait()
	workers.Wait()
	return result
}

func (r *Router) heartbeat(ctx context.Context, ctrl tunnel.TunnelStream, sess tunnel.TunnelSession, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sess.Done():
			return errors.New("Relay transport closed during heartbeat")
		case <-ticker.C:
			deadline := time.Now().Add(max(5*time.Second, 2*interval))
			if err := ctrl.SetDeadline(deadline); err != nil {
				_ = sess.Close()
				return err
			}
			ping := protocol.PingMessage{Timestamp: time.Now().UnixMilli()}
			if err := protocol.WriteJSON(ctrl, ping); err != nil {
				_ = sess.Close()
				return fmt.Errorf("send heartbeat: %w", err)
			}
			var pong protocol.PongMessage
			if err := protocol.ReadJSON(ctrl, &pong); err != nil {
				_ = sess.Close()
				return fmt.Errorf("read heartbeat: %w", err)
			}
			if err := ctrl.SetDeadline(time.Time{}); err != nil {
				_ = sess.Close()
				return err
			}
		}
	}
}

func (r *Router) acceptStreams(ctx context.Context, sess tunnel.TunnelSession, maxStreams int, allowExit bool, p2pManager *proxyp2p.Manager, workers *sync.WaitGroup) error {
	ctx = exit.WithRelayOrigin(ctx)
	if maxStreams <= 0 {
		maxStreams = r.cfg.MaxConnections
	}
	sem := make(chan struct{}, maxStreams)
	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			return fmt.Errorf("accept incoming stream: %w", err)
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		stop := tunnel.InterruptOnCancel(ctx, stream)
		header, err := protocol.ReadStreamHeader(stream)
		stop()
		if err != nil {
			<-sem
			_ = stream.Close()
			continue
		}
		if header.Type == protocol.FrameTypeP2PControl && p2pManager != nil {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() { <-sem }()
				defer stream.Close()
				var message protocol.P2PControlMessage
				if err := protocol.ReadJSON(stream, &message); err != nil {
					log.Printf("[Router] invalid P2P control message: %v", err)
					return
				}
				p2pManager.HandleControl(message)
			}()
			continue
		}
		if !allowExit || (header.Type != protocol.FrameTypeOpenTCP && header.Type != protocol.FrameTypeOpenUDP && header.Type != protocol.FrameTypeExitSystemDNS) {
			<-sem
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-sem }()
			r.handler.HandleRouterStreamWithHeader(ctx, stream, header)
		}()
	}
}

// sendP2PControlRequest keeps authorization and rendezvous signaling on the
// authenticated Relay TLS session. The direct QUIC path carries data only.
func (r *Router) sendP2PControlRequest(ctx context.Context, sess tunnel.TunnelSession, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
	if sess == nil {
		return protocol.P2PControlMessage{}, errors.New("Relay session is unavailable")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return protocol.P2PControlMessage{}, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeP2PControl,
		RequestID: fmt.Sprintf("router_p2p_ctl_%d", time.Now().UnixNano()), ExitDeviceID: message.ExitDeviceID,
	}); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	if err := protocol.WriteJSON(stream, message); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	var response protocol.P2PControlMessage
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	return response, nil
}

// serveProxyP2PExit consumes only authenticated, server-coordinated direct
// sessions. The Router keeps its Relay listener active in parallel, so Client
// routing can fall back without changing the exit or reconnecting the device.
func (r *Router) serveProxyP2PExit(ctx context.Context, manager *proxyp2p.Manager, maxStreams int) {
	if manager == nil || r == nil || r.handler == nil {
		return
	}
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case p2pSession, ok := <-manager.ReadySessions():
			if !ok {
				return
			}
			if p2pSession == nil {
				continue
			}
			policy := p2pSession.RelayPolicy()
			if policy == nil {
				log.Printf("[Router] rejected READY P2P session=%d without server ACL policy", p2pSession.ID)
				continue
			}
			direct, ready := p2pSession.Tunnel()
			if !ready || direct == nil {
				continue
			}
			clientDeviceID, exitDeviceID := p2pSession.ClientDeviceID, p2pSession.ExitDeviceID
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				r.acceptProxyP2PExitSession(ctx, direct, policy, clientDeviceID, exitDeviceID, maxStreams)
			}()
		}
	}
}

// acceptProxyP2PExitSession applies the same Router-only protocol profile and
// server ACL as the Relay path. A P2P peer cannot supply or override its device
// identities or the relay policy in an OpenTCP/OpenUDP request.
func (r *Router) acceptProxyP2PExitSession(ctx context.Context, sess tunnel.TunnelSession, relayPolicy *acl.Policy, clientDeviceID, exitDeviceID string, maxStreams int) {
	if r == nil || r.handler == nil || sess == nil || relayPolicy == nil || clientDeviceID == "" || exitDeviceID == "" {
		return
	}
	if maxStreams <= 0 {
		maxStreams = r.cfg.MaxConnections
	}
	sem := make(chan struct{}, maxStreams)
	var workers sync.WaitGroup
	defer workers.Wait()
	boundCtx := exit.WithDirectP2POrigin(exit.BindRelayPolicy(ctx, relayPolicy), clientDeviceID, exitDeviceID)
	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		stop := tunnel.InterruptOnCancel(ctx, stream)
		header, err := protocol.ReadStreamHeader(stream)
		stop()
		if err != nil || (header.Type != protocol.FrameTypeOpenTCP &&
			header.Type != protocol.FrameTypeOpenUDP && header.Type != protocol.FrameTypeExitSystemDNS) {
			<-sem
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-sem }()
			r.handler.HandleRouterStreamWithHeader(boundCtx, stream, header)
		}()
	}
}

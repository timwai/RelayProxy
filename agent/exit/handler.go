package exit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"relayproxy/internal/acl"
	p2presume "relayproxy/internal/p2p/resume"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const udpIdleTimeout = 60 * time.Second

var udpPipeBufferPool = sync.Pool{
	New: func() any { return make([]byte, protocol.MaxUDPDatagramPayload+1) },
}

type HandlerConfig struct {
	ACLChecker        *acl.Checker
	ConnectTimeout    time.Duration
	Upstream          UpstreamConfig
	ResumeEnabled     bool
	ResumeGrace       time.Duration
	ResumeMaxSessions int
	ResumeReplayLimit int
}

type Handler struct {
	cfg              HandlerConfig
	resolver         *dnsCache
	resume           *resumeRegistry
	activeStreams    atomic.Int64
	relayACLMu       sync.Mutex
	relayACLCacheKey string
	relayACLCache    *acl.Checker
}

func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	cfg.Upstream = cfg.Upstream.normalized()
	return &Handler{
		cfg:      cfg,
		resolver: newDNSCache(defaultDNSCacheTTL, defaultDNSCacheEntries),
		resume:   newResumeRegistry(cfg.ResumeGrace, cfg.ResumeMaxSessions),
	}
}

func (h *Handler) Close() error {
	if h != nil && h.resume != nil {
		h.resume.closeAll()
	}
	return nil
}

func (h *Handler) ActiveStreams() int64 {
	return h.activeStreams.Load()
}

// HandleStream handles a single reverse stream from Relay server
func (h *Handler) HandleStream(ctx context.Context, stream tunnel.TunnelStream) {
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	header, err := protocol.ReadStreamHeader(stream)
	if err != nil {
		log.Printf("[ExitHandler] Failed to read stream header: %v", err)
		_ = stream.Close()
		return
	}
	h.HandleStreamWithHeader(ctx, stream, header)
}

// HandleStreamWithHeader handles a stream after a caller has already consumed
// its protocol header. The agent uses this entry point to dispatch exit and
// RDP-host streams through one AcceptStream loop.
func (h *Handler) HandleStreamWithHeader(ctx context.Context, stream tunnel.TunnelStream, header *protocol.StreamHeader) {
	defer stream.Close()
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))

	switch header.Type {
	case protocol.FrameTypeOpenTCP:
		h.handleOpenTCP(ctx, stream)
	case protocol.FrameTypeOpenUDP:
		h.handleOpenUDP(ctx, stream)
	default:
		log.Printf("[ExitHandler] Unsupported frame type: %d", header.Type)
	}
}

func (h *Handler) handleOpenTCP(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.OpenTCPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		log.Printf("[ExitHandler] Failed to read OpenTCPRequest: %v", err)
		return
	}
	if policy := boundRelayPolicy(ctx); policy != nil {
		req.RelayPolicy = policy
	}
	if req.Resume != nil {
		if !h.cfg.ResumeEnabled {
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				ErrorCode:    protocol.ErrCodeInvalidRequest,
				ErrorMessage: "resumable TCP is not enabled on this Exit",
			})
			return
		}
		if req.Resume.Mode == protocol.TCPResumeModeRebind {
			h.handleTCPResumeRebind(ctx, stream, req)
			return
		}
		if req.Resume.Mode != protocol.TCPResumeModeOpen {
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				ErrorCode:    protocol.ErrCodeInvalidRequest,
				ErrorMessage: "invalid resumable TCP mode",
			})
			return
		}
	}

	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeACLDenied, ErrorMessage: err.Error()})
		return
	}
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, req.Host, req.Port, "tcp"); err != nil {
			log.Printf("[ExitHandler] ACL denied for host %s: %v", req.Host, err)
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeACLDenied,
				ErrorMessage: err.Error(),
			})
			return
		}
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = h.cfg.ConnectTimeout
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, timeout)
	defer dialCancel()

	var ips []net.IP
	targetIP := net.ParseIP(req.Host)
	if targetIP != nil {
		if checker != nil {
			if err := checker.CheckIPProtocol(dialCtx, targetIP, req.Port, "tcp"); err != nil {
				_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
					RequestID:    req.RequestID,
					Success:      false,
					ErrorCode:    protocol.ErrCodeACLDenied,
					ErrorMessage: err.Error(),
				})
				return
			}
		}
		ips = []net.IP{targetIP}
	} else {
		var err error
		ips, err = h.resolver.lookup(dialCtx, req.Host)
		if err != nil {
			log.Printf("[ExitHandler] DNS resolution failed for %s: %v", req.Host, err)
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeDNSFailed,
				ErrorMessage: "DNS resolution failed: " + err.Error(),
			})
			return
		}

		if len(ips) == 0 {
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeDNSFailed,
				ErrorMessage: "no IP addresses found for host: " + req.Host,
			})
			return
		}

		if checker != nil {
			for _, ip := range ips {
				if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "tcp"); err != nil {
					log.Printf("[ExitHandler] Resolved IP %s failed ACL: %v", ip, err)
					_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
						RequestID:    req.RequestID,
						Success:      false,
						ErrorCode:    protocol.ErrCodeACLDenied,
						ErrorMessage: fmt.Sprintf("resolved ip %s blocked by ACL: %v", ip, err),
					})
					return
				}
			}
		}
	}

	targetConn, remoteTarget, lastErr := h.dialTCP(dialCtx, ips, req.Port)

	if targetConn == nil {
		log.Printf("[ExitHandler] Dial to %s failed: %v", req.Host, lastErr)
		errorCode := protocol.ErrCodeConnectTimeout
		if isConnRefused(lastErr) {
			errorCode = protocol.ErrCodeConnectionRefused
		}
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    errorCode,
			ErrorMessage: lastErr.Error(),
		})
		return
	}
	tunnel.TuneTCPConn(targetConn)

	if req.Resume != nil {
		h.handleTCPResumeOpen(stream, req, targetConn, remoteTarget)
		return
	}
	defer targetConn.Close()

	resp := protocol.OpenTCPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  remoteTarget,
	}
	if err := protocol.WriteJSON(stream, resp); err != nil {
		log.Printf("[ExitHandler] Failed to write success response: %v", err)
		return
	}
	_ = stream.SetDeadline(time.Time{})

	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)

	tunnel.Pipe(ctx, stream, targetConn, 5*time.Minute, nil)
}

func (h *Handler) handleTCPResumeOpen(
	stream tunnel.TunnelStream,
	req protocol.OpenTCPRequest,
	targetConn net.Conn,
	remoteTarget string,
) {
	peer, err := p2presume.RequestBindingFromProtocol(req.Resume)
	if err != nil || peer.Generation != 1 || peer.SendOffset != 0 || peer.ReceiveOffset != 0 {
		_ = targetConn.Close()
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			ErrorCode:    protocol.ErrCodeInvalidRequest,
			ErrorMessage: "invalid initial resumable TCP binding",
		})
		return
	}

	local := p2presume.Binding{
		Type:       p2presume.BindAck,
		Identity:   peer.Identity,
		Generation: 1,
	}
	h.activeStreams.Add(1)
	session, err := h.resume.registerLogical(
		local,
		targetConn,
		remoteTarget,
		h.cfg.ResumeReplayLimit,
		func() { h.activeStreams.Add(-1) },
	)
	if err != nil {
		h.activeStreams.Add(-1)
		_ = targetConn.Close()
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			ErrorCode:    protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "failed to register resumable TCP session",
		})
		return
	}

	local, err = session.localBinding()
	if err != nil {
		h.resume.remove(peer.Identity.ID)
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			ErrorCode:    protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "resumable TCP session closed during setup",
		})
		return
	}
	wire, err := p2presume.BindingToProtocol(local, protocol.TCPResumeModeOpen)
	if err != nil {
		h.resume.remove(peer.Identity.ID)
		return
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  remoteTarget,
		Resume:    wire,
	}); err != nil {
		h.resume.remove(peer.Identity.ID)
		return
	}
	_ = stream.SetDeadline(time.Time{})

	done, err := session.bindTransport(stream, local.Generation)
	if err != nil {
		h.resume.remove(peer.Identity.ID)
		return
	}
	<-done
}

func (h *Handler) handleTCPResumeRebind(
	ctx context.Context,
	stream tunnel.TunnelStream,
	req protocol.OpenTCPRequest,
) {
	peer, err := p2presume.RequestBindingFromProtocol(req.Resume)
	if err != nil || req.Resume.Mode != protocol.TCPResumeModeRebind {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			ErrorCode:    protocol.ErrCodeInvalidRequest,
			ErrorMessage: "invalid resumable TCP rebind",
		})
		return
	}
	session, retry, err := h.resume.rebind(peer)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			ErrorCode:    protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "resumable TCP session is unavailable",
		})
		return
	}

	local, err := session.localBinding()
	if err != nil {
		return
	}
	recoveryArmed := true
	defer func() {
		if recoveryArmed {
			_ = session.detach(local)
		}
	}()

	wire, err := p2presume.BindingToProtocol(local, protocol.TCPResumeModeRebind)
	if err != nil {
		return
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  session.remoteAddress(),
		Resume:    wire,
	}); err != nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})

	var done <-chan struct{}
	if retry {
		done, err = session.retryTransport(stream, local.Generation)
	} else {
		done, err = session.bindTransport(stream, local.Generation)
	}
	if err != nil {
		return
	}
	recoveryArmed = false
	select {
	case <-done:
	case <-ctx.Done():
		_ = stream.Close()
		<-done
	}
}

func (h *Handler) handleOpenUDP(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.OpenUDPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		log.Printf("[ExitHandler] Failed to read OpenUDPRequest: %v", err)
		return
	}
	if policy := boundRelayPolicy(ctx); policy != nil {
		req.RelayPolicy = policy
	}
	if req.Mode != "" && req.Mode != protocol.UDPModeStream && req.Mode != protocol.UDPModeDatagram {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "unsupported UDP mode"})
		return
	}
	useDatagrams := req.Mode == protocol.UDPModeDatagram && req.AssociationID != 0 && tunnel.PeerSupportsDatagrams(tunnel.StreamSession(stream))
	if req.DatagramRequired && !useDatagrams {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: "native UDP datagrams are required but the exit tunnel does not support them"})
		return
	}

	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeACLDenied, ErrorMessage: err.Error()})
		return
	}
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, req.Host, req.Port, "udp"); err != nil {
			log.Printf("[ExitHandler] ACL denied for UDP host %s: %v", req.Host, err)
			_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeACLDenied,
				ErrorMessage: err.Error(),
			})
			return
		}
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = h.cfg.ConnectTimeout
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	dialCtx, dialCancel := context.WithTimeout(ctx, timeout)
	defer dialCancel()

	targetIP := net.ParseIP(req.Host)
	var dialHost string
	if targetIP != nil {
		if checker != nil {
			if err := checker.CheckIPProtocol(dialCtx, targetIP, req.Port, "udp"); err != nil {
				_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
					RequestID:    req.RequestID,
					Success:      false,
					ErrorCode:    protocol.ErrCodeACLDenied,
					ErrorMessage: err.Error(),
				})
				return
			}
		}
		dialHost = targetIP.String()
	} else {
		ips, err := h.resolver.lookup(dialCtx, req.Host)
		if err != nil || len(ips) == 0 {
			msg := "no IP addresses found for host: " + req.Host
			if err != nil {
				msg = "DNS resolution failed: " + err.Error()
			}
			_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeDNSFailed,
				ErrorMessage: msg,
			})
			return
		}
		if checker != nil {
			for _, ip := range ips {
				if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "udp"); err != nil {
					_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
						RequestID:    req.RequestID,
						Success:      false,
						ErrorCode:    protocol.ErrCodeACLDenied,
						ErrorMessage: fmt.Sprintf("resolved ip %s blocked by ACL: %v", ip, err),
					})
					return
				}
			}
		}
		dialHost = ips[0].String()
	}

	targetAddr, err := netip.ParseAddrPort(net.JoinHostPort(dialHost, strconv.Itoa(int(req.Port))))
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeInternalError,
			ErrorMessage: err.Error(),
		})
		return
	}

	targetConn, err := h.dialUDP(dialCtx, targetAddr)
	if err != nil {
		log.Printf("[ExitHandler] UDP dial to %s via %s failed: %v", req.Host, h.cfg.Upstream.Mode, err)
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeConnectTimeout,
			ErrorMessage: err.Error(),
		})
		return
	}
	defer targetConn.Close()
	if udpConn, ok := targetConn.(*net.UDPConn); ok {
		tunnel.TuneUDPConn(udpConn)
	}

	resp := protocol.OpenUDPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  targetAddr.String(),
		Mode:      protocol.UDPModeStream,
	}
	var datagrams *tunnel.DatagramChannel
	if useDatagrams {
		datagrams, err = tunnel.OpenDatagramChannel(tunnel.StreamSession(stream), req.AssociationID)
		if err != nil {
			_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed, ErrorMessage: err.Error()})
			return
		}
		defer datagrams.Close()
		resp.Mode, resp.AssociationID = protocol.UDPModeDatagram, req.AssociationID
	}
	if err := protocol.WriteJSON(stream, resp); err != nil {
		log.Printf("[ExitHandler] Failed to write UDP success response: %v", err)
		return
	}
	_ = stream.SetDeadline(time.Time{})

	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)

	var pc net.PacketConn
	if datagrams != nil {
		pc = tunnel.NewUDPDatagramConn(datagrams, stream, targetConn.RemoteAddr())
	} else {
		pc = tunnel.NewUDPStreamConn(stream, targetConn.RemoteAddr())
	}
	h.pipeUDP(ctx, pc, targetConn)
}

func (h *Handler) pipeUDP(ctx context.Context, pc net.PacketConn, conn net.Conn) {
	var once sync.Once
	stop := func() { once.Do(func() { _ = pc.Close(); _ = conn.Close() }) }
	defer stop()
	stopCancel := context.AfterFunc(ctx, stop)
	defer stopCancel()

	var activitySeq atomic.Uint64
	finished := make(chan struct{}, 2)
	_ = conn.SetDeadline(time.Now().Add(udpIdleTimeout))

	go func() {
		defer func() { finished <- struct{}{} }()
		defer stop()
		buf := udpPipeBufferPool.Get().([]byte)
		defer udpPipeBufferPool.Put(buf)
		for {
			n, _, err := pc.ReadFrom(buf[:protocol.MaxUDPDatagramPayload])
			if err != nil {
				return
			}
			activitySeq.Add(1)
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	go func() {
		defer func() { finished <- struct{}{} }()
		defer stop()
		// Read one extra byte so an oversized IPv6 datagram is rejected rather
		// than forwarded as a silently truncated packet.
		buf := udpPipeBufferPool.Get().([]byte)
		defer udpPipeBufferPool.Put(buf)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if n > protocol.MaxUDPDatagramPayload {
				continue
			}
			activitySeq.Add(1)
			if _, err := pc.WriteTo(buf[:n], nil); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastSeq := activitySeq.Load()
	for completed := 0; completed < 2; {
		select {
		case <-ctx.Done():
			stop()
		case <-finished:
			completed++
		case now := <-ticker.C:
			seq := activitySeq.Load()
			if seq != lastSeq {
				lastSeq = seq
				_ = conn.SetDeadline(now.Add(udpIdleTimeout))
			}
		}
	}
}

func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	var sysErr syscall.Errno
	if errors.As(err, &sysErr) {
		if sysErr == syscall.ECONNREFUSED || sysErr == 10061 {
			return true
		}
	}
	return strings.Contains(strings.ToLower(err.Error()), "refused")
}

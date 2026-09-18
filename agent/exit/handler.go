package exit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const udpIdleTimeout = 60 * time.Second

var udpPipeBufferPool = sync.Pool{
	New: func() any { return make([]byte, protocol.MaxUDPDatagramPayload+1) },
}

type HandlerConfig struct {
	ACLChecker     *acl.Checker
	ConnectTimeout time.Duration
}

type Handler struct {
	cfg           HandlerConfig
	resolver      *dnsCache
	activeStreams atomic.Int64
}

func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	return &Handler{cfg: cfg, resolver: newDNSCache(defaultDNSCacheTTL, defaultDNSCacheEntries)}
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

	targetConn, lastErr := dialTCPIPs(dialCtx, ips, req.Port)

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
	defer targetConn.Close()
	tunnel.TuneTCPConn(targetConn)

	resp := protocol.OpenTCPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  targetConn.RemoteAddr().String(),
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

func (h *Handler) handleOpenUDP(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.OpenUDPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		log.Printf("[ExitHandler] Failed to read OpenUDPRequest: %v", err)
		return
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

	raddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(dialHost, strconv.Itoa(int(req.Port))))
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeInternalError,
			ErrorMessage: err.Error(),
		})
		return
	}

	var d net.Dialer
	c, err := d.DialContext(dialCtx, "udp", raddr.String())
	if err != nil {
		log.Printf("[ExitHandler] UDP dial to %s failed: %v", req.Host, err)
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeConnectTimeout,
			ErrorMessage: err.Error(),
		})
		return
	}
	defer c.Close()

	udpConn, ok := c.(*net.UDPConn)
	if !ok {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeInternalError,
			ErrorMessage: "udp dial did not return *UDPConn",
		})
		return
	}
	tunnel.TuneUDPConn(udpConn)

	resp := protocol.OpenUDPResponse{
		RequestID: req.RequestID,
		Success:   true,
		RemoteIP:  udpConn.RemoteAddr().String(),
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
		pc = tunnel.NewUDPDatagramConn(datagrams, stream, udpConn.RemoteAddr())
	} else {
		pc = tunnel.NewUDPStreamConn(stream, udpConn.RemoteAddr())
	}
	h.pipeUDP(ctx, pc, udpConn)
}

func (h *Handler) pipeUDP(ctx context.Context, pc net.PacketConn, conn *net.UDPConn) {
	var wg sync.WaitGroup
	wg.Add(2)
	var once sync.Once
	stop := func() { once.Do(func() { _ = pc.Close(); _ = conn.Close() }) }
	defer stop()
	stopCancel := context.AfterFunc(ctx, stop)
	defer stopCancel()
	var nextDeadlineRefresh atomic.Int64
	touch := func() {
		now := time.Now()
		nowNanos := now.UnixNano()
		next := nextDeadlineRefresh.Load()
		if next != 0 && nowNanos < next {
			return
		}
		if !nextDeadlineRefresh.CompareAndSwap(next, now.Add(time.Second).UnixNano()) {
			return
		}
		_ = conn.SetDeadline(now.Add(udpIdleTimeout))
	}
	touch()

	go func() {
		defer wg.Done()
		defer stop()
		buf := udpPipeBufferPool.Get().([]byte)
		defer udpPipeBufferPool.Put(buf)
		for {
			n, _, err := pc.ReadFrom(buf[:protocol.MaxUDPDatagramPayload])
			if err != nil {
				return
			}
			touch()
			if _, err := conn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
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
			touch()
			if _, err := pc.WriteTo(buf[:n], nil); err != nil {
				return
			}
		}
	}()

	wg.Wait()
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

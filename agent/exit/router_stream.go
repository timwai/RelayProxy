package exit

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// NewRouterHandler creates the reduced exit handler used by the headless
// Router binary. It deliberately does not initialize TCP resume, E2EE, or
// optional browser/direct-path services.
func NewRouterHandler(cfg HandlerConfig) *Handler {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	cfg.Upstream = cfg.Upstream.normalized()
	return &Handler{
		cfg:      cfg,
		resolver: newDNSCache(defaultDNSCacheTTL, defaultDNSCacheEntries),
	}
}

// HandleRouterStreamWithHeader exposes only the Router data plane. The Router
// protocol profile is TLS/yamux with TCP and stream-framed UDP; SecureRelay,
// RDP, file share, browser network, and native QUIC datagrams are not accepted.
func (h *Handler) HandleRouterStreamWithHeader(ctx context.Context, stream tunnel.TunnelStream, header *protocol.StreamHeader) {
	if h == nil || stream == nil || header == nil {
		if stream != nil { _ = stream.Close() }
		return
	}
	defer stream.Close()
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))

	switch header.Type {
	case protocol.FrameTypeOpenTCP:
		h.handleRouterOpenTCP(ctx, stream)
	case protocol.FrameTypeOpenUDP:
		h.handleRouterOpenUDP(ctx, stream)
	case protocol.FrameTypeExitSystemDNS:
		h.handleExitSystemDNS(ctx, stream)
	default:
		log.Printf("[Router] rejected unsupported incoming frame type=%d", header.Type)
	}
}

func (h *Handler) handleRouterOpenTCP(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.OpenTCPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		log.Printf("[Router] invalid TCP request: %v", err)
		return
	}
	if req.ExitHop < 0 || req.ExitHop >= protocol.MaxExitHops {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeInvalidRequest, ErrorMessage:"exit chain exceeds hop limit"})
		return
	}
	if req.Resume != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeInvalidRequest, ErrorMessage:"TCP resume is not supported by Router"})
		return
	}
	ctx = protocol.WithExitHop(ctx, req.ExitHop+1)
	if policy := boundRelayPolicy(ctx); policy != nil { req.RelayPolicy = policy }

	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeACLDenied, ErrorMessage:err.Error()})
		return
	}
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, req.Host, req.Port, "tcp"); err != nil {
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeACLDenied, ErrorMessage:err.Error()})
			return
		}
	}
	timeout := time.Duration(req.TimeoutMs)*time.Millisecond
	if timeout <= 0 { timeout = h.cfg.ConnectTimeout }
	if timeout > 60*time.Second { timeout = 60*time.Second }
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var ips []net.IP
	if ip := net.ParseIP(req.Host); ip != nil {
		if checker != nil {
			if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "tcp"); err != nil {
				_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeACLDenied, ErrorMessage:err.Error()})
				return
			}
		}
		ips = []net.IP{ip}
	} else {
		ips, err = h.resolver.lookup(dialCtx, req.Host)
		if err != nil || len(ips)==0 {
			msg := "no IP addresses found for host: "+req.Host
			if err != nil { msg = "DNS resolution failed: "+err.Error() }
			_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeDNSFailed, ErrorMessage:msg})
			return
		}
		if checker != nil {
			for _, ip := range ips {
				if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "tcp"); err != nil {
					_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeACLDenied, ErrorMessage:fmt.Sprintf("resolved IP %s blocked by ACL: %v",ip,err)})
					return
				}
			}
		}
	}
	target, remoteIP, dialErr := h.dialTCP(dialCtx, ips, req.Port)
	if target == nil {
		if dialErr == nil { dialErr = fmt.Errorf("TCP dial failed") }
		code := protocol.ErrCodeConnectTimeout
		if isConnRefused(dialErr) { code = protocol.ErrCodeConnectionRefused }
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, ErrorCode:code, ErrorMessage:dialErr.Error()})
		return
	}
	defer target.Close()
	tunnel.TuneTCPConn(target)
	if err := protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID:req.RequestID, Success:true, RemoteIP:remoteIP}); err != nil {
		log.Printf("[Router] write TCP response failed: %v",err)
		return
	}
	_ = stream.SetDeadline(time.Time{})
	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)
	tunnel.Pipe(ctx, stream, target, 5*time.Minute, nil)
}

func (h *Handler) handleRouterOpenUDP(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.OpenUDPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		log.Printf("[Router] invalid UDP request: %v",err)
		return
	}
	if req.ExitHop < 0 || req.ExitHop >= protocol.MaxExitHops {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeInvalidRequest, ErrorMessage:"exit chain exceeds hop limit"})
		return
	}
	if req.Mode!="" && req.Mode!=protocol.UDPModeStream && req.Mode!=protocol.UDPModeDatagram {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeInvalidRequest, ErrorMessage:"unsupported UDP mode"})
		return
	}
	if req.DatagramRequired {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID:req.RequestID, ErrorCode:protocol.ErrCodeDatagramRequired, ErrorMessage:"Router supports UDP over reliable TLS streams only"})
		return
	}
	ctx = protocol.WithExitHop(ctx,req.ExitHop+1)
	if policy:=boundRelayPolicy(ctx); policy!=nil { req.RelayPolicy=policy }
	checker,err:=h.aclForRequest(req.RelayPolicy)
	if err!=nil {
		_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeACLDenied,ErrorMessage:err.Error()})
		return
	}
	if checker!=nil {
		if err:=checker.CheckHostProtocol(ctx,req.Host,req.Port,"udp"); err!=nil {
			_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeACLDenied,ErrorMessage:err.Error()})
			return
		}
	}
	timeout:=time.Duration(req.TimeoutMs)*time.Millisecond
	if timeout<=0 { timeout=h.cfg.ConnectTimeout }
	if timeout>60*time.Second { timeout=60*time.Second }
	dialCtx,cancel:=context.WithTimeout(ctx,timeout)
	defer cancel()

	ip:=net.ParseIP(req.Host)
	dialHost:=""
	if ip!=nil {
		if checker!=nil {
			if err:=checker.CheckIPProtocol(dialCtx,ip,req.Port,"udp"); err!=nil {
				_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeACLDenied,ErrorMessage:err.Error()})
				return
			}
		}
		dialHost=ip.String()
	} else {
		ips,lookupErr:=h.resolver.lookup(dialCtx,req.Host)
		if lookupErr!=nil || len(ips)==0 {
			msg:="no IP addresses found for host: "+req.Host
			if lookupErr!=nil { msg="DNS resolution failed: "+lookupErr.Error() }
			_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeDNSFailed,ErrorMessage:msg})
			return
		}
		if checker!=nil {
			for _,resolved:=range ips {
				if err:=checker.CheckIPProtocol(dialCtx,resolved,req.Port,"udp"); err!=nil {
					_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeACLDenied,ErrorMessage:fmt.Sprintf("resolved IP %s blocked by ACL: %v",resolved,err)})
					return
				}
			}
		}
		dialHost=ips[0].String()
	}
	targetAddr,err:=netip.ParseAddrPort(net.JoinHostPort(dialHost,strconv.Itoa(int(req.Port))))
	if err!=nil {
		_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeInternalError,ErrorMessage:err.Error()})
		return
	}
	target,err:=h.dialUDP(dialCtx,targetAddr)
	if err!=nil {
		_ = protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,ErrorCode:protocol.ErrCodeConnectTimeout,ErrorMessage:err.Error()})
		return
	}
	defer target.Close()
	if udp,ok:=target.(*net.UDPConn); ok { tunnel.TuneUDPConn(udp) }
	if err:=protocol.WriteJSON(stream,protocol.OpenUDPResponse{RequestID:req.RequestID,Success:true,RemoteIP:targetAddr.String(),Mode:protocol.UDPModeStream}); err!=nil {
		return
	}
	_ = stream.SetDeadline(time.Time{})
	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)
	pc:=tunnel.NewUDPStreamConn(stream,target.RemoteAddr())
	h.pipeUDP(ctx,pc,target)
}

package exit

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// OpenTCP opens a target connection using the same DNS, ACL and upstream
// behavior as a tunnel-backed Exit. It is used by relay-server when the Relay
// process itself is selected as the exit.
func (h *Handler) OpenTCP(ctx context.Context, req protocol.OpenTCPRequest) (net.Conn, string, error) {
	if policy := boundRelayPolicy(ctx); policy != nil {
		req.RelayPolicy = policy
	}
	if req.Resume != nil {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeInvalidRequest, "resumable TCP is not supported by the server exit")
	}

	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
	}
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, req.Host, req.Port, "tcp"); err != nil {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
		}
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = h.cfg.ConnectTimeout
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var ips []net.IP
	if targetIP := net.ParseIP(req.Host); targetIP != nil {
		if checker != nil {
			if err := checker.CheckIPProtocol(dialCtx, targetIP, req.Port, "tcp"); err != nil {
				return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
			}
		}
		ips = []net.IP{targetIP}
	} else {
		ips, err = h.resolver.lookup(dialCtx, req.Host)
		if err != nil {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeDNSFailed, "DNS resolution failed: "+err.Error())
		}
		if len(ips) == 0 {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeDNSFailed, "no IP addresses found for host: "+req.Host)
		}
		if checker != nil {
			for _, ip := range ips {
				if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "tcp"); err != nil {
					return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, fmt.Sprintf("resolved ip %s blocked by ACL: %v", ip, err))
				}
			}
		}
	}

	conn, remote, dialErr := h.dialTCP(dialCtx, ips, req.Port)
	if dialErr != nil {
		code := protocol.ErrCodeConnectTimeout
		if isConnRefused(dialErr) {
			code = protocol.ErrCodeConnectionRefused
		}
		return nil, "", protocol.NewRelayError(code, dialErr.Error())
	}
	tunnel.TuneTCPConn(conn)
	return conn, remote, nil
}

// OpenUDP opens the final UDP target for the server-local exit. Native QUIC
// datagrams are a property of the peer tunnel, so the server-local path uses
// the existing reliable UDP stream framing.
func (h *Handler) OpenUDP(ctx context.Context, req protocol.OpenUDPRequest) (net.Conn, string, error) {
	if policy := boundRelayPolicy(ctx); policy != nil {
		req.RelayPolicy = policy
	}
	if req.DatagramRequired {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are not available on the server exit")
	}
	if req.Mode != "" && req.Mode != protocol.UDPModeStream {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeInvalidRequest, "server exit requires UDP stream mode")
	}

	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
	}
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, req.Host, req.Port, "udp"); err != nil {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
		}
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = h.cfg.ConnectTimeout
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialHost string
	if targetIP := net.ParseIP(req.Host); targetIP != nil {
		if checker != nil {
			if err := checker.CheckIPProtocol(dialCtx, targetIP, req.Port, "udp"); err != nil {
				return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
			}
		}
		dialHost = targetIP.String()
	} else {
		ips, err := h.resolver.lookup(dialCtx, req.Host)
		if err != nil {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeDNSFailed, "DNS resolution failed: "+err.Error())
		}
		if len(ips) == 0 {
			return nil, "", protocol.NewRelayError(protocol.ErrCodeDNSFailed, "no IP addresses found for host: "+req.Host)
		}
		if checker != nil {
			for _, ip := range ips {
				if err := checker.CheckIPProtocol(dialCtx, ip, req.Port, "udp"); err != nil {
					return nil, "", protocol.NewRelayError(protocol.ErrCodeACLDenied, fmt.Sprintf("resolved ip %s blocked by ACL: %v", ip, err))
				}
			}
		}
		dialHost = ips[0].String()
	}

	target, err := netip.ParseAddrPort(net.JoinHostPort(dialHost, strconv.Itoa(int(req.Port))))
	if err != nil {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeInternalError, err.Error())
	}
	conn, err := h.dialUDP(dialCtx, target)
	if err != nil {
		return nil, "", protocol.NewRelayError(protocol.ErrCodeConnectTimeout, err.Error())
	}
	if udpConn, ok := conn.(*net.UDPConn); ok {
		tunnel.TuneUDPConn(udpConn)
	}
	return conn, target.String(), nil
}

// PipeTCP relays one already-opened server-exit TCP target and keeps the same
// active stream metric exposed by tunnel-backed Exit handling.
func (h *Handler) PipeTCP(ctx context.Context, stream tunnel.TunnelStream, conn net.Conn) (int64, int64) {
	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)
	return tunnel.Pipe(ctx, stream, conn, 5*time.Minute, nil)
}

// PipeUDPStream bridges RelayProxy's UDP stream framing to a server-local UDP
// socket.
func (h *Handler) PipeUDPStream(ctx context.Context, stream tunnel.TunnelStream, conn net.Conn) (int64, int64) {
	h.activeStreams.Add(1)
	defer h.activeStreams.Add(-1)
	pc := tunnel.NewUDPStreamConn(stream, conn.RemoteAddr())
	return h.pipeUDP(ctx, pc, conn)
}

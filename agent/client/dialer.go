package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
	"relayproxy/internal/tunnel"
)

type proxyAddr struct {
	net  string
	addr string
}

func (a proxyAddr) Network() string { return a.net }
func (a proxyAddr) String() string  { return a.addr }

type TunnelDialer struct {
	getTunnel     func() tunnel.TunnelSession
	getClientID   func() string
	defaultExitID atomic.Pointer[string]
	requestSeq    atomic.Uint64

	directMu       sync.RWMutex
	getDirect      func(exitDeviceID string) (tunnel.TunnelSession, bool)
	ensureDirect   func(exitDeviceID string)
	directMode     string
	directFallback bool
}

func NewTunnelDialer(getTunnel func() tunnel.TunnelSession, getClientID func() string) *TunnelDialer {
	return &TunnelDialer{
		getTunnel: getTunnel, getClientID: getClientID,
		directMode: "auto", directFallback: true,
	}
}

// ConfigureDirectPath installs optional Client -> Exit P2P lookup hooks. When
// no READY direct path exists, normal Relay traffic proceeds immediately while
// ensureDirect prepares a path for later flows.
func (d *TunnelDialer) ConfigureDirectPath(
	get func(exitDeviceID string) (tunnel.TunnelSession, bool),
	ensure func(exitDeviceID string),
) {
	d.directMu.Lock()
	d.getDirect = get
	d.ensureDirect = ensure
	d.directMu.Unlock()
}

func (d *TunnelDialer) ConfigureDirectPolicy(mode string, fallback bool) {
	d.directMu.Lock()
	d.directMode, d.directFallback = mode, fallback
	d.directMu.Unlock()
}

func (d *TunnelDialer) directFallbackEnabled() bool {
	d.directMu.RLock()
	defer d.directMu.RUnlock()
	return d.directMode != "p2p_only" && d.directFallback
}

func (d *TunnelDialer) sessionForExit(exitDeviceID string) (tunnel.TunnelSession, bool) {
	d.directMu.RLock()
	getDirect, ensureDirect := d.getDirect, d.ensureDirect
	mode := d.directMode
	d.directMu.RUnlock()
	if mode != "relay_only" && exitDeviceID != "" {
		if getDirect != nil {
			if session, ok := getDirect(exitDeviceID); ok && session != nil {
				return session, true
			}
		}
		if ensureDirect != nil {
			ensureDirect(exitDeviceID)
		}
		if mode == "p2p_only" {
			return nil, false
		}
	}
	if d.getTunnel == nil {
		return nil, false
	}
	return d.getTunnel(), false
}

func (d *TunnelDialer) openProxyStream(ctx context.Context, exitDeviceID string) (tunnel.TunnelSession, tunnel.TunnelStream, bool, error) {
	session, direct := d.sessionForExit(exitDeviceID)
	if session == nil {
		return nil, nil, false, fmt.Errorf("tunnel is not connected")
	}
	stream, err := session.OpenStream(ctx)
	if err == nil {
		return session, stream, direct, nil
	}
	if !direct || d.getTunnel == nil {
		return nil, nil, direct, err
	}
	relay := d.getTunnel()
	if relay == nil || relay == session {
		return nil, nil, direct, err
	}
	stream, relayErr := relay.OpenStream(ctx)
	if relayErr != nil {
		return nil, nil, false, fmt.Errorf("direct stream failed: %v; relay fallback failed: %w", err, relayErr)
	}
	return relay, stream, false, nil
}

func (d *TunnelDialer) SetDefaultExitID(exitID string) {
	d.defaultExitID.Store(&exitID)
}

func (d *TunnelDialer) GetDefaultExitID() string {
	ptr := d.defaultExitID.Load()
	if ptr == nil {
		return ""
	}
	return *ptr
}

func (d *TunnelDialer) nextRequestID() string {
	return d.nextRequestIDWithPrefix("req_")
}

func (d *TunnelDialer) nextRequestIDWithPrefix(prefix string) string {
	return prefix + strconv.FormatUint(d.requestSeq.Add(1), 36)
}

func (d *TunnelDialer) DialTCP(ctx context.Context, exitNodeID string, host string, port uint16) (net.Conn, error) {
	if exitNodeID == "" {
		exitNodeID = d.GetDefaultExitID()
	}

	sess, direct := d.sessionForExit(exitNodeID)
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	conn, err := d.dialTCPOnSession(ctx, sess, exitNodeID, host, port)
	if err == nil || !direct || !retryableDirectHandshakeError(ctx, err) || d.getTunnel == nil || !d.directFallbackEnabled() {
		return conn, err
	}
	relay := d.getTunnel()
	if relay == nil || relay == sess {
		return nil, err
	}
	return d.dialTCPOnSession(ctx, relay, exitNodeID, host, port)
}

func (d *TunnelDialer) dialTCPOnSession(ctx context.Context, sess tunnel.TunnelSession, exitNodeID, host string, port uint16) (net.Conn, error) {
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open tunnel stream: %w", err)
	}
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	}

	reqID := d.nextRequestID()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenTCP,
		RequestID: reqID, ExitDeviceID: exitNodeID,
	}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write stream header: %w", err)
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPRequest{
		RequestID: reqID, Host: host, Port: port, TimeoutMs: 10000,
	}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write open request: %w", err)
	}

	var resp protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &resp); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to read open response: %w", err)
	}
	if !resp.Success {
		_ = stream.Close()
		code := resp.ErrorCode
		if code == "" {
			code = protocol.ErrCodeInternalError
		}
		msg := resp.ErrorMessage
		if msg == "" {
			msg = fmt.Sprintf("dial target %s:%d failed via exit %s", host, port, exitNodeID)
		}
		return nil, protocol.NewRelayError(code, msg)
	}

	stopCancel()
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})

	localAddr := sess.LocalAddr()
	var remoteAddr net.Addr
	if ip := net.ParseIP(host); ip != nil {
		remoteAddr = &net.TCPAddr{IP: ip, Port: int(port)}
	} else {
		remoteAddr = proxyAddr{net: "tcp", addr: net.JoinHostPort(host, strconv.Itoa(int(port)))}
	}
	return tunnel.NewNetConnAdapter(stream, localAddr, remoteAddr), nil
}

func retryableDirectHandshakeError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var relayErr *protocol.RelayError
	return !errors.As(err, &relayErr)
}

type UDPDialOptions = proxy.UDPDialOptions

func (d *TunnelDialer) DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error) {
	return d.DialUDPWithOptions(ctx, exitNodeID, host, port, UDPDialOptions{})
}

func (d *TunnelDialer) DialUDPWithOptions(ctx context.Context, exitNodeID string, host string, port uint16, opts UDPDialOptions) (net.PacketConn, error) {
	if exitNodeID == "" {
		exitNodeID = d.GetDefaultExitID()
	}

	sess, direct := d.sessionForExit(exitNodeID)
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	if opts.DatagramRequired && !tunnel.PeerSupportsDatagrams(sess) {
		if direct && d.getTunnel != nil {
			relay := d.getTunnel()
			if relay != nil && tunnel.PeerSupportsDatagrams(relay) {
				sess, direct = relay, false
			}
		}
		if !tunnel.PeerSupportsDatagrams(sess) {
			return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required but the selected tunnel does not support them")
		}
	}

	conn, err := d.dialUDPOnSession(ctx, sess, exitNodeID, host, port, opts)
	if err == nil || !direct || d.getTunnel == nil || !d.directFallbackEnabled() || !retryableDirectUDPHandshakeError(ctx, err) {
		return conn, err
	}
	relay := d.getTunnel()
	if relay == nil || relay == sess || (opts.DatagramRequired && !tunnel.PeerSupportsDatagrams(relay)) {
		return nil, err
	}
	return d.dialUDPOnSession(ctx, relay, exitNodeID, host, port, opts)
}

func retryableDirectUDPHandshakeError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var relayErr *protocol.RelayError
	if !errors.As(err, &relayErr) {
		return true
	}
	return relayErr.Code == protocol.ErrCodeDatagramRequired
}

func (d *TunnelDialer) dialUDPOnSession(ctx context.Context, sess tunnel.TunnelSession, exitNodeID, host string, port uint16, opts UDPDialOptions) (net.PacketConn, error) {
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open tunnel stream: %w", err)
	}
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()

	var datagrams *tunnel.DatagramChannel
	if tunnel.PeerSupportsDatagrams(sess) {
		datagrams, err = tunnel.OpenDatagramChannel(sess, 0)
		if err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("failed to open UDP datagram association: %w", err)
		}
	}
	keepDatagrams := false
	defer func() {
		if datagrams != nil && !keepDatagrams {
			_ = datagrams.Close()
		}
	}()

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	}

	reqID := d.nextRequestID()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenUDP,
		RequestID: reqID, ExitDeviceID: exitNodeID,
	}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write stream header: %w", err)
	}

	req := protocol.OpenUDPRequest{
		RequestID: reqID, Host: host, Port: port, TimeoutMs: 10000,
		Mode: protocol.UDPModeStream, DatagramRequired: opts.DatagramRequired,
	}
	if datagrams != nil {
		req.Mode = protocol.UDPModeDatagram
		req.AssociationID = datagrams.ID
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write open request: %w", err)
	}

	var resp protocol.OpenUDPResponse
	if err := protocol.ReadJSON(stream, &resp); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to read open response: %w", err)
	}
	if !resp.Success {
		_ = stream.Close()
		code := resp.ErrorCode
		if code == "" {
			code = protocol.ErrCodeInternalError
		}
		msg := resp.ErrorMessage
		if msg == "" {
			msg = fmt.Sprintf("dial udp target %s:%d failed via exit %s", host, port, exitNodeID)
		}
		return nil, protocol.NewRelayError(code, msg)
	}
	if opts.DatagramRequired && resp.Mode != protocol.UDPModeDatagram {
		_ = stream.Close()
		return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required but the selected tunnel downgraded to reliable stream mode")
	}

	stopCancel()
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})

	var remoteAddr net.Addr
	if ip := net.ParseIP(host); ip != nil {
		remoteAddr = &net.UDPAddr{IP: ip, Port: int(port)}
	} else {
		remoteAddr = proxyAddr{net: "udp", addr: net.JoinHostPort(host, strconv.Itoa(int(port)))}
	}

	if resp.Mode == protocol.UDPModeDatagram {
		if datagrams == nil || resp.AssociationID != datagrams.ID {
			_ = stream.Close()
			return nil, fmt.Errorf("invalid UDP association negotiation")
		}
		keepDatagrams = true
		return tunnel.NewUDPDatagramConn(datagrams, stream, remoteAddr), nil
	}
	if resp.Mode != "" && resp.Mode != protocol.UDPModeStream {
		_ = stream.Close()
		return nil, fmt.Errorf("unsupported UDP mode %q", resp.Mode)
	}
	return newUDPTunnelConn(stream, remoteAddr), nil
}

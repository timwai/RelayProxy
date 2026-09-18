package client

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
}

func NewTunnelDialer(getTunnel func() tunnel.TunnelSession, getClientID func() string) *TunnelDialer {
	return &TunnelDialer{
		getTunnel:   getTunnel,
		getClientID: getClientID,
	}
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
	sess := d.getTunnel()
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}

	if exitNodeID == "" {
		exitNodeID = d.GetDefaultExitID()
	}
	// Empty exitNodeID is allowed: relay auto-selects when exactly one authorized exit is online (P3-1).

	// 1. Open stream on tunnel session
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

	// 2. Write StreamHeader
	header := &protocol.StreamHeader{
		Magic:        protocol.MagicHeader,
		Version:      protocol.CurrentVersion,
		Type:         protocol.FrameTypeOpenTCP,
		RequestID:    reqID,
		ExitDeviceID: exitNodeID,
	}
	if err := protocol.WriteStreamHeader(stream, header); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to write stream header: %w", err)
	}

	// 3. Write OpenTCPRequest
	req := protocol.OpenTCPRequest{
		RequestID: reqID,
		Host:      host,
		Port:      port,
		TimeoutMs: 10000,
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to write open request: %w", err)
	}

	// 4. Read OpenTCPResponse
	var resp protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &resp); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to read open response: %w", err)
	}

	if !resp.Success {
		stream.Close()
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

	// Clear handshake deadline for raw proxy data transfer
	stopCancel()
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})

	// 5. Wrap stream in net.Conn adapter without local DNS resolution
	localAddr := sess.LocalAddr()
	var remoteAddr net.Addr
	if ip := net.ParseIP(host); ip != nil {
		remoteAddr = &net.TCPAddr{IP: ip, Port: int(port)}
	} else {
		remoteAddr = proxyAddr{net: "tcp", addr: net.JoinHostPort(host, strconv.Itoa(int(port)))}
	}

	return tunnel.NewNetConnAdapter(stream, localAddr, remoteAddr), nil
}

type UDPDialOptions = proxy.UDPDialOptions

func (d *TunnelDialer) DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error) {
	return d.DialUDPWithOptions(ctx, exitNodeID, host, port, UDPDialOptions{})
}

func (d *TunnelDialer) DialUDPWithOptions(ctx context.Context, exitNodeID string, host string, port uint16, opts UDPDialOptions) (net.PacketConn, error) {
	sess := d.getTunnel()
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	if opts.DatagramRequired && !tunnel.PeerSupportsDatagrams(sess) {
		return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required but the client tunnel does not support them")
	}

	if exitNodeID == "" {
		exitNodeID = d.GetDefaultExitID()
	}

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

	header := &protocol.StreamHeader{
		Magic:        protocol.MagicHeader,
		Version:      protocol.CurrentVersion,
		Type:         protocol.FrameTypeOpenUDP,
		RequestID:    reqID,
		ExitDeviceID: exitNodeID,
	}
	if err := protocol.WriteStreamHeader(stream, header); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to write stream header: %w", err)
	}

	req := protocol.OpenUDPRequest{
		RequestID:        reqID,
		Host:             host,
		Port:             port,
		TimeoutMs:        10000,
		Mode:             protocol.UDPModeStream,
		DatagramRequired: opts.DatagramRequired,
	}
	if datagrams != nil {
		req.Mode = protocol.UDPModeDatagram
		req.AssociationID = datagrams.ID
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to write open request: %w", err)
	}

	var resp protocol.OpenUDPResponse
	if err := protocol.ReadJSON(stream, &resp); err != nil {
		stream.Close()
		return nil, fmt.Errorf("failed to read open response: %w", err)
	}

	if !resp.Success {
		stream.Close()
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
		return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required but the relay selected reliable stream fallback")
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

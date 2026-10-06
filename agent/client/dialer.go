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

	p2presume "relayproxy/internal/p2p/resume"
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

// SelectedSession carries the concrete proxy path with the tunnel session.
// Direct-path selection must not infer Public Direct vs P2P from a boolean.
type SelectedSession struct {
	Session tunnel.TunnelSession
	Path    protocol.ProxyPath
}

func (s SelectedSession) IsDirect() bool { return s.Path.IsDirect() }

type TunnelDialer struct {
	getTunnel     func() tunnel.TunnelSession
	getClientID   func() string
	defaultExitID atomic.Pointer[string]
	requestSeq    atomic.Uint64

	directMu             sync.RWMutex
	getDirect            func(exitDeviceID string) (SelectedSession, bool)
	ensureDirect         func(exitDeviceID string)
	directMode           string
	directFallback       bool
	noteFallback         func(exitDeviceID string, path protocol.ProxyPath)
	noteDirectFailure    func(exitDeviceID string, path protocol.ProxyPath, reason string)
	streamResume         bool
	resumeReplayLimit    int
	directAttemptTimeout time.Duration
}

const defaultDirectAttemptTimeout = 2 * time.Second

func NewTunnelDialer(getTunnel func() tunnel.TunnelSession, getClientID func() string) *TunnelDialer {
	return &TunnelDialer{
		getTunnel: getTunnel, getClientID: getClientID,
		directMode: "auto", directFallback: true,
		directAttemptTimeout: defaultDirectAttemptTimeout,
	}
}

// ConfigurePathProvider installs a direct-path provider. Providers return the
// concrete path together with a READY session, allowing Public Direct and P2P
// to coexist without path inference.
func (d *TunnelDialer) ConfigurePathProvider(
	get func(exitDeviceID string) (SelectedSession, bool),
	ensure func(exitDeviceID string),
) {
	d.directMu.Lock()
	d.getDirect = get
	d.ensureDirect = ensure
	d.directMu.Unlock()
}

// ConfigureDirectPath preserves the existing P2P integration while mapping it
// onto the path-neutral provider model.
func (d *TunnelDialer) ConfigureDirectPath(
	get func(exitDeviceID string) (tunnel.TunnelSession, bool),
	ensure func(exitDeviceID string),
) {
	var provider func(exitDeviceID string) (SelectedSession, bool)
	if get != nil {
		provider = func(exitDeviceID string) (SelectedSession, bool) {
			session, ok := get(exitDeviceID)
			if !ok || session == nil {
				return SelectedSession{}, false
			}
			return SelectedSession{Session: session, Path: protocol.ProxyPathP2PQUIC}, true
		}
	}
	d.ConfigurePathProvider(provider, ensure)
}

func (d *TunnelDialer) ConfigureDirectPolicy(mode string, fallback bool) {
	d.directMu.Lock()
	d.directMode, d.directFallback = mode, fallback
	d.directMu.Unlock()
}

// ConfigureDirectAttemptTimeout bounds a READY direct-path handshake when
// Relay fallback is enabled. The caller's original context is retained for
// the fallback, so a stale direct session cannot consume the entire deadline.
func (d *TunnelDialer) ConfigureDirectAttemptTimeout(timeout time.Duration) {
	d.directMu.Lock()
	if timeout <= 0 {
		timeout = defaultDirectAttemptTimeout
	}
	d.directAttemptTimeout = timeout
	d.directMu.Unlock()
}

func (d *TunnelDialer) ConfigurePathMetrics(noteFallback func(exitDeviceID string, path protocol.ProxyPath)) {
	d.directMu.Lock()
	d.noteFallback = noteFallback
	d.directMu.Unlock()
}

func (d *TunnelDialer) ConfigureDirectMetrics(noteFallback func(exitDeviceID string)) {
	if noteFallback == nil {
		d.ConfigurePathMetrics(nil)
		return
	}
	d.ConfigurePathMetrics(func(exitDeviceID string, _ protocol.ProxyPath) {
		noteFallback(exitDeviceID)
	})
}

func (d *TunnelDialer) ConfigurePathFailure(noteFailure func(exitDeviceID string, path protocol.ProxyPath, reason string)) {
	d.directMu.Lock()
	d.noteDirectFailure = noteFailure
	d.directMu.Unlock()
}

func (d *TunnelDialer) ConfigureDirectFailure(noteFailure func(exitDeviceID, reason string)) {
	if noteFailure == nil {
		d.ConfigurePathFailure(nil)
		return
	}
	d.ConfigurePathFailure(func(exitDeviceID string, _ protocol.ProxyPath, reason string) {
		noteFailure(exitDeviceID, reason)
	})
}

func (d *TunnelDialer) ConfigureStreamResume(enabled bool, replayLimit int) {
	d.directMu.Lock()
	d.streamResume = enabled
	d.resumeReplayLimit = replayLimit
	d.directMu.Unlock()
}

func (d *TunnelDialer) streamResumeConfig() (bool, int) {
	d.directMu.RLock()
	defer d.directMu.RUnlock()
	return d.streamResume, d.resumeReplayLimit
}

func (d *TunnelDialer) recordPathFailure(exitDeviceID string, path protocol.ProxyPath, err error) {
	if exitDeviceID == "" || !path.IsDirect() || err == nil {
		return
	}
	d.directMu.RLock()
	note := d.noteDirectFailure
	d.directMu.RUnlock()
	if note != nil {
		note(exitDeviceID, path, err.Error())
	}
}

func (d *TunnelDialer) recordFallback(exitDeviceID string, path protocol.ProxyPath) {
	if exitDeviceID == "" || !path.IsDirect() {
		return
	}
	d.directMu.RLock()
	note := d.noteFallback
	d.directMu.RUnlock()
	if note != nil {
		note(exitDeviceID, path)
	}
}

func (d *TunnelDialer) directFallbackEnabled() bool {
	d.directMu.RLock()
	defer d.directMu.RUnlock()
	return d.directMode != "p2p_only" && d.directMode != "direct_only" && d.directFallback
}

func (d *TunnelDialer) alternateDirectSession(exitDeviceID string, failed SelectedSession) SelectedSession {
	d.directMu.RLock()
	getDirect := d.getDirect
	mode := d.directMode
	d.directMu.RUnlock()
	if getDirect == nil || exitDeviceID == "" ||
		(mode != "direct_only" && mode != "auto") {
		return SelectedSession{}
	}
	selected, ok := getDirect(exitDeviceID)
	if !ok || selected.Session == nil || !selected.Path.IsDirect() ||
		selected.Session == failed.Session || selected.Path == failed.Path {
		return SelectedSession{}
	}
	return selected
}

func (d *TunnelDialer) relayFallbackSession(failed tunnel.TunnelSession) tunnel.TunnelSession {
	if !d.directFallbackEnabled() || d.getTunnel == nil {
		return nil
	}
	relay := d.getTunnel()
	if relay == nil || relay == failed {
		return nil
	}
	return relay
}

// prepareDirectFallback quarantines the failed path before asking the provider
// for another READY direct mechanism. When Relay is already known to be
// available, record the fallback first so legacy P2P path reports retain their
// existing metric ordering.
func (d *TunnelDialer) prepareDirectFallback(exitDeviceID string, failed SelectedSession, err error, relay tunnel.TunnelSession) SelectedSession {
	precounted := relay != nil
	if precounted {
		d.recordFallback(exitDeviceID, failed.Path)
	}
	d.recordPathFailure(exitDeviceID, failed.Path, err)
	alternate := d.alternateDirectSession(exitDeviceID, failed)
	if alternate.Session != nil && !precounted {
		d.recordFallback(exitDeviceID, failed.Path)
	}
	return alternate
}

func (d *TunnelDialer) directAttemptContext(parent context.Context) (context.Context, context.CancelFunc) {
	d.directMu.RLock()
	timeout := d.directAttemptTimeout
	d.directMu.RUnlock()
	if timeout <= 0 {
		timeout = defaultDirectAttemptTimeout
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(parent)
		}
		// Reserve at least half of the caller's remaining budget for Relay.
		timeout = min(timeout, remaining/2)
	}
	return context.WithTimeout(parent, timeout)
}

func (d *TunnelDialer) selectedSessionForExit(exitDeviceID string) SelectedSession {
	// The reserved server exit lives inside the Relay process, not behind an
	// authenticated direct peer session.
	if exitDeviceID == protocol.ServerExitDeviceID {
		if d.getTunnel == nil {
			return SelectedSession{}
		}
		relay := d.getTunnel()
		return SelectedSession{Session: relay, Path: relaySessionPath(relay)}
	}
	d.directMu.RLock()
	getDirect, ensureDirect := d.getDirect, d.ensureDirect
	mode := d.directMode
	d.directMu.RUnlock()
	if mode != "relay_only" && exitDeviceID != "" {
		if getDirect != nil {
			if selected, ok := getDirect(exitDeviceID); ok && selected.Session != nil &&
				selected.Path.IsDirect() && (mode != "p2p_only" || selected.Path == protocol.ProxyPathP2PQUIC) {
				return selected
			}
		}
		if ensureDirect != nil {
			ensureDirect(exitDeviceID)
		}
		if mode == "p2p_only" || mode == "direct_only" {
			return SelectedSession{}
		}
	}
	if d.getTunnel == nil {
		return SelectedSession{}
	}
	relay := d.getTunnel()
	return SelectedSession{Session: relay, Path: relaySessionPath(relay)}
}

func (d *TunnelDialer) openProxyStream(ctx context.Context, exitDeviceID string) (SelectedSession, tunnel.TunnelStream, error) {
	selected := d.selectedSessionForExit(exitDeviceID)
	if selected.Session == nil {
		return SelectedSession{}, nil, fmt.Errorf("tunnel is not connected")
	}
	stream, err := selected.Session.OpenStream(ctx)
	if err == nil {
		return selected, stream, nil
	}
	if !selected.IsDirect() {
		return SelectedSession{}, nil, err
	}

	relay := d.relayFallbackSession(selected.Session)
	alternate := d.prepareDirectFallback(exitDeviceID, selected, err, relay)
	if alternate.Session != nil {
		stream, alternateErr := alternate.Session.OpenStream(ctx)
		if alternateErr == nil {
			return alternate, stream, nil
		}
		if relay == nil || relay == alternate.Session {
			relay = d.relayFallbackSession(alternate.Session)
		}
		if relay != nil {
			d.recordFallback(exitDeviceID, alternate.Path)
		}
		d.recordPathFailure(exitDeviceID, alternate.Path, alternateErr)
		if relay == nil {
			return SelectedSession{}, nil, alternateErr
		}
	} else if relay == nil {
		return SelectedSession{}, nil, err
	}

	stream, relayErr := relay.OpenStream(ctx)
	if relayErr != nil {
		return SelectedSession{}, nil, fmt.Errorf("direct stream failed: %v; relay fallback failed: %w", err, relayErr)
	}
	return SelectedSession{Session: relay, Path: relaySessionPath(relay)}, stream, nil
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

	selected := d.selectedSessionForExit(exitNodeID)
	sess := selected.Session
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	direct := selected.IsDirect()
	attemptCtx, cancelAttempt := ctx, func() {}
	if direct && d.directFallbackEnabled() {
		attemptCtx, cancelAttempt = d.directAttemptContext(ctx)
	}
	allowResume := direct && tunnel.PeerSupportsStreamResume(sess)
	conn, err := d.dialTCPOnSession(attemptCtx, sess, exitNodeID, host, port, allowResume, selected.Path)
	retryableDirectFailure := direct && retryableDirectHandshakeError(ctx, err)
	if err == nil || !retryableDirectFailure {
		cancelAttempt()
		return conn, err
	}

	relay := d.relayFallbackSession(sess)
	alternate := d.prepareDirectFallback(exitNodeID, selected, err, relay)
	if alternate.Session != nil && attemptCtx.Err() == nil {
		allowAlternateResume := tunnel.PeerSupportsStreamResume(alternate.Session)
		conn, alternateErr := d.dialTCPOnSession(
			attemptCtx, alternate.Session, exitNodeID, host, port,
			allowAlternateResume, alternate.Path,
		)
		if alternateErr == nil || !retryableDirectHandshakeError(ctx, alternateErr) {
			cancelAttempt()
			return conn, alternateErr
		}
		if relay == nil || relay == alternate.Session {
			relay = d.relayFallbackSession(alternate.Session)
		}
		if relay != nil {
			d.recordFallback(exitNodeID, alternate.Path)
		}
		d.recordPathFailure(exitNodeID, alternate.Path, alternateErr)
		err = alternateErr
	}
	cancelAttempt()

	if relay == nil {
		return nil, err
	}
	return d.dialTCPOnSession(ctx, relay, exitNodeID, host, port, false, relaySessionPath(relay))
}

func (d *TunnelDialer) dialTCPOnSession(ctx context.Context, sess tunnel.TunnelSession, exitNodeID, host string, port uint16, allowResume bool, path protocol.ProxyPath) (net.Conn, error) {
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
	var resumeState *p2presume.StreamState
	var resumeWire *protocol.TCPResumeBinding
	if enabled, replayLimit := d.streamResumeConfig(); enabled && allowResume {
		resumeState, err = p2presume.NewRandomStreamState(replayLimit)
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
		binding, bindErr := resumeState.Binding(p2presume.BindOpen, 1)
		if bindErr != nil {
			_ = stream.Close()
			return nil, bindErr
		}
		resumeWire, bindErr = p2presume.BindingToProtocol(binding, protocol.TCPResumeModeOpen)
		if bindErr != nil {
			_ = stream.Close()
			return nil, bindErr
		}
	}

	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenTCP,
		RequestID: reqID, ExitDeviceID: exitNodeID,
	}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("failed to write stream header: %w", err)
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPRequest{
		RequestID: reqID, Host: host, Port: port, TimeoutMs: 10000, Resume: resumeWire,
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

	if resumeState != nil && resp.Resume != nil {
		peer, err := p2presume.ResponseBindingFromProtocol(resp.Resume)
		if err != nil || peer.Generation != 1 || peer.Identity != resumeState.Identity() ||
			peer.SendOffset != 0 || peer.ReceiveOffset != 0 {
			_ = stream.Close()
			return nil, fmt.Errorf("invalid resumable TCP open response")
		}
		endpoint, err := p2presume.NewEndpoint(resumeState)
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
		if err := endpoint.Bind(stream, 1); err != nil {
			_ = endpoint.Close()
			return nil, err
		}
		return newResumableTCPConn(d, endpoint, resumeState, exitNodeID, host, port, localAddr, remoteAddr, path), nil
	}
	return &tcpPathConn{
		NetConnAdapter: &tunnel.NetConnAdapter{Stream: stream, LocalAddrVal: localAddr, RemoteAddrVal: remoteAddr},
		path:           path,
	}, nil
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

	selected := d.selectedSessionForExit(exitNodeID)
	sess := selected.Session
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	direct := selected.IsDirect()
	if opts.DatagramRequired && !tunnel.PeerSupportsDatagrams(sess) {
		if direct {
			relay := d.relayFallbackSession(sess)
			alternate := d.prepareDirectFallback(
				exitNodeID, selected,
				protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "selected direct path does not support native UDP datagrams"),
				relay,
			)
			if alternate.Session != nil && tunnel.PeerSupportsDatagrams(alternate.Session) {
				selected, sess = alternate, alternate.Session
				direct = true
			} else if relay != nil && tunnel.PeerSupportsDatagrams(relay) {
				selected = SelectedSession{Session: relay, Path: relaySessionPath(relay)}
				sess, direct = relay, false
			}
		}
		if !tunnel.PeerSupportsDatagrams(sess) {
			return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required but the selected tunnel does not support them")
		}
	}

	attemptCtx, cancelAttempt := ctx, func() {}
	if direct && d.directFallbackEnabled() {
		attemptCtx, cancelAttempt = d.directAttemptContext(ctx)
	}
	conn, err := d.dialUDPOnSession(attemptCtx, sess, exitNodeID, host, port, opts)
	retryableDirectFailure := direct && retryableDirectUDPHandshakeError(ctx, err)
	if err == nil || !retryableDirectFailure {
		cancelAttempt()
		return conn, err
	}

	relay := d.relayFallbackSession(sess)
	alternate := d.prepareDirectFallback(exitNodeID, selected, err, relay)
	if alternate.Session != nil && attemptCtx.Err() == nil {
		if !opts.DatagramRequired || tunnel.PeerSupportsDatagrams(alternate.Session) {
			conn, alternateErr := d.dialUDPOnSession(attemptCtx, alternate.Session, exitNodeID, host, port, opts)
			if alternateErr == nil || !retryableDirectUDPHandshakeError(ctx, alternateErr) {
				cancelAttempt()
				return conn, alternateErr
			}
			if relay == nil || relay == alternate.Session {
				relay = d.relayFallbackSession(alternate.Session)
			}
			if relay != nil {
				d.recordFallback(exitNodeID, alternate.Path)
			}
			d.recordPathFailure(exitNodeID, alternate.Path, alternateErr)
			err = alternateErr
		}
	}
	cancelAttempt()

	if relay == nil || (opts.DatagramRequired && !tunnel.PeerSupportsDatagrams(relay)) {
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
	useNativeDatagrams := opts.DatagramRequired || !opts.PreferStream
	if useNativeDatagrams && tunnel.PeerSupportsDatagrams(sess) {
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

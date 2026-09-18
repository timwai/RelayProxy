package gateway

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

var (
	errExitOffline   = errors.New("Exit node is offline or not found")
	errNoExitOnline  = errors.New("No online exit node available; specify exitDeviceId or bring an exit online")
	errMultipleExits = errors.New("Multiple online exits available; specify exitDeviceId explicitly")
)

type StreamRouter struct {
	sessions          *session.Manager
	aclChecker        *acl.Checker
	authChecker       func(clientDeviceID, exitDeviceID string) (bool, error)
	rdpChecker        func(controllerDeviceID, targetDeviceID string) (bool, error)
	rdpControlHandler func(context.Context, tunnel.TunnelStream, *session.DeviceSession)
	onAudit           func(audit *repository.ConnectionAudit)
}

// SetRDPChecker installs the server-side Controller -> Target authorization
// check. RDP admission is intentionally separate from generic exit access.
func (r *StreamRouter) SetRDPChecker(fn func(controllerDeviceID, targetDeviceID string) (bool, error)) {
	r.rdpChecker = fn
}

// SetRDPControlHandler installs the short-lived rendezvous control handler.
// It is deliberately separate from the heartbeat stream: the latter has one
// reader and one writer for ping/pong and must not be multiplexed with P2P
// signaling frames.
func (r *StreamRouter) SetRDPControlHandler(fn func(context.Context, tunnel.TunnelStream, *session.DeviceSession)) {
	r.rdpControlHandler = fn
}

func NewStreamRouter(
	sessions *session.Manager,
	aclChecker *acl.Checker,
	authChecker func(clientDeviceID, exitDeviceID string) (bool, error),
	onAudit func(audit *repository.ConnectionAudit),
) *StreamRouter {
	return &StreamRouter{
		sessions:    sessions,
		aclChecker:  aclChecker,
		authChecker: authChecker,
		onAudit:     onAudit,
	}
}

func (r *StreamRouter) emitAudit(a *repository.ConnectionAudit) {
	if r.onAudit == nil || a == nil {
		return
	}
	r.onAudit(a)
}

func (r *StreamRouter) authorizeExit(client, exit *session.DeviceSession) (bool, error) {
	if client == nil || exit == nil {
		return false, nil
	}
	// Ownership is loaded as part of the authenticated session snapshot.
	// Authorization changes invalidate that session, so matching owners can be
	// checked without SQLite on every data stream.
	if client.OwnerUserID != "" && exit.OwnerUserID != "" {
		return client.OwnerUserID == exit.OwnerUserID, nil
	}
	if r.authChecker != nil {
		return r.authChecker(client.DeviceID, exit.DeviceID)
	}
	return true, nil
}

// resolveExitSession returns an online exit session.
// Empty exitDeviceID triggers auto-select when exactly one authorized exit is online (P3-1).
func (r *StreamRouter) resolveExitSession(client *session.DeviceSession, exitDeviceID string) (*session.DeviceSession, error) {
	if exitDeviceID != "" {
		exitSession, exists := r.sessions.Get(exitDeviceID)
		if !exists || !exitSession.IsExit() {
			return nil, errExitOffline
		}
		return exitSession, nil
	}

	if client != nil && client.OwnerUserID != "" {
		candidate, count := r.sessions.UniqueExitForOwner(client.OwnerUserID)
		switch count {
		case 0:
			return nil, errNoExitOnline
		case 1:
			return candidate, nil
		default:
			return nil, errMultipleExits
		}
	}

	exits := r.sessions.GetExits()
	var candidate *session.DeviceSession
	count := 0
	for _, e := range exits {
		ok, err := r.authorizeExit(client, e)
		if err != nil || !ok {
			continue
		}
		candidate = e
		count++
		if count > 1 {
			return nil, errMultipleExits
		}
	}
	if count == 0 {
		return nil, errNoExitOnline
	}
	return candidate, nil
}

// HandleClientStream processes a new stream opened by a Client
func (r *StreamRouter) HandleClientStream(ctx context.Context, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession) {
	defer clientStream.Close()
	stopCancel := tunnel.InterruptOnCancel(ctx, clientStream)
	defer stopCancel()
	handshakeDeadline := time.Now().Add(15 * time.Second)
	_ = clientStream.SetDeadline(handshakeDeadline)

	// 1. Read StreamHeader
	header, err := protocol.ReadStreamHeader(clientStream)
	if err != nil {
		log.Printf("[StreamRouter] Failed to read StreamHeader from device %s: %v", clientSession.DeviceID, err)
		return
	}
	header.ClientDeviceID = clientSession.DeviceID

	switch header.Type {
	case protocol.FrameTypeOpenTCP:
		r.handleOpenTCP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeOpenUDP:
		r.handleOpenUDP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeOpenRDP:
		r.handleOpenRDPTCP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeOpenRDPUDP:
		r.handleOpenRDPUDP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeRDPControl:
		if r.rdpControlHandler != nil && (containsCapability(clientSession.Grants, protocol.CapabilityRDPClient) || containsCapability(clientSession.Grants, protocol.CapabilityRDPHost)) {
			r.rdpControlHandler(ctx, clientStream, clientSession)
		}
	default:
		log.Printf("[StreamRouter] Unsupported FrameType %d from device %s", header.Type, clientSession.DeviceID)
	}
}

func (r *StreamRouter) handleOpenTCP(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.OpenTCPRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		log.Printf("[StreamRouter] Failed to read OpenTCPRequest: %v", err)
		return
	}
	if !containsCapability(clientSession.Grants, protocol.CapabilityProxyClient) {
		log.Printf("[StreamRouter] Device %s opened a TCP proxy stream without proxy.client capability", clientSession.DeviceID)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeAccessDenied,
			ErrorMessage: "Device is not approved for proxy client access",
		})
		return
	}

	now := time.Now()
	baseAudit := func(result, errCode, resolvedIP string) *repository.ConnectionAudit {
		return &repository.ConnectionAudit{
			UserID:         clientSession.OwnerUserID,
			ClientDeviceID: clientSession.DeviceID,
			ExitDeviceID:   header.ExitDeviceID,
			Protocol:       "tcp",
			TargetHost:     req.Host,
			TargetPort:     int(req.Port),
			ResolvedIP:     resolvedIP,
			StartedAt:      now,
			EndedAt:        time.Now(),
			Result:         result,
			ErrorCode:      errCode,
		}
	}

	// 1. Resolve exit: explicit ID, or auto-pick when exactly one authorized exit is online (P3-1)
	exitDeviceID := header.ExitDeviceID
	exitSession, resolveErr := r.resolveExitSession(clientSession, exitDeviceID)
	if resolveErr != nil {
		code := protocol.ErrCodeExitOffline
		msg := resolveErr.Error()
		if resolveErr == errMultipleExits {
			code = protocol.ErrCodeInvalidRequest
		}
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    code,
			ErrorMessage: msg,
		})
		header.ExitDeviceID = exitDeviceID
		r.emitAudit(baseAudit("EXIT_RESOLVE_FAILED", code, ""))
		return
	}
	exitDeviceID = exitSession.DeviceID
	header.ExitDeviceID = exitDeviceID

	// 2. Validate Client -> Exit authorization (P0-2)
	// (auto-select already filtered by auth; explicit ID still needs the check)
	authorized, authErr := r.authorizeExit(clientSession, exitSession)
	if authErr != nil || !authorized {
		log.Printf("[StreamRouter] Unauthorized access: client %s -> exit %s", clientSession.DeviceID, exitDeviceID)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeACLDenied,
			ErrorMessage: "Client is not authorized to access this exit node",
		})
		r.emitAudit(baseAudit("UNAUTHORIZED", protocol.ErrCodeACLDenied, ""))
		return
	}

	// Bind the Relay's own policy, replacing any policy supplied by the client.
	relayPolicy, policyErr := r.targetPolicy(ctx, exitSession, req.Host, req.Port, "tcp")
	req.RelayPolicy = relayPolicy
	if policyErr != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeACLDenied,
			ErrorMessage: policyErr.Error(),
		})
		r.emitAudit(baseAudit("ACL_DENIED", protocol.ErrCodeACLDenied, ""))
		return
	}

	// 4. Open reverse stream to Exit Node
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	} else if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	if d := time.Now().Add(timeout); d.Before(handshakeDeadline) {
		handshakeDeadline = d
	}
	openCtx, openCancel := context.WithDeadline(ctx, handshakeDeadline)
	defer openCancel()

	exitStream, err := exitSession.Tunnel.OpenStream(openCtx)
	if err != nil {
		log.Printf("[StreamRouter] Failed to open stream to exit node %s: %v", exitDeviceID, err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Failed to open stream to exit node",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}
	defer exitStream.Close()
	stopExitCancel := tunnel.InterruptOnCancel(ctx, exitStream)
	defer stopExitCancel()

	// Apply handshake deadline to the entire exit OpenTCP exchange (P1-1)
	_ = exitStream.SetDeadline(handshakeDeadline)

	// 5. Send StreamHeader and OpenTCPRequest to Exit Node
	if err := protocol.WriteStreamHeader(exitStream, header); err != nil {
		log.Printf("[StreamRouter] Failed to write header to exit stream: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Exit node closed the stream before receiving the request",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}
	if err := protocol.WriteJSON(exitStream, req); err != nil {
		log.Printf("[StreamRouter] Failed to write req to exit stream: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Exit node closed the stream before receiving the request",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}

	// 6. Read OpenTCPResponse from Exit Node
	var exitResp protocol.OpenTCPResponse
	if err := protocol.ReadJSON(exitStream, &exitResp); err != nil {
		log.Printf("[StreamRouter] Failed to read response from exit node: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeConnectTimeout,
			ErrorMessage: "Timeout reading response from exit node",
		})
		r.emitAudit(baseAudit("EXIT_RESPONSE_FAILED", protocol.ErrCodeConnectTimeout, ""))
		return
	}
	_ = exitStream.SetDeadline(time.Time{})

	// 7. Forward response to Client
	if err := protocol.WriteJSON(clientStream, exitResp); err != nil {
		log.Printf("[StreamRouter] Failed to forward exit response to client: %v", err)
		return
	}

	if !exitResp.Success {
		r.emitAudit(baseAudit("EXIT_DIAL_FAILED", exitResp.ErrorCode, exitResp.RemoteIP))
		return
	}
	_ = clientStream.SetDeadline(time.Time{})

	// 8. Success: Full duplex raw data transfer
	startTime := time.Now()
	clientSession.ActiveStreams.Add(1)
	exitSession.ActiveStreams.Add(1)
	clientSession.ActiveExitID.Store(&exitDeviceID)

	bytesUp, bytesDown := r.pipeStreams(ctx, clientStream, exitStream, clientSession, exitSession)

	if clientSession.ActiveStreams.Add(-1) <= 0 {
		clientSession.ActiveExitID.CompareAndSwap(&exitDeviceID, nil)
	}
	exitSession.ActiveStreams.Add(-1)
	r.emitAudit(&repository.ConnectionAudit{
		UserID:         clientSession.OwnerUserID,
		ClientDeviceID: clientSession.DeviceID,
		ExitDeviceID:   exitDeviceID,
		Protocol:       "tcp",
		TargetHost:     req.Host,
		TargetPort:     int(req.Port),
		ResolvedIP:     exitResp.RemoteIP,
		StartedAt:      startTime,
		EndedAt:        time.Now(),
		BytesUp:        bytesUp,
		BytesDown:      bytesDown,
		Result:         "SUCCESS",
	})
}

func (r *StreamRouter) handleOpenUDP(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.OpenUDPRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		log.Printf("[StreamRouter] Failed to read OpenUDPRequest: %v", err)
		return
	}
	if !containsCapability(clientSession.Grants, protocol.CapabilityProxyClient) {
		log.Printf("[StreamRouter] Device %s opened a UDP proxy stream without proxy.client capability", clientSession.DeviceID)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeAccessDenied,
			ErrorMessage: "Device is not approved for proxy client access",
		})
		return
	}
	if req.Mode != "" && req.Mode != protocol.UDPModeStream && req.Mode != protocol.UDPModeDatagram {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "unsupported UDP mode"})
		return
	}

	now := time.Now()
	baseAudit := func(result, errCode, resolvedIP string) *repository.ConnectionAudit {
		return &repository.ConnectionAudit{
			UserID:         clientSession.OwnerUserID,
			ClientDeviceID: clientSession.DeviceID,
			ExitDeviceID:   header.ExitDeviceID,
			Protocol:       "udp",
			TargetHost:     req.Host,
			TargetPort:     int(req.Port),
			ResolvedIP:     resolvedIP,
			StartedAt:      now,
			EndedAt:        time.Now(),
			Result:         result,
			ErrorCode:      errCode,
		}
	}

	exitDeviceID := header.ExitDeviceID
	exitSession, resolveErr := r.resolveExitSession(clientSession, exitDeviceID)
	if resolveErr != nil {
		code := protocol.ErrCodeExitOffline
		msg := resolveErr.Error()
		if resolveErr == errMultipleExits {
			code = protocol.ErrCodeInvalidRequest
		}
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    code,
			ErrorMessage: msg,
		})
		header.ExitDeviceID = exitDeviceID
		r.emitAudit(baseAudit("EXIT_RESOLVE_FAILED", code, ""))
		return
	}
	exitDeviceID = exitSession.DeviceID
	header.ExitDeviceID = exitDeviceID

	authorized, authErr := r.authorizeExit(clientSession, exitSession)
	if authErr != nil || !authorized {
		log.Printf("[StreamRouter] Unauthorized UDP access: client %s -> exit %s", clientSession.DeviceID, exitDeviceID)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeACLDenied,
			ErrorMessage: "Client is not authorized to access this exit node",
		})
		r.emitAudit(baseAudit("UNAUTHORIZED", protocol.ErrCodeACLDenied, ""))
		return
	}

	// A client cannot weaken or replace the Relay's destination restrictions.
	relayPolicy, policyErr := r.targetPolicy(ctx, exitSession, req.Host, req.Port, "udp")
	req.RelayPolicy = relayPolicy
	if policyErr != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeACLDenied,
			ErrorMessage: policyErr.Error(),
		})
		r.emitAudit(baseAudit("ACL_DENIED", protocol.ErrCodeACLDenied, ""))
		return
	}

	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 10 * time.Second
	} else if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	if d := time.Now().Add(timeout); d.Before(handshakeDeadline) {
		handshakeDeadline = d
	}
	openCtx, openCancel := context.WithDeadline(ctx, handshakeDeadline)
	defer openCancel()

	clientAssociationID := req.AssociationID
	var clientDatagrams, exitDatagrams *tunnel.DatagramChannel
	if req.Mode == protocol.UDPModeDatagram && clientAssociationID != 0 &&
		hasCapability(clientSession, protocol.UDPModeDatagram) && hasCapability(exitSession, protocol.UDPModeDatagram) &&
		tunnel.PeerSupportsDatagrams(clientSession.Tunnel) && tunnel.PeerSupportsDatagrams(exitSession.Tunnel) {
		var channelErr error
		clientDatagrams, channelErr = tunnel.OpenDatagramChannel(clientSession.Tunnel, clientAssociationID)
		if channelErr == nil {
			exitDatagrams, channelErr = tunnel.OpenDatagramChannel(exitSession.Tunnel, 0)
		}
		if clientDatagrams != nil {
			defer clientDatagrams.Close()
		}
		if exitDatagrams != nil {
			defer exitDatagrams.Close()
		}
		if channelErr != nil {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed, ErrorMessage: channelErr.Error()})
			return
		}
		req.AssociationID = exitDatagrams.ID
	} else {
		if req.DatagramRequired {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: "native UDP datagrams are required on both tunnel hops"})
			r.emitAudit(baseAudit("UDP_NEGOTIATION_FAILED", protocol.ErrCodeDatagramRequired, ""))
			return
		}
		req.Mode = protocol.UDPModeStream
		req.AssociationID = 0
	}

	exitStream, err := exitSession.Tunnel.OpenStream(openCtx)
	if err != nil {
		log.Printf("[StreamRouter] Failed to open UDP stream to exit node %s: %v", exitDeviceID, err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Failed to open stream to exit node",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}
	defer exitStream.Close()
	stopExitCancel := tunnel.InterruptOnCancel(ctx, exitStream)
	defer stopExitCancel()

	_ = exitStream.SetDeadline(handshakeDeadline)

	header.Type = protocol.FrameTypeOpenUDP
	if err := protocol.WriteStreamHeader(exitStream, header); err != nil {
		log.Printf("[StreamRouter] Failed to write UDP header to exit stream: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Exit node closed the stream before receiving the request",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}
	if err := protocol.WriteJSON(exitStream, req); err != nil {
		log.Printf("[StreamRouter] Failed to write UDP req to exit stream: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed,
			ErrorMessage: "Exit node closed the stream before receiving the request",
		})
		r.emitAudit(baseAudit("STREAM_OPEN_FAILED", protocol.ErrCodeStreamOpenFailed, ""))
		return
	}

	var exitResp protocol.OpenUDPResponse
	if err := protocol.ReadJSON(exitStream, &exitResp); err != nil {
		log.Printf("[StreamRouter] Failed to read UDP response from exit node: %v", err)
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID:    req.RequestID,
			Success:      false,
			ErrorCode:    protocol.ErrCodeConnectTimeout,
			ErrorMessage: "Timeout reading response from exit node",
		})
		r.emitAudit(baseAudit("EXIT_RESPONSE_FAILED", protocol.ErrCodeConnectTimeout, ""))
		return
	}
	_ = exitStream.SetDeadline(time.Time{})
	if exitResp.Success && exitResp.Mode == protocol.UDPModeDatagram {
		if exitDatagrams == nil || exitResp.AssociationID != exitDatagrams.ID {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "invalid exit UDP negotiation"})
			return
		}
		exitResp.AssociationID = clientAssociationID
	} else if exitResp.Success {
		if req.DatagramRequired {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: "exit did not negotiate required native UDP datagrams"})
			r.emitAudit(baseAudit("UDP_NEGOTIATION_FAILED", protocol.ErrCodeDatagramRequired, ""))
			return
		}
		if exitResp.Mode != "" && exitResp.Mode != protocol.UDPModeStream {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "unsupported exit UDP mode"})
			return
		}
		if clientDatagrams != nil {
			_ = clientDatagrams.Close()
			_ = exitDatagrams.Close()
			clientDatagrams = nil
			exitDatagrams = nil
		}
		exitResp.Mode = protocol.UDPModeStream
		exitResp.AssociationID = 0
	}

	if err := protocol.WriteJSON(clientStream, exitResp); err != nil {
		log.Printf("[StreamRouter] Failed to forward UDP exit response to client: %v", err)
		return
	}

	if !exitResp.Success {
		r.emitAudit(baseAudit("EXIT_DIAL_FAILED", exitResp.ErrorCode, exitResp.RemoteIP))
		return
	}
	_ = clientStream.SetDeadline(time.Time{})

	startTime := time.Now()
	clientSession.ActiveStreams.Add(1)
	exitSession.ActiveStreams.Add(1)
	clientSession.ActiveExitID.Store(&exitDeviceID)

	var bytesUp, bytesDown int64
	if clientDatagrams != nil {
		bytesUp, bytesDown = r.pipeDatagrams(ctx, clientStream, exitStream, clientDatagrams, exitDatagrams, clientSession, exitSession, tunnel.UDPIdleTimeout)
	} else {
		bytesUp, bytesDown = r.pipeStreams(ctx, clientStream, exitStream, clientSession, exitSession)
	}

	if clientSession.ActiveStreams.Add(-1) <= 0 {
		clientSession.ActiveExitID.CompareAndSwap(&exitDeviceID, nil)
	}
	exitSession.ActiveStreams.Add(-1)
	r.emitAudit(&repository.ConnectionAudit{
		UserID:         clientSession.OwnerUserID,
		ClientDeviceID: clientSession.DeviceID,
		ExitDeviceID:   exitDeviceID,
		Protocol:       "udp",
		TargetHost:     req.Host,
		TargetPort:     int(req.Port),
		ResolvedIP:     exitResp.RemoteIP,
		StartedAt:      startTime,
		EndedAt:        time.Now(),
		BytesUp:        bytesUp,
		BytesDown:      bytesDown,
		Result:         "SUCCESS",
	})
}

// defaultStreamIdleTimeout recycles half-closed / stalled proxy streams (P2-2).
const defaultStreamIdleTimeout = 5 * time.Minute

func recordTransfer(c1, c2 *session.DeviceSession, up bool, n int) {
	count := int64(n)
	if up {
		c1.BytesUp.Add(count)
		c2.BytesDown.Add(count)
	} else {
		c2.BytesUp.Add(count)
		c1.BytesDown.Add(count)
	}
}

func (r *StreamRouter) pipeStreams(ctx context.Context, s1, s2 tunnel.TunnelStream, c1, c2 *session.DeviceSession) (int64, int64) {
	return tunnel.Pipe(ctx, s1, s2, defaultStreamIdleTimeout, func(up bool, n int) { recordTransfer(c1, c2, up, n) })
}

func hasCapability(s *session.DeviceSession, name string) bool {
	for _, cap := range s.Capabilities {
		if cap == name {
			return true
		}
	}
	return false
}

// The Exit resolves the target, so it must enforce the same immutable Relay
// policy again against resolved IPs before opening a target socket.
func (r *StreamRouter) targetPolicy(ctx context.Context, exit *session.DeviceSession, host string, port uint16, transport string) (*acl.Policy, error) {
	if r.aclChecker == nil {
		return nil, nil
	}
	if err := r.aclChecker.CheckHostProtocol(ctx, host, port, transport); err != nil {
		return nil, err
	}
	if !hasCapability(exit, protocol.CapabilityTargetACL) {
		return nil, errors.New("exit does not support required target ACL enforcement")
	}
	policy := r.aclChecker.Policy()
	return &policy, nil
}

// Native forwarding changes only the session-scoped association envelope.
// Fragment payloads remain opaque to the Relay, with bounded transport queues.
func (r *StreamRouter) pipeDatagrams(ctx context.Context, s1, s2 tunnel.TunnelStream, d1, d2 *tunnel.DatagramChannel, c1, c2 *session.DeviceSession, idleTimeout time.Duration) (int64, int64) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); _ = d1.Close(); _ = d2.Close(); _ = s1.Close(); _ = s2.Close() }) }
	defer stop()
	var wg sync.WaitGroup
	wg.Add(4)
	finished := make(chan struct{}, 4)
	complete := func() { wg.Done(); finished <- struct{}{} }
	monitor := func(s tunnel.TunnelStream) { defer complete(); var b [1]byte; _, _ = s.Read(b[:]) }
	go monitor(s1)
	go monitor(s2)
	var up, down int64
	var activitySeq atomic.Uint64
	const datagramStatsBatch = 64 * 1024
	forward := func(dst, src *tunnel.DatagramChannel, upward bool, total *int64) {
		defer complete()
		pendingStats := 0
		defer func() {
			if pendingStats > 0 {
				recordTransfer(c1, c2, upward, pendingStats)
			}
		}()
		for {
			frame, err := src.Receive(ctx)
			if err != nil {
				return
			}
			if err := dst.Forward(ctx, frame); err != nil {
				return
			}
			n := len(frame) - protocol.UDPFragmentHeaderSize
			*total += int64(n)
			pendingStats += n
			if pendingStats >= datagramStatsBatch {
				recordTransfer(c1, c2, upward, pendingStats)
				pendingStats = 0
			}
			activitySeq.Add(1)
		}
	}
	go forward(d2, d1, true, &up)
	go forward(d1, d2, false, &down)
	var idleTicker *time.Ticker
	var idleTick <-chan time.Time
	lastActivity := time.Now()
	lastActivitySeq := activitySeq.Load()
	if idleTimeout > 0 {
		idleTicker = time.NewTicker(min(time.Second, idleTimeout))
		idleTick = idleTicker.C
		defer idleTicker.Stop()
	}
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-finished:
			break wait
		case now := <-idleTick:
			seq := activitySeq.Load()
			if seq != lastActivitySeq {
				lastActivitySeq = seq
				lastActivity = now
				continue
			}
			if now.Sub(lastActivity) >= idleTimeout {
				// Avoid closing on activity that raced with this tick.
				if activitySeq.Load() == seq {
					break wait
				}
				lastActivitySeq = activitySeq.Load()
				lastActivity = now
			}
		}
	}
	stop()
	wg.Wait()
	return up, down
}

package gateway

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	agentexit "relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/speedtest"
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
	localExit         *agentexit.Handler
	aclChecker        *acl.Checker
	relayPolicy       *acl.Policy
	authChecker       func(clientDeviceID, exitDeviceID string) (bool, error)
	rdpChecker        func(controllerDeviceID, targetDeviceID string) (bool, error)
	rdpControlHandler func(context.Context, tunnel.TunnelStream, *session.DeviceSession)
	p2pControlHandler func(context.Context, tunnel.TunnelStream, *session.DeviceSession)
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

// SetP2PControlHandler installs proxy direct-path signaling. Routing remains
// unchanged: this handler only coordinates the transport to an already
// selected Exit.
func (r *StreamRouter) SetP2PControlHandler(fn func(context.Context, tunnel.TunnelStream, *session.DeviceSession)) {
	r.p2pControlHandler = fn
}

// SetLocalExit enables the Relay process itself as an egress node. The
// reserved protocol.ServerExitDeviceID selects this path explicitly.
func (r *StreamRouter) SetLocalExit(handler *agentexit.Handler) {
	r.localExit = handler
}

func NewStreamRouter(
	sessions *session.Manager,
	aclChecker *acl.Checker,
	authChecker func(clientDeviceID, exitDeviceID string) (bool, error),
	onAudit func(audit *repository.ConnectionAudit),
) *StreamRouter {
	router := &StreamRouter{
		sessions:    sessions,
		aclChecker:  aclChecker,
		authChecker: authChecker,
		onAudit:     onAudit,
	}
	if aclChecker != nil {
		policy := aclChecker.Policy()
		router.relayPolicy = &policy
	}
	return router
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
	// v4 peers use live policy so disabled identities and narrowed capabilities
	// take effect before their control sessions finish disconnecting.
	if client.IdentityID != "" || exit.IdentityID != "" {
		if client.IdentityID == "" || exit.IdentityID == "" {
			return false, nil
		}
		if r.authChecker != nil {
			return r.authChecker(client.DeviceID, exit.DeviceID)
		}
		return false, nil
	}
	// This fallback is unreachable from the identity-only production Gateway.
	// It remains for controlled legacy data migration and compatibility tests.
	if client.OwnerUserID != "" && exit.OwnerUserID != "" {
		return client.OwnerUserID == exit.OwnerUserID, nil
	}
	if r.authChecker != nil {
		return r.authChecker(client.DeviceID, exit.DeviceID)
	}
	return false, nil
}

// resolveExitSession returns an online exit. A nil session with local=true
// represents the Relay process itself. Empty exitDeviceID auto-selects only
// when exactly one authorized device exit or server exit is available.
func (r *StreamRouter) resolveExitSession(client *session.DeviceSession, exitDeviceID string) (*session.DeviceSession, bool, error) {
	if exitDeviceID != "" {
		if exitDeviceID == protocol.ServerExitDeviceID {
			if r.localExit == nil {
				return nil, false, errExitOffline
			}
			if client != nil && client.IdentityID != "" {
				if r.authChecker == nil {
					return nil, false, errExitOffline
				}
				allowed, err := r.authChecker(client.DeviceID, protocol.ServerExitDeviceID)
				if err != nil {
					return nil, false, err
				}
				if !allowed {
					return nil, false, errExitOffline
				}
			}
			return nil, true, nil
		}
		exitSession, exists := r.sessions.Get(exitDeviceID)
		if !exists || !exitSession.IsExit() {
			return nil, false, errExitOffline
		}
		return exitSession, false, nil
	}

	if client != nil && client.IdentityID != "" {
		var candidate *session.DeviceSession
		count := 0
		local := false
		if r.localExit != nil && r.authChecker != nil {
			allowed, err := r.authChecker(client.DeviceID, protocol.ServerExitDeviceID)
			if err != nil {
				return nil, false, err
			}
			if allowed {
				count = 1
				local = true
			}
		}
		for _, exit := range r.sessions.GetExits() {
			if exit == nil || exit.DeviceID == client.DeviceID {
				continue
			}
			allowed, err := r.authorizeExit(client, exit)
			if err != nil {
				return nil, false, err
			}
			if !allowed {
				continue
			}
			candidate = exit
			count++
			if count > 1 {
				return nil, false, errMultipleExits
			}
		}
		switch count {
		case 0:
			return nil, false, errNoExitOnline
		case 1:
			if local {
				return nil, true, nil
			}
			return candidate, false, nil
		default:
			return nil, false, errMultipleExits
		}
	}

	if client != nil && client.OwnerUserID != "" {
		exits := r.sessions.GetExitsForOwner(client.OwnerUserID)
		count := len(exits)
		if r.localExit != nil {
			count++
		}
		switch count {
		case 0:
			return nil, false, errNoExitOnline
		case 1:
			if r.localExit != nil {
				return nil, true, nil
			}
			return exits[0], false, nil
		default:
			return nil, false, errMultipleExits
		}
	}

	exits := r.sessions.GetExits()
	var candidate *session.DeviceSession
	count := 0
	local := false
	if r.localExit != nil {
		count = 1
		local = true
	}
	for _, e := range exits {
		ok, err := r.authorizeExit(client, e)
		if err != nil || !ok {
			continue
		}
		candidate = e
		local = false
		count++
		if count > 1 {
			return nil, false, errMultipleExits
		}
	}
	if count == 0 {
		return nil, false, errNoExitOnline
	}
	if local {
		return nil, true, nil
	}
	return candidate, false, nil
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
	case protocol.FrameTypeSpeedTest:
		r.handleSpeedTest(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeOpenRDP:
		r.handleOpenRDPTCP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeOpenRDPUDP:
		r.handleOpenRDPUDP(ctx, header, clientStream, clientSession, handshakeDeadline)
	case protocol.FrameTypeRDPControl:
		if r.rdpControlHandler != nil && (containsCapability(clientSession.Grants, protocol.CapabilityRDPClient) || containsCapability(clientSession.Grants, protocol.CapabilityRDPHost)) {
			r.rdpControlHandler(ctx, clientStream, clientSession)
		}
	case protocol.FrameTypeP2PControl:
		if r.p2pControlHandler != nil && containsCapability(clientSession.Capabilities, protocol.CapabilityProxyP2P) &&
			(containsCapability(clientSession.Grants, protocol.CapabilityProxyClient) || containsCapability(clientSession.Grants, protocol.CapabilityProxyExit)) {
			r.p2pControlHandler(ctx, clientStream, clientSession)
		}
	default:
		log.Printf("[StreamRouter] Unsupported FrameType %d from device %s", header.Type, clientSession.DeviceID)
	}
}

func (r *StreamRouter) handleSpeedTest(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.SpeedTestRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		_ = speedtest.WriteError(clientStream, header.RequestID, protocol.ErrCodeInvalidRequest, err)
		return
	}
	if !containsCapability(clientSession.Grants, protocol.CapabilityProxyClient) || !hasCapability(clientSession, protocol.CapabilitySpeedTest) {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeAccessDenied, errors.New("speed test capability is not available"))
		return
	}
	if _, err := speedtest.Validate(req); err != nil {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeInvalidRequest, err)
		return
	}

	exitSession, localExit, err := r.resolveExitSession(clientSession, header.ExitDeviceID)
	if err != nil {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeExitOffline, err)
		return
	}
	if localExit {
		header.ExitDeviceID = protocol.ServerExitDeviceID
		clientSession.ActiveStreams.Add(1)
		activeExitID := header.ExitDeviceID
		clientSession.ActiveExitID.Store(&activeExitID)
		measurement, serveErr := speedtest.Serve(clientStream, req)
		if req.Direction == protocol.SpeedTestUpload {
			clientSession.BytesUp.Add(int64(measurement.Bytes))
		} else {
			clientSession.BytesDown.Add(int64(measurement.Bytes))
		}
		if clientSession.ActiveStreams.Add(-1) <= 0 {
			clientSession.ActiveExitID.CompareAndSwap(&activeExitID, nil)
		}
		if serveErr != nil {
			log.Printf("[SpeedTest] server exit failed for %s: %v", clientSession.DeviceID, serveErr)
		}
		return
	}

	header.ExitDeviceID = exitSession.DeviceID
	authorized, authErr := r.authorizeExit(clientSession, exitSession)
	if authErr != nil || !authorized {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeAccessDenied, errors.New("client is not authorized to access this exit node"))
		return
	}
	if !hasCapability(exitSession, protocol.CapabilitySpeedTest) {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeInvalidRequest, errors.New("selected exit does not support speed tests"))
		return
	}

	openCtx, cancel := context.WithDeadline(ctx, handshakeDeadline)
	defer cancel()
	exitStream, err := exitSession.Tunnel.OpenStream(openCtx)
	if err != nil {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeStreamOpenFailed, err)
		return
	}
	defer exitStream.Close()
	_ = exitStream.SetDeadline(handshakeDeadline)
	if err := protocol.WriteStreamHeader(exitStream, header); err == nil {
		err = protocol.WriteJSON(exitStream, req)
	}
	if err != nil {
		_ = speedtest.WriteError(clientStream, req.RequestID, protocol.ErrCodeStreamOpenFailed, err)
		return
	}
	_ = clientStream.SetDeadline(time.Time{})
	_ = exitStream.SetDeadline(time.Time{})
	clientSession.ActiveStreams.Add(1)
	exitSession.ActiveStreams.Add(1)
	activeExitID := &exitSession.DeviceID
	clientSession.ActiveExitID.Store(activeExitID)
	bytesUp, bytesDown := r.pipeStreams(ctx, clientStream, exitStream, clientSession, exitSession)
	if clientSession.ActiveStreams.Add(-1) <= 0 {
		clientSession.ActiveExitID.CompareAndSwap(activeExitID, nil)
	}
	exitSession.ActiveStreams.Add(-1)
	log.Printf("[SpeedTest] %s -> %s direction=%s up=%d down=%d", clientSession.DeviceID, exitSession.DeviceID, req.Direction, bytesUp, bytesDown)
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
	exitSession, localExit, resolveErr := r.resolveExitSession(clientSession, exitDeviceID)
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
	if localExit {
		exitDeviceID = protocol.ServerExitDeviceID
	} else {
		exitDeviceID = exitSession.DeviceID
	}
	header.ExitDeviceID = exitDeviceID

	// 2. Validate Client -> device Exit authorization (P0-2). The server-local
	// exit is a process-level service guarded by the client's proxy.client grant
	// plus the Relay and server-exit ACLs.
	if !localExit {
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
	}

	if req.Resume != nil {
		var err error
		negotiatedResume := !localExit && proxyStreamResumeNegotiated(clientSession, exitSession)
		req.Resume, err = normalizeTCPResumeBinding(req.Resume, negotiatedResume)
		if err != nil {
			_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
				RequestID:    req.RequestID,
				Success:      false,
				ErrorCode:    protocol.ErrCodeInvalidRequest,
				ErrorMessage: err.Error(),
			})
			r.emitAudit(baseAudit("RESUME_INVALID", protocol.ErrCodeInvalidRequest, ""))
			return
		}
	}

	if localExit {
		// Bind the same immutable Relay ACL used for remote exits. OpenTCP then
		// intersects it with the server exit's local policy before and after DNS.
		localCtx := agentexit.BindRelayPolicy(ctx, r.relayPolicy)
		targetConn, remoteTarget, openErr := r.localExit.OpenTCP(localCtx, req)
		if openErr != nil {
			code, message := relayErrorDetails(openErr)
			_ = protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
				RequestID: req.RequestID, Success: false, ErrorCode: code, ErrorMessage: message,
			})
			r.emitAudit(baseAudit("EXIT_DIAL_FAILED", code, ""))
			return
		}
		defer targetConn.Close()
		if err := protocol.WriteJSON(clientStream, protocol.OpenTCPResponse{
			RequestID: req.RequestID, Success: true, RemoteIP: remoteTarget,
		}); err != nil {
			return
		}
		_ = clientStream.SetDeadline(time.Time{})

		startTime := time.Now()
		clientSession.ActiveStreams.Add(1)
		activeExitID := exitDeviceID
		clientSession.ActiveExitID.Store(&activeExitID)
		bytesUp, bytesDown := r.localExit.PipeTCP(ctx, clientStream, targetConn)
		clientSession.BytesUp.Add(bytesUp)
		clientSession.BytesDown.Add(bytesDown)
		if clientSession.ActiveStreams.Add(-1) <= 0 {
			clientSession.ActiveExitID.CompareAndSwap(&activeExitID, nil)
		}
		r.emitAudit(&repository.ConnectionAudit{
			UserID: clientSession.OwnerUserID, ClientDeviceID: clientSession.DeviceID,
			ExitDeviceID: exitDeviceID, Protocol: "tcp", TargetHost: req.Host, TargetPort: int(req.Port),
			ResolvedIP: remoteTarget, StartedAt: startTime, EndedAt: time.Now(),
			BytesUp: bytesUp, BytesDown: bytesDown, Result: "SUCCESS",
		})
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
	activeExitID := &exitSession.DeviceID
	clientSession.ActiveExitID.Store(activeExitID)

	bytesUp, bytesDown := r.pipeStreams(ctx, clientStream, exitStream, clientSession, exitSession)

	if clientSession.ActiveStreams.Add(-1) <= 0 {
		clientSession.ActiveExitID.CompareAndSwap(activeExitID, nil)
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
	exitSession, localExit, resolveErr := r.resolveExitSession(clientSession, exitDeviceID)
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
	if localExit {
		exitDeviceID = protocol.ServerExitDeviceID
	} else {
		exitDeviceID = exitSession.DeviceID
	}
	header.ExitDeviceID = exitDeviceID

	if !localExit {
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
	}

	if localExit {
		if req.DatagramRequired {
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
				RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired,
				ErrorMessage: "native UDP datagrams are not available on the server exit",
			})
			r.emitAudit(baseAudit("UDP_NEGOTIATION_FAILED", protocol.ErrCodeDatagramRequired, ""))
			return
		}
		// The local egress socket is attached directly to this client stream.
		// Optional native-datagram requests therefore fall back to the established
		// UDP stream framing instead of creating a second tunnel association.
		req.Mode = protocol.UDPModeStream
		req.AssociationID = 0
		localCtx := agentexit.BindRelayPolicy(ctx, r.relayPolicy)
		targetConn, remoteTarget, openErr := r.localExit.OpenUDP(localCtx, req)
		if openErr != nil {
			code, message := relayErrorDetails(openErr)
			_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
				RequestID: req.RequestID, Success: false, ErrorCode: code, ErrorMessage: message,
			})
			r.emitAudit(baseAudit("EXIT_DIAL_FAILED", code, ""))
			return
		}
		defer targetConn.Close()
		if err := protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{
			RequestID: req.RequestID, Success: true, RemoteIP: remoteTarget, Mode: protocol.UDPModeStream,
		}); err != nil {
			return
		}
		_ = clientStream.SetDeadline(time.Time{})

		startTime := time.Now()
		clientSession.ActiveStreams.Add(1)
		activeExitID := exitDeviceID
		clientSession.ActiveExitID.Store(&activeExitID)
		bytesUp, bytesDown := r.localExit.PipeUDPStream(ctx, clientStream, targetConn)
		clientSession.BytesUp.Add(bytesUp)
		clientSession.BytesDown.Add(bytesDown)
		if clientSession.ActiveStreams.Add(-1) <= 0 {
			clientSession.ActiveExitID.CompareAndSwap(&activeExitID, nil)
		}
		r.emitAudit(&repository.ConnectionAudit{
			UserID: clientSession.OwnerUserID, ClientDeviceID: clientSession.DeviceID,
			ExitDeviceID: exitDeviceID, Protocol: "udp", TargetHost: req.Host, TargetPort: int(req.Port),
			ResolvedIP: remoteTarget, StartedAt: startTime, EndedAt: time.Now(),
			BytesUp: bytesUp, BytesDown: bytesDown, Result: "SUCCESS",
		})
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
	activeExitID := &exitSession.DeviceID
	clientSession.ActiveExitID.Store(activeExitID)

	var bytesUp, bytesDown int64
	if clientDatagrams != nil {
		bytesUp, bytesDown = r.pipeDatagrams(ctx, clientStream, exitStream, clientDatagrams, exitDatagrams, clientSession, exitSession, tunnel.UDPIdleTimeout)
	} else {
		bytesUp, bytesDown = r.pipeStreams(ctx, clientStream, exitStream, clientSession, exitSession)
	}

	if clientSession.ActiveStreams.Add(-1) <= 0 {
		clientSession.ActiveExitID.CompareAndSwap(activeExitID, nil)
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

func relayErrorDetails(err error) (string, string) {
	if err == nil {
		return protocol.ErrCodeInternalError, "unknown exit error"
	}
	var relayErr *protocol.RelayError
	if errors.As(err, &relayErr) {
		return relayErr.Code, relayErr.Message
	}
	return protocol.ErrCodeInternalError, err.Error()
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
	return r.relayPolicy, nil
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
			packet, err := src.ReceivePacket(ctx)
			if err != nil {
				return
			}
			n := packet.PayloadBytes()
			if err := dst.ForwardPacket(ctx, packet); err != nil {
				return
			}
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

func proxyStreamResumeNegotiated(client, exit *session.DeviceSession) bool {
	return client != nil && exit != nil &&
		hasCapability(client, protocol.CapabilityProxyStreamResume) &&
		hasCapability(exit, protocol.CapabilityProxyStreamResume)
}

func normalizeTCPResumeBinding(binding *protocol.TCPResumeBinding, negotiated bool) (*protocol.TCPResumeBinding, error) {
	if binding == nil {
		return nil, nil
	}
	switch binding.Mode {
	case protocol.TCPResumeModeOpen:
		if !negotiated {
			// Mixed-version compatibility: a new Client may optimistically
			// request resume while the selected Exit is older. Strip only the
			// initial-open extension and preserve the legacy TCP connection.
			return nil, nil
		}
	case protocol.TCPResumeModeRebind:
		if !negotiated {
			return nil, errors.New("resumable TCP rebind capability was not negotiated")
		}
	default:
		return nil, errors.New("invalid resumable TCP mode")
	}
	if err := validateTCPResumeBinding(binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func validateTCPResumeBinding(binding *protocol.TCPResumeBinding) error {
	if binding == nil {
		return errors.New("missing resumable TCP binding")
	}
	if binding.Mode != protocol.TCPResumeModeOpen && binding.Mode != protocol.TCPResumeModeRebind {
		return errors.New("invalid resumable TCP mode")
	}
	if len(binding.StreamID) != protocol.TCPResumeStreamIDSize {
		return errors.New("invalid resumable TCP stream id")
	}
	if len(binding.Token) != protocol.TCPResumeTokenSize {
		return errors.New("invalid resumable TCP token")
	}
	if binding.Generation == 0 {
		return errors.New("invalid resumable TCP generation")
	}
	return nil
}

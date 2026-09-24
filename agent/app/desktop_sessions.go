package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"relayproxy/agent/desktop"
	rdpp2p "relayproxy/agent/rdp/p2p"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func newRemoteDesktopSessionID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("desktop_%x", raw[:]), nil
}

func (a *Agent) desktopP2PEligible(sessionID string, controller *desktop.ControllerSession) bool {
	if a == nil || controller == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.desktopConnections[sessionID] == controller && controller.Active()
}

func (a *Agent) desktopSessionByID(sessionID string) *desktop.ControllerSession {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return a.desktopConnection
	}
	return a.desktopConnections[sessionID]
}

func (a *Agent) takeDesktopSessionsLocked() []*desktop.ControllerSession {
	if a == nil {
		return nil
	}
	seen := make(map[*desktop.ControllerSession]struct{})
	out := make([]*desktop.ControllerSession, 0, len(a.desktopConnections)+1)
	add := func(session *desktop.ControllerSession) {
		if session == nil {
			return
		}
		if _, ok := seen[session]; ok {
			return
		}
		seen[session] = struct{}{}
		out = append(out, session)
	}
	add(a.desktopConnection)
	for _, session := range a.desktopConnections {
		add(session)
	}
	a.desktopConnection = nil
	a.desktopPrimarySessionID = ""
	a.desktopConnections = make(map[string]*desktop.ControllerSession)
	return out
}

func (a *Agent) removeDesktopSession(sessionID string, session *desktop.ControllerSession) {
	if a == nil || session == nil || sessionID == "" {
		return
	}
	var directToClose interface{ Close() error }
	a.mu.Lock()
	if a.desktopConnections[sessionID] != session {
		a.mu.Unlock()
		return
	}
	delete(a.desktopConnections, sessionID)
	if direct := a.desktopP2PSessions[sessionID]; direct != nil {
		directToClose = direct
		delete(a.desktopP2PSessions, sessionID)
	}
	if a.desktopPrimarySessionID == sessionID && a.desktopConnection == session {
		a.desktopConnection = nil
		a.desktopPrimarySessionID = ""
		ids := make([]string, 0, len(a.desktopConnections))
		for id, candidate := range a.desktopConnections {
			if candidate != nil && candidate.Active() {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		if len(ids) > 0 {
			a.desktopPrimarySessionID = ids[0]
			a.desktopConnection = a.desktopConnections[ids[0]]
		}
	}
	a.mu.Unlock()
	if directToClose != nil {
		_ = directToClose.Close()
	}
}

func (a *Agent) connectRelayDesktopSession(
	target protocol.RemoteDesktopTarget,
	options protocol.RemoteDesktopConnectOptions,
	primary bool,
) (protocol.RemoteDesktopSessionInfo, error) {
	if a == nil {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("Relay Desktop agent is unavailable")
	}
	if !primary && !target.Capabilities.MultiStream {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("remote Relay Desktop host does not advertise multi-stream support")
	}

	a.mu.RLock()
	desktopHost := a.desktopHost
	a.mu.RUnlock()
	var localCodecCapabilities []protocol.DesktopCodecCapability
	if provider, ok := desktopHost.(interface {
		CodecCapabilities() []protocol.DesktopCodecCapability
	}); ok {
		localCodecCapabilities = provider.CodecCapabilities()
	}
	var err error
	options, err = negotiateRemoteDesktopVideo(target, localCodecCapabilities, options)
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, err
	}
	options, err = negotiateRemoteDesktopAudio(target, options)
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, err
	}

	sessionID, err := newRemoteDesktopSessionID()
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, fmt.Errorf("allocate Relay Desktop session id: %w", err)
	}
	session, err := desktop.StartControllerWithOptions(a.ctx, target.DeviceID, func(ctx context.Context, id string) (*desktopmedia.MediaConn, error) {
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return a.rawDialer.DialDesktopMediaForSessionWithOptions(dialCtx, id, sessionID, options)
	}, options)
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, err
	}

	a.mu.Lock()
	if a.closed.Load() || !a.handshakeOK.Load() || a.readySession == nil {
		a.mu.Unlock()
		_ = session.Close()
		return protocol.RemoteDesktopSessionInfo{}, errors.New("relay session is no longer approved")
	}
	if a.desktopConnections == nil {
		a.desktopConnections = make(map[string]*desktop.ControllerSession)
	}
	if a.desktopP2PSessions == nil {
		a.desktopP2PSessions = make(map[string]*rdpp2p.Session)
	}
	if !primary && a.desktopConnection == nil {
		primary = true
	}
	var old *desktop.ControllerSession
	var oldDirect interface{ Close() error }
	if primary {
		old = a.desktopConnection
		oldPrimaryID := a.desktopPrimarySessionID
		if oldPrimaryID != "" {
			delete(a.desktopConnections, oldPrimaryID)
			if direct := a.desktopP2PSessions[oldPrimaryID]; direct != nil {
				oldDirect = direct
				delete(a.desktopP2PSessions, oldPrimaryID)
			}
		}
		a.desktopConnection = session
		a.desktopPrimarySessionID = sessionID
	}
	a.desktopConnections[sessionID] = session
	p2pManager := a.rdpP2P
	a.mu.Unlock()

	if old != nil && old != session {
		_ = old.Close()
	}
	if oldDirect != nil {
		_ = oldDirect.Close()
	}
	a.startRelayDesktopDirectPath(sessionID, session, target.DeviceID, p2pManager)

	go func() {
		<-session.Done()
		a.removeDesktopSession(sessionID, session)
	}()

	return protocol.RemoteDesktopSessionInfo{
		SessionID:  sessionID,
		Target:     target,
		Backend:    protocol.DesktopBackendRelay,
		State:      "connected",
		PathTCP:    "relay-control",
		PathUDP:    "quic-datagram",
		UDPEnabled: true,
	}, nil
}

// ConnectRemoteDesktopSession opens an additional independent Relay Desktop
// media association. It never replaces the existing primary Relay Desktop
// controller session; if no primary exists the first new session is promoted.
func (a *Agent) ConnectRemoteDesktopSession(targetID string, options protocol.RemoteDesktopConnectOptions) (protocol.RemoteDesktopSessionInfo, error) {
	targetID = strings.TrimSpace(targetID)
	target, ok := a.remoteDesktopTarget(targetID)
	if !ok {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("remote desktop target is not approved")
	}
	if !target.Online {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("remote desktop target is offline")
	}
	backend, err := desktop.SelectBackend(target, options)
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, err
	}
	if backend != protocol.DesktopBackendRelay {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("independent multi-view sessions require Relay Desktop")
	}
	a.DisconnectRDP()
	return a.connectRelayDesktopSession(target, options, false)
}

func (a *Agent) remoteDesktopStatusForSession(sessionID string, session *desktop.ControllerSession) protocol.RemoteDesktopStatus {
	out := protocol.RemoteDesktopStatus{SessionID: sessionID, State: "idle"}
	if session == nil || !session.Active() {
		return out
	}
	targetID := session.TargetID()
	pathUDP := session.DatagramPathName()
	if pathUDP == "" || pathUDP == "relay" {
		pathUDP = "quic-datagram"
	}
	out.State = "connected"
	out.Backend = protocol.DesktopBackendRelay
	out.TargetID = targetID
	out.PathTCP = "relay-control"
	out.PathUDP = pathUDP
	out.UDPEnabled = true
	out.UDPActive = true
	config := session.VideoConfigSnapshot()
	out.CaptureBackend = session.CaptureBackendPreference()
	out.DisplayID = config.DisplayID
	out.Generation = config.Generation
	out.Codec = config.Codec
	out.Width = config.Width
	out.Height = config.Height
	out.MaxWidth = config.MaxWidth
	out.MaxHeight = config.MaxHeight
	out.FPS = config.FPS
	liveDisplays, live := session.DisplayCapabilitiesSnapshot()
	out.DisplaysReady = live
	if live {
		out.Displays = liveDisplays
	}
	if target, ok := a.remoteDesktopTarget(targetID); ok {
		out.TargetName = target.Name
		if !live {
			out.Displays = append([]protocol.DesktopDisplayCapability(nil), target.Capabilities.Displays...)
		}
	}
	for _, display := range out.Displays {
		if display.ID == out.DisplayID {
			out.DisplayName = display.Name
			break
		}
	}
	return out
}

func (a *Agent) RemoteDesktopConnectOptionsForSession(sessionID string) protocol.RemoteDesktopConnectOptions {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return protocol.RemoteDesktopConnectOptions{}
	}
	return session.ConnectOptionsSnapshot()
}

func (a *Agent) RemoteDesktopStatusForSession(sessionID string) protocol.RemoteDesktopStatus {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		a.mu.RLock()
		sessionID = a.desktopPrimarySessionID
		session := a.desktopConnection
		a.mu.RUnlock()
		return a.remoteDesktopStatusForSession(sessionID, session)
	}
	return a.remoteDesktopStatusForSession(sessionID, a.desktopSessionByID(sessionID))
}

func (a *Agent) RemoteDesktopStatuses() []protocol.RemoteDesktopStatus {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	ids := make([]string, 0, len(a.desktopConnections))
	sessions := make(map[string]*desktop.ControllerSession, len(a.desktopConnections))
	for id, session := range a.desktopConnections {
		ids = append(ids, id)
		sessions[id] = session
	}
	a.mu.RUnlock()
	slices.Sort(ids)
	out := make([]protocol.RemoteDesktopStatus, 0, len(ids))
	for _, id := range ids {
		if status := a.remoteDesktopStatusForSession(id, sessions[id]); status.State == "connected" {
			out = append(out, status)
		}
	}
	return out
}

func (a *Agent) DisconnectRemoteDesktopSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	session := a.desktopSessionByID(sessionID)
	if session == nil {
		return
	}
	_ = session.Close()
	a.removeDesktopSession(sessionID, session)
}

func (a *Agent) RemoteDesktopFrameForSession(sessionID string) protocol.RemoteDesktopFrame {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return protocol.RemoteDesktopFrame{}
	}
	frame, ok := session.LatestFrame()
	if !ok {
		return protocol.RemoteDesktopFrame{}
	}
	return protocol.RemoteDesktopFrame{
		Sequence: frame.Sequence, Generation: frame.Generation, MimeType: frame.MimeType, Codec: frame.Codec,
		Width: frame.Width, Height: frame.Height, Timestamp: frame.Timestamp,
		KeyFrame: frame.KeyFrame, Data: frame.Data,
	}
}

func (a *Agent) RemoteDesktopCursorForSession(sessionID, knownCursorID string) protocol.DesktopCursorState {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return protocol.DesktopCursorState{}
	}
	cursor, ok := session.LatestCursor(knownCursorID)
	if !ok {
		return protocol.DesktopCursorState{}
	}
	return cursor
}

func (a *Agent) SendRemoteDesktopInputForSession(sessionID string, event protocol.DesktopInputEvent) error {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.SendInput(ctx, event)
}

func (a *Agent) SetRemoteDesktopViewportResolutionForSession(sessionID string, width, height int) error {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.RequestViewportResolution(ctx, width, height)
}

func (a *Agent) RemoteDesktopViewportFollowEnabledForSession(sessionID string) bool {
	session := a.desktopSessionByID(sessionID)
	return session != nil && session.Active() && session.ViewportFollowEnabled()
}

func (a *Agent) RequestRemoteDesktopIDRForSession(sessionID string) error {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.RequestIDR(ctx)
}

func (a *Agent) RemoteDesktopStatsForSession(sessionID string) protocol.DesktopSessionStats {
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return protocol.DesktopSessionStats{}
	}
	return session.Stats()
}

func (a *Agent) ReportRemoteDesktopViewerStatsForSession(sessionID string, stats protocol.DesktopSessionStats) {
	session := a.desktopSessionByID(sessionID)
	if session != nil && session.Active() {
		session.UpdateViewerStats(stats)
	}
}

func (a *Agent) RemoteDesktopAudioEnabledForSession(sessionID string) bool {
	session := a.desktopSessionByID(sessionID)
	return session != nil && session.Active() && session.AudioEnabled()
}

func (a *Agent) NextRemoteDesktopAudioFrameForSession(
	ctx context.Context,
	sessionID string,
) (desktop.AudioFrameSnapshot, protocol.DesktopAudioConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session := a.desktopSessionByID(sessionID)
	if session == nil || !session.Active() {
		return desktop.AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is not connected")
	}
	return session.NextAudioFrame(ctx)
}

package app

import (
	"context"
	"log"
	"strings"

	"relayproxy/agent/client"
	proxydirect "relayproxy/agent/direct"
	"relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type publicDirectClientManager = proxydirect.ClientManager

func (a *Agent) initPublicDirectClient() {
	if a == nil || a.proxyDirect != nil {
		return
	}
	a.proxyDirect = proxydirect.NewClientManager(a.ctx, func() string {
		a.mu.RLock()
		defer a.mu.RUnlock()
		if a.closed.Load() {
			return ""
		}
		return a.cfg.DeviceID
	}, proxydirect.ClientManagerOptions{})
}

func (a *Agent) configureProxyPathProvider() {
	if a == nil || a.rawDialer == nil {
		return
	}
	a.rawDialer.ConfigurePathProvider(
		func(exitDeviceID string) (client.SelectedSession, bool) {
			a.mu.RLock()
			publicDirect := a.proxyDirect
			p2p := a.proxyP2P
			closed := a.closed.Load()
			mode := a.cfg.P2PMode
			a.mu.RUnlock()
			if closed {
				return client.SelectedSession{}, false
			}
			if mode != "p2p_only" && publicDirect != nil {
				if session, ok := publicDirect.ReadyForExit(exitDeviceID); ok && session != nil {
					return client.SelectedSession{Session: session, Path: protocol.ProxyPathPublicDirectQUIC}, true
				}
			}
			if p2p != nil {
				if session, ok := p2p.ReadyForExit(exitDeviceID); ok && session != nil {
					return client.SelectedSession{Session: session, Path: protocol.ProxyPathP2PQUIC}, true
				}
			}
			return client.SelectedSession{}, false
		},
		func(exitDeviceID string) {
			a.ensureProxyDirectPath(exitDeviceID)
		},
	)
	a.rawDialer.ConfigurePathMetrics(func(exitDeviceID string, path protocol.ProxyPath) {
		a.mu.RLock()
		publicDirect := a.proxyDirect
		p2p := a.proxyP2P
		a.mu.RUnlock()
		switch path {
		case protocol.ProxyPathPublicDirectQUIC:
			if publicDirect != nil {
				publicDirect.NoteFallback(exitDeviceID)
			}
		case protocol.ProxyPathP2PQUIC:
			if p2p != nil {
				p2p.NoteFallback(exitDeviceID)
			}
		}
	})
	a.rawDialer.ConfigurePathFailure(func(exitDeviceID string, path protocol.ProxyPath, reason string) {
		a.mu.RLock()
		publicDirect := a.proxyDirect
		p2p := a.proxyP2P
		a.mu.RUnlock()
		switch path {
		case protocol.ProxyPathPublicDirectQUIC:
			if publicDirect != nil {
				publicDirect.FailReadyForExit(exitDeviceID, reason)
			}
		case protocol.ProxyPathP2PQUIC:
			if p2p != nil {
				p2p.FailReadyForExit(exitDeviceID, reason)
			}
		}
	})
}

func (a *Agent) ensureProxyDirectPath(exitDeviceID string) {
	if a == nil {
		return
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if exitDeviceID == "" || exitDeviceID == protocol.ServerExitDeviceID {
		return
	}
	a.mu.RLock()
	publicDirect := a.proxyDirect
	p2p := a.proxyP2P
	ready := a.handshakeOK.Load()
	mode := a.cfg.P2PMode
	a.mu.RUnlock()
	if !ready || mode == "relay_only" {
		return
	}
	if mode != "p2p_only" && publicDirect != nil {
		if publicDirect.PreferForExit(exitDeviceID) {
			publicDirect.EnsureClient(exitDeviceID)
			return
		}
		if publicDirect.EnsureClient(exitDeviceID) {
			return
		}
	}
	if p2p != nil {
		p2p.EnsureClient(exitDeviceID)
	}
}

func (a *Agent) updatePublicDirectInventory(exits []protocol.ProxyExit) {
	if a == nil {
		return
	}
	a.mu.RLock()
	manager := a.proxyDirect
	a.mu.RUnlock()
	if manager != nil {
		manager.UpdateInventory(exits)
	}
}

func (a *Agent) closePublicDirectClient() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	manager := a.proxyDirect
	a.proxyDirect = nil
	a.mu.Unlock()
	if manager != nil {
		return manager.Close()
	}
	return nil
}

func (a *Agent) startPublicDirectExit(
	ctx context.Context,
	relay tunnel.TunnelSession,
	accepted protocol.DeviceAccepted,
	handler *exit.Handler,
	maxStreams int,
) (func(), error) {
	runtime, err := proxydirect.StartExitRuntime(ctx, relay, accepted, handler, proxydirect.ExitRuntimeOptions{
		MaxStreams:      maxStreams,
		PortStart:       accepted.PublicDirectPortStart,
		PortEnd:         accepted.PublicDirectPortEnd,
		ManualAdvertise: a.cfg.PublicDirectAdvertise,
		ValidateTicket: func(validateCtx context.Context, claims protocol.PublicDirectTicketClaims) error {
			return proxydirect.ValidateTicketCurrent(validateCtx, relay, claims)
		},
	})
	if err != nil {
		return nil, err
	}
	log.Printf("[PublicDirect] exit listener registered on UDP %d with %d local candidate(s)",
		runtime.ListenPort(), len(runtime.Candidates()))
	return func() { _ = runtime.Close() }, nil
}

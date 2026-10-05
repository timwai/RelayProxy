package androidcore

import (
	"context"
	"log"
	"strings"

	agentclient "relayproxy/agent/client"
	proxydirect "relayproxy/agent/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type publicDirectClientManager = proxydirect.ClientManager

func (c *Client) initPublicDirectClient() {
	if c == nil || c.proxyDirect != nil {
		return
	}
	manager := proxydirect.NewClientManager(c.ctx, func() string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		if c.closed {
			return ""
		}
		return c.status.DeviceID
	}, proxydirect.ClientManagerOptions{})
	manager.SetFallback(func(exitDeviceID, _ string) {
		c.mu.RLock()
		p2p := c.proxyP2P
		p2pEnabled := c.cfg.ProxyP2PEnabled != nil && *c.cfg.ProxyP2PEnabled
		mode := c.cfg.ProxyPathMode
		closed := c.closed
		c.mu.RUnlock()
		if !closed && c.clientApproved.Load() && p2pEnabled && mode != "relay_only" && p2p != nil {
			p2p.EnsureClient(exitDeviceID)
		}
	})
	c.proxyDirect = manager
}

func (c *Client) configureProxyPathProvider() {
	if c == nil || c.proxyDialer == nil {
		return
	}
	c.proxyDialer.ConfigurePathProvider(
		func(exitDeviceID string) (agentclient.SelectedSession, bool) {
			c.mu.RLock()
			publicDirect := c.proxyDirect
			p2p := c.proxyP2P
			mode := c.cfg.ProxyPathMode
			closed := c.closed
			c.mu.RUnlock()
			if closed || !c.clientApproved.Load() || mode == "relay_only" {
				return agentclient.SelectedSession{}, false
			}
			if mode != "p2p_only" && publicDirect != nil {
				if session, ok := publicDirect.ReadyForExit(exitDeviceID); ok && session != nil {
					return agentclient.SelectedSession{Session: session, Path: protocol.ProxyPathPublicDirectQUIC}, true
				}
			}
			if p2p != nil {
				if session, ok := p2p.ReadyForExit(exitDeviceID); ok && session != nil {
					return agentclient.SelectedSession{Session: session, Path: protocol.ProxyPathP2PQUIC}, true
				}
			}
			return agentclient.SelectedSession{}, false
		},
		func(exitDeviceID string) {
			c.ensureProxyDirectPath(exitDeviceID)
		},
	)
	c.proxyDialer.ConfigurePathMetrics(func(exitDeviceID string, path protocol.ProxyPath) {
		c.mu.RLock()
		publicDirect := c.proxyDirect
		p2p := c.proxyP2P
		c.mu.RUnlock()
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
	c.proxyDialer.ConfigurePathFailure(func(exitDeviceID string, path protocol.ProxyPath, reason string) {
		c.mu.RLock()
		publicDirect := c.proxyDirect
		p2p := c.proxyP2P
		c.mu.RUnlock()
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

func (c *Client) ensureProxyDirectPath(exitDeviceID string) {
	if c == nil {
		return
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if exitDeviceID == "" || exitDeviceID == protocol.ServerExitDeviceID || !c.clientApproved.Load() {
		return
	}
	c.mu.RLock()
	publicDirect := c.proxyDirect
	p2p := c.proxyP2P
	p2pEnabled := c.cfg.ProxyP2PEnabled != nil && *c.cfg.ProxyP2PEnabled
	mode := c.cfg.ProxyPathMode
	closed := c.closed
	c.mu.RUnlock()
	if closed || mode == "relay_only" {
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
	if p2pEnabled && p2p != nil {
		p2p.EnsureClient(exitDeviceID)
	}
}

func (c *Client) updatePublicDirectInventory(exits []protocol.ProxyExit) {
	if c == nil {
		return
	}
	c.mu.RLock()
	manager := c.proxyDirect
	c.mu.RUnlock()
	if manager != nil {
		manager.UpdateInventory(exits)
	}
}

func (c *Client) closePublicDirectClient() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	manager := c.proxyDirect
	c.proxyDirect = nil
	c.mu.Unlock()
	if manager != nil {
		return manager.Close()
	}
	return nil
}

func (c *Client) startPublicDirectExit(
	ctx context.Context,
	relay tunnel.TunnelSession,
	accepted protocol.DeviceAccepted,
	enabled bool,
	maxStreams int,
) func() {
	if c == nil || !enabled {
		return func() {}
	}
	runtime, err := proxydirect.StartExitRuntime(ctx, relay, accepted, c.handler, proxydirect.ExitRuntimeOptions{
		MaxStreams: maxStreams,
		PortStart:  accepted.PublicDirectPortStart,
		PortEnd:    accepted.PublicDirectPortEnd,
		ValidateTicket: func(validateCtx context.Context, claims protocol.PublicDirectTicketClaims) error {
			return proxydirect.ValidateTicketCurrent(validateCtx, relay, claims)
		},
	})
	if err != nil {
		log.Printf("[PublicDirect] Android exit listener unavailable; P2P/Relay fallback remains active: %v", err)
		return func() {}
	}
	log.Printf("[PublicDirect] Android exit listener registered on UDP %d with %d local candidate(s)",
		runtime.ListenPort(), len(runtime.Candidates()))
	return func() { _ = runtime.Close() }
}

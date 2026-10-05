package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/client"
	proxydirect "relayproxy/agent/direct"
	"relayproxy/agent/exit"
	directcore "relayproxy/internal/direct"
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
			a.mu.RUnlock()
			if closed {
				return client.SelectedSession{}, false
			}
			if publicDirect != nil {
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
	if publicDirect != nil {
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
	if handler == nil || relay == nil ||
		!slices.Contains(accepted.TransportCapabilities, protocol.CapabilityProxyPublicDirect) {
		return func() {}, nil
	}
	authenticator, err := proxydirect.NewTicketAuthenticator(
		accepted.PublicDirectTicketIssuer,
		accepted.PublicDirectTicketKey,
		accepted.DeviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize ticket authenticator: %w", err)
	}
	identity, err := directcore.GenerateTLSIdentity()
	if err != nil {
		return nil, fmt.Errorf("generate TLS identity: %w", err)
	}
	listener, err := proxydirect.Listen(proxydirect.ListenerConfig{
		ListenAddress: ":0",
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{identity.Certificate},
		},
		Authenticator: authenticator,
	})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	closeListener := func() { _ = listener.Close() }

	_, portText, err := net.SplitHostPort(listener.Addr())
	if err != nil {
		closeListener()
		return nil, fmt.Errorf("parse listener address %q: %w", listener.Addr(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		closeListener()
		return nil, fmt.Errorf("invalid listener port %q", portText)
	}
	candidates, err := proxydirect.DiscoverEndpointCandidates(uint16(port), "")
	if err != nil {
		closeListener()
		return nil, fmt.Errorf("discover endpoints: %w", err)
	}
	registerCtx, cancelRegister := context.WithTimeout(ctx, 5*time.Second)
	response, err := proxydirect.RegisterEndpoint(registerCtx, relay, protocol.PublicDirectRegistrationRequest{
		ListenerPort:    uint16(port),
		CertFingerprint: identity.Fingerprint,
		Candidates:      candidates,
	})
	cancelRegister()
	if err != nil {
		closeListener()
		return nil, fmt.Errorf("register endpoint: %w", err)
	}
	if !response.Success {
		closeListener()
		return nil, fmt.Errorf("register endpoint rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := proxydirect.ServeExit(ctx, listener, handler, 0, maxStreams); err != nil &&
			ctx.Err() == nil {
			log.Printf("[PublicDirect] exit listener stopped: %v", err)
		}
	}()
	log.Printf("[PublicDirect] exit listener registered on UDP %d with %d local candidate(s)", port, len(candidates))

	var once sync.Once
	return func() {
		once.Do(func() {
			closeListener()
			<-done
		})
	}, nil
}

package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"relayproxy/agent/divert"
	"relayproxy/agent/gateway"
	"relayproxy/agent/routing"
	"relayproxy/internal/exitgroup"
)

func (a *Agent) buildGateway(cfg gateway.Config) (*gateway.Server, error) {
	dns, err := divert.New(divert.Options{Dialer: routing.SelectedExitDialer{Routing: a.dialer}, DNSUpstreams: func() []divert.DNSUpstream {
		c := a.routingEngine.Config()
		result := make([]divert.DNSUpstream, len(c.DNSUpstreams))
		for i, u := range c.DNSUpstreams {
			result[i] = divert.DNSUpstream{URL: u.URL, BootstrapIP: u.BootstrapIP}
		}
		return result
	}})
	if err != nil {
		return nil, err
	}
	return gateway.New(gateway.Options{Config: cfg, Dialer: a.dialer, DefaultExitID: func() string {
		if p := a.selectedExit.Load(); p != nil {
			return *p
		}
		return ""
	}, DNS: func(ctx context.Context, id string, q []byte, udp bool) []byte {
		if pinned := a.routingEngine.Config().DNSExitID; pinned != "" {
			id = pinned
		}
		return dns.ExchangeGatewayDNS(ctx, id, q, udp)
	}})
}
func (a *Agent) startGateway(cfg gateway.Config) error {
	if !cfg.Enabled {
		return nil
	}
	srv, err := a.buildGateway(cfg)
	if err != nil {
		return err
	}
	if err = srv.Start(); err != nil {
		return fmt.Errorf("启动 LAN 网关: %w", err)
	}
	a.mu.Lock()
	a.gatewaySrv = srv
	a.mu.Unlock()
	return nil
}
func (a *Agent) GatewayStatus() gateway.Status {
	a.mu.RLock()
	srv := a.gatewaySrv
	a.mu.RUnlock()
	if srv != nil {
		return srv.Status()
	}
	return gateway.Status{Supported: gateway.Supported()}
}

// ApplyGatewayConfig serializes socket and kernel ownership changes with shutdown.
func (a *Agent) ApplyGatewayConfig(cfg gateway.Config, explicit ...bool) error {
	forceDisable := len(explicit) > 0 && explicit[0] && !cfg.Enabled
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	old := a.Config()
	if reflect.DeepEqual(old.Gateway, cfg) && !forceDisable {
		return nil
	}
	if err := routing.ValidateCustomReferences(old.CustomExits, "", "", nil, cfg.ExitID); err != nil {
		return err
	}
	if a.closed.Load() {
		return errors.New("agent closed")
	}
	if err := gateway.Validate(cfg); err != nil {
		return err
	}
	if err := exitgroup.Validate(old.Routing.DeviceGroups, cfg.ExitID); err != nil {
		return err
	}
	a.mu.RLock()
	srv, started := a.gatewaySrv, a.started
	a.mu.RUnlock()
	if !started {
		a.mu.Lock()
		a.cfg.Gateway = gateway.Clone(cfg)
		a.mu.Unlock()
		return nil
	}
	if srv != nil {
		_ = srv.Stop()
	}
	a.mu.Lock()
	a.gatewaySrv = nil
	a.mu.Unlock()
	var err error
	if cfg.Enabled {
		err = a.startGateway(cfg)
	} else if old.Gateway.Enabled || srv != nil || forceDisable {
		err = gateway.Disable()
	}
	if err != nil {
		restoreErr := a.startGateway(old.Gateway)
		return errors.Join(err, restoreErr)
	}
	a.mu.Lock()
	a.cfg.Gateway = gateway.Clone(cfg)
	ctrl := a.ctrlStream
	a.mu.Unlock()
	if cfg.Enabled && !old.Gateway.Enabled && old.Mode == "EXIT" && ctrl != nil {
		_ = ctrl.Close()
	}
	return nil
}

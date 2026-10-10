package app

import (
	"context"
	"errors"

	"relayproxy/agent/divert"
)

// ProbeDNS uses the active transparent-proxy dialer and DNS policy. Even
// when DNS interception is disabled, diagnostics use the pinned DNS exit
// rather than performing unprotected system DNS lookups.
func (a *Agent) ProbeDNS(ctx context.Context) (divert.DNSProbeReport, error) {
	if a == nil {
		return divert.DNSProbeReport{}, errors.New("agent unavailable")
	}
	a.mu.RLock()
	srv := a.divertSrv
	closed := a.closed.Load()
	a.mu.RUnlock()
	if closed || srv == nil {
		return divert.DNSProbeReport{}, errors.New("transparent DNS Agent is not running")
	}
	return srv.ProbeDNS(ctx)
}

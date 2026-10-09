package routing

import (
	"context"
	"fmt"
	"net"

	"relayproxy/agent/exit"
	"relayproxy/internal/proxy"
)

// SelectedExitDialer dispatches an already classified transparent-proxy flow.
// It must NOT re-evaluate routing rules: divert already matched the process
// identity and selected the exit before calling into this adapter.
type SelectedExitDialer struct {
	Routing *RoutingDialer
}

func (d SelectedExitDialer) DialTCP(ctx context.Context, id, host string, port uint16) (net.Conn, error) {
	if d.Routing == nil {
		return nil, fmt.Errorf("missing local exit dispatcher")
	}
	if IsCustomExitID(id) {
		upstream, err := d.Routing.lookupCustom(id)
		if err != nil {
			return nil, err
		}
		return exit.DialViaUpstreamTCP(ctx, upstream, host, port)
	}
	return d.Routing.tunnel.DialTCP(ctx, id, host, port)
}

func (d SelectedExitDialer) DialUDP(ctx context.Context, id, host string, port uint16) (net.PacketConn, error) {
	return d.DialUDPWithOptions(ctx, id, host, port, proxy.UDPDialOptions{})
}

func (d SelectedExitDialer) DialUDPWithOptions(ctx context.Context, id, host string, port uint16, options proxy.UDPDialOptions) (net.PacketConn, error) {
	if d.Routing == nil {
		return nil, fmt.Errorf("missing local exit dispatcher")
	}
	if IsCustomExitID(id) {
		if options.DatagramRequired {
			return nil, fmt.Errorf("native Relay datagrams are not supported by custom exits")
		}
		upstream, err := d.Routing.lookupCustom(id)
		if err != nil {
			return nil, err
		}
		return exit.DialViaUpstreamUDP(ctx, upstream, host, port)
	}
	if td, ok := d.Routing.tunnel.(proxy.UDPOptionsDialer); ok {
		return td.DialUDPWithOptions(ctx, id, host, port, options)
	}
	if options.DatagramRequired {
		return nil, fmt.Errorf("tunnel does not support required native UDP datagrams")
	}
	return d.Routing.tunnel.DialUDP(ctx, id, host, port)
}

// CustomExitReady avoids tying local interception to Relay authentication.
func (d *RoutingDialer) CustomExitReady(id string) bool {
	if !IsCustomExitID(id) {
		return false
	}
	d.customMu.RLock()
	defer d.customMu.RUnlock()
	item, ok := d.customExits[id]
	return ok && item.Enabled
}

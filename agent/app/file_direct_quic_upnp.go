package app

import (
	"context"
	"net"
	"net/netip"
	"time"

	"relayproxy/internal/p2p/upnp"
)

// runQUICUPnP maps only the QUIC listener's existing UDP port, independently
// of the proxy P2P mapping and the file TLS/TCP mapping. The short finite
// lease is refreshed and ownership-checked by upnp.Mapping.Close().
func (r *fileDirectRuntime) runQUICUPnP(ctx context.Context) {
	if r.quicListener == nil {
		r.mu.Lock()
		if !r.closed && ctx.Err() == nil {
			r.udpUPnPState = "unavailable"
			r.udpUPnPReason = "file QUIC listener is unavailable"
		}
		r.mu.Unlock()
		return
	}
	addr, ok := r.quicListener.Addr().(*net.UDPAddr)
	if !ok || addr.Port <= 0 {
		return
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	mapping, address, err := upnp.MapUDP(discoveryCtx, addr.Port)
	cancel()
	if err != nil {
		r.mu.Lock()
		if !r.closed && ctx.Err() == nil {
			r.udpUPnPState = "unavailable"
			r.udpUPnPReason = err.Error()
		}
		r.mu.Unlock()
		return
	}
	defer mapping.Close()
	if !upnp.IsPublicWANIPv4(address.Addr()) {
		r.mu.Lock()
		if !r.closed && ctx.Err() == nil {
			r.udpUPnPState = "private_wan"
			r.udpUPnPReason = "router WAN IPv4 is not publicly routable"
		}
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	if !r.closed && ctx.Err() == nil {
		r.udpUPnPAddress = address
		r.udpUPnPState = "mapped"
		r.udpUPnPReason = ""
	}
	r.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-mapping.Updates():
			r.mu.Lock()
			if !r.closed && ctx.Err() == nil && update.Healthy &&
				upnp.IsPublicWANIPv4(update.Address.Addr()) {
				r.udpUPnPAddress = update.Address
				r.udpUPnPState = "mapped"
				r.udpUPnPReason = ""
			} else {
				r.udpUPnPAddress = netip.AddrPort{}
				if ctx.Err() == nil && !r.closed {
					r.udpUPnPState = "unavailable"
					r.udpUPnPReason = update.Reason
				}
			}
			r.mu.Unlock()
		}
	}
}

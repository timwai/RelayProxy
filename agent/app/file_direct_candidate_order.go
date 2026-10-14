package app

import "net/netip"

// prioritizeFileDirectEndpoints moves actual same-subnet addresses ahead of
// UPnP/public candidates. Do not simply prefer all RFC1918 addresses:
// private addresses from unrelated networks can waste the 20-second ticket.
// Every candidate is still independently authenticated by pinned TLS + ticket.
func prioritizeFileDirectEndpoints(endpoints []string, subnets []netip.Prefix) []string {
	local := make([]string, 0, len(endpoints))
	other := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		addr, err := netip.ParseAddrPort(endpoint)
		if err == nil {
			ip := addr.Addr().Unmap()
			match := false
			for _, subnet := range subnets {
				if subnet.IsValid() && subnet.Contains(ip) {
					match = true
					break
				}
			}
			if match {
				local = append(local, endpoint)
				continue
			}
		}
		other = append(other, endpoint)
	}
	return append(local, other...)
}

//go:build windows

package divert

import "net/netip"

// LookupLocalProcess also supports local SOCKS5/HTTP clients. Remote clients
// have no matching owner-table entry and are left unidentified.
func LookupLocalProcess(protocol string, source, destination netip.AddrPort) (uint32, string, error) {
	identity, err := lookupPacketProcess(Protocol(protocol), source, destination)
	return identity.PID, identity.Path, err
}

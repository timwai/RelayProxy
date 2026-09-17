//go:build !windows && !linux

package divert

import (
	"errors"
	"net/netip"
)

func LookupLocalProcess(string, netip.AddrPort, netip.AddrPort) (uint32, string, error) {
	return 0, "", errors.New("local process lookup is unavailable on this platform")
}

//go:build windows

package divert

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// DnsFlushResolverCache only clears the Windows DNS Client's cache. It does
// not flush Chromium's host cache or capture an application's private DoH.
// Invoke it after WinDivert capture starts so the next system DNS answer can
// populate RelayProxy's IP-to-domain association cache.
func refreshWindowsSystemDNSCache() error {
	proc := windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("DNS cache refresh API unavailable: %w", err)
	}
	ok, _, err := proc.Call()
	if ok != 0 {
		return nil
	}
	return fmt.Errorf("Windows DNS cache refresh failed: %w", err)
}

func shouldRefreshWindowsSystemDNSCache(associationEnabled, fakeIPEnabled, proxyDNSEnabled bool) bool {
	// Active DNS interception already handles new A/AAAA replies, and must
	// not cause unnecessary changes to the host resolver during startup.
	return associationEnabled && !fakeIPEnabled && !proxyDNSEnabled
}

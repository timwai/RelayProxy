//go:build darwin

package divert

func platformIndependentDNSGuardStatus() (string, string) {
	// A Network Extension IPC marker is not an independent packet firewall:
	// macOS may bypass the transparent provider for system and loopback DNS.
	return "unverified", "Network Extension capture and its marker are not proof of a persistent OS DNS firewall"
}

//go:build windows

package divert

func platformIndependentDNSGuardStatus() (string, string) {
	// In-process WinDivert and WFP filter state alone cannot attest a
	// persistent DNS kill switch. Check the PowerShell firewall script as an
	// administrator on the target machine.
	return "unverified", "Use scripts/dns-killswitch-windows.ps1 Status to inspect persistent WFP rules"
}

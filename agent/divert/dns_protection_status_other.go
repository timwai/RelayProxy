//go:build !linux && !windows && !darwin

package divert

func platformIndependentDNSGuardStatus() (string, string) {
	return "unsupported", "independent DNS firewall verification is not implemented for this platform"
}

package divert

// DNSProtectionStatus distinguishes configuration intent from the kernel's
// observable state. "present" is NOT proof of a persistent, system-wide kill
// switch or a zero-leak guarantee.
type DNSProtectionStatus struct {
	FakeIPEnabled    bool   `json:"fakeIpEnabled"`
	Capture          string `json:"capture"`
	IndependentGuard string `json:"independentGuard"`
	Detail           string `json:"detail,omitempty"`
}

// DNSProtectionStatus is safe to call from the Agent's live diagnostics page.
// Platform checks are cached/limited; it never installs firewall rules.
func (s *Server) DNSProtectionStatus() DNSProtectionStatus {
	state := DNSProtectionStatus{Capture: "unavailable"}
	if s == nil {
		state.IndependentGuard, state.Detail = platformIndependentDNSGuardStatus()
		return state
	}
	state.FakeIPEnabled = s.fakeIPEnabled()
	s.mu.Lock()
	interceptor := s.interceptor
	s.mu.Unlock()
	if interceptor != nil && interceptor.Running() {
		state.Capture = "running-unverified"
		if reporter, ok := interceptor.(interface{ dnsCaptureMode() string }); ok {
			state.Capture = reporter.dnsCaptureMode()
		}
	}
	state.IndependentGuard, state.Detail = platformIndependentDNSGuardStatus()
	return state
}

package divert

import (
	"errors"
	"testing"
)

type stubDNSCaptureInterceptor struct {
	enabled bool
	updates int
	fail    bool
}

func (s *stubDNSCaptureInterceptor) Running() bool     { return true }
func (s *stubDNSCaptureInterceptor) ListenAddr() string { return "" }
func (s *stubDNSCaptureInterceptor) Close()             {}
func (s *stubDNSCaptureInterceptor) SyncPlatformDNSCapture(enabled bool) error {
	s.updates++
	if s.fail {
		return errors.New("kernel DNS queue update failed")
	}
	s.enabled = enabled
	return nil
}

func TestServerSynchronousDNSCaptureTransition(t *testing.T) {
	s := newTestServer(t, Options{Config: Config{DefaultAction: ActionProxy}})
	interceptor := &stubDNSCaptureInterceptor{}
	s.mu.Lock()
	s.interceptor = interceptor
	s.mu.Unlock()
	if err := s.SyncPlatformDNSCapture(true); err != nil {
		t.Fatal(err)
	}
	if !interceptor.enabled || interceptor.updates != 1 {
		t.Fatalf("FakeIP was not enforced by kernel capture first: %+v", interceptor)
	}
	interceptor.fail = true
	if err := s.SyncPlatformDNSCapture(false); err == nil {
		t.Fatal("kernel update failure was incorrectly reported as success")
	}
	if !interceptor.enabled {
		t.Fatal("failed kernel downgrade unexpectedly changed confirmed state")
	}
}

package divert

import (
	"net/netip"
	"testing"
	"time"
)

type timedTestPacketDevice struct{ *testPacketDevice }

func (*timedTestPacketDevice) packetHandlingTimeout() time.Duration { return 50 * time.Millisecond }

func TestInterceptorTimeoutReleasesDeviceBeforeBlockedLookupReturns(t *testing.T) {
	s := newTestServer(t, Options{Config: Config{DefaultAction: ActionDirect}})
	device := &timedTestPacketDevice{newTestPacketDevice()}
	entered, release, cleaned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	i := newPacketInterceptor(s, device, nil, func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		close(entered)
		<-release
		return packetProcess{}, nil
	})
	i.onStop = func() { close(cleaned) }
	i.start()
	defer func() { close(release); i.Close() }()
	device.incoming <- capturedTestPacket{data: interceptedSYN(false), meta: packetMetadata{outbound: true}}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("packet did not enter blocking process lookup")
	}
	select {
	case <-device.done:
	case <-time.After(time.Second):
		t.Fatal("blocked lookup retained machine-wide capture")
	}
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("DNS cleanup waited for the blocked capture worker")
	}
	if i.Running() || i.Diagnostics().LastError != errNetworkCaptureTimeout.Error() {
		t.Fatalf("watchdog failure missing from status: %+v", i.Diagnostics())
	}
}

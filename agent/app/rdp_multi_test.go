package app

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/agent/rdp"
)

func newTestRDPConnection(t *testing.T, id string) *rdp.Connection {
	t.Helper()
	conn, err := rdp.StartController(context.Background(), rdp.Target{DeviceID: id, Name: id, Online: true},
		rdp.ControllerOptions{DialTCP: func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("test only: no remote RDP dial")
		}, SupportsUDP: func() bool { return false }}, false)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func assertRDPListener(t *testing.T, address string, shouldListen bool) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
	if conn != nil { _ = conn.Close() }
	if (err == nil) != shouldListen {
		t.Fatalf("RDP listener %s accepting=%v; want %v (err=%v)", address, err == nil, shouldListen, err)
	}
}

// A second device must not close the first listener, and an individual
// disconnect must neither change nor close other devices' stable ports.
func TestRDPConcurrentSessionsDisconnectOnlySelectedTarget(t *testing.T) {
	a := &Agent{rdpConnections: map[string]*rdp.Connection{}}
	first := newTestRDPConnection(t, "multi-target-alpha")
	second := newTestRDPConnection(t, "multi-target-bravo")
	a.rdpConnections[first.Target.DeviceID] = first
	a.rdpConnections[second.Target.DeviceID] = second
	a.rdpActiveTarget = second.Target.DeviceID

	if first.ListenAddr == second.ListenAddr { t.Fatalf("separate devices shared port %s", first.ListenAddr) }
	assertRDPListener(t, first.ListenAddr, true)
	assertRDPListener(t, second.ListenAddr, true)

	a.DisconnectRDPTarget(second.Target.DeviceID)
	assertRDPListener(t, first.ListenAddr, true)
	assertRDPListener(t, second.ListenAddr, false)
	if len(a.rdpConnections) != 1 || a.rdpConnections[first.Target.DeviceID] != first {
		t.Fatalf("disconnect removed a different RDP session: %+v", a.rdpConnections)
	}

	a.DisconnectRDP()
	assertRDPListener(t, first.ListenAddr, false)
	if len(a.rdpConnections) != 0 { t.Fatalf("all sessions should be cleared: %+v", a.rdpConnections) }
}

func TestRDPDisconnectUnknownTargetPreservesExistingSession(t *testing.T) {
	a := &Agent{rdpConnections: map[string]*rdp.Connection{}}
	first := newTestRDPConnection(t, "multi-target-existing")
	a.rdpConnections[first.Target.DeviceID] = first
	a.DisconnectRDPTarget("some-other-target")
	assertRDPListener(t, first.ListenAddr, true)
	a.DisconnectRDP()
}

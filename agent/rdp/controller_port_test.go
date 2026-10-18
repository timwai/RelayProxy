package rdp

import (
	"net"
	"testing"
)

func TestPreferredControllerPortStablePerDevice(t *testing.T) {
	for _, id := range []string{"device-a", "device-b", "device-a"} {
		port := preferredControllerPort(id)
		if port < 40000 || port >= 60000 {
			t.Fatalf("invalid RDP port for %q: %d", id, port)
		}
		if port != preferredControllerPort(id) {
			t.Fatalf("port changed for device %q", id)
		}
	}
}

func TestControllerPortReusedAndOccupiedFallback(t *testing.T) {
	id := "test-controller-stable-port"
	tcp, udp, err := listenControllerPorts(id, true)
	if err != nil {
		t.Fatal(err)
	}
	first := tcp.Addr().(*net.TCPAddr).Port
	if udp.LocalAddr().(*net.UDPAddr).Port != first {
		t.Fatalf("TCP/UDP port mismatch")
	}
	// Keeping the first listener open forces a different port for the same device.
	otherTCP, otherUDP, err := listenControllerPorts(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if otherTCP.Addr().(*net.TCPAddr).Port == first {
		t.Fatal("occupied port was reused")
	}
	_ = otherUDP.Close()
	_ = otherTCP.Close()
	_ = udp.Close()
	_ = tcp.Close()
	reopenedTCP, reopenedUDP, err := listenControllerPorts(id, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedTCP.Close()
	defer reopenedUDP.Close()
	if reopenedTCP.Addr().(*net.TCPAddr).Port != first {
		t.Fatalf("expected port %d after close, got %d", first, reopenedTCP.Addr().(*net.TCPAddr).Port)
	}
}

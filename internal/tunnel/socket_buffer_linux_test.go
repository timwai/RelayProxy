//go:build linux

package tunnel

import (
	"net"
	"testing"
)

func TestQUICUDPSocketBufferDiagnostics(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	TuneQUICUDPConn(conn)
	readBytes, writeBytes := udpSocketBufferSizes(conn)
	if readBytes <= 0 || writeBytes <= 0 {
		t.Fatalf("UDP socket buffer diagnostics unavailable: read=%d write=%d", readBytes, writeBytes)
	}
}

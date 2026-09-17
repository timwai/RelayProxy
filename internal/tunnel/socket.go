package tunnel

import (
	"net"
	"time"
)

const (
	// Screen updates arrive as short UDP bursts. Four MiB gives the application
	// enough time to drain them before the OS starts discarding datagrams.
	udpSocketBufferBytes = 4 << 20
	tcpSocketBufferBytes = 256 << 10
)

// TuneUDPConn raises kernel queues so short bursts do not turn into packet
// loss before the application can drain them. The kernel may clamp these
// values; callers intentionally keep the listener usable if tuning fails.
func TuneUDPConn(conn *net.UDPConn) {
	if conn == nil {
		return
	}
	_ = conn.SetReadBuffer(udpSocketBufferBytes)
	_ = conn.SetWriteBuffer(udpSocketBufferBytes)
}

// TuneTCPConn applies the low-latency settings shared by tunnel and ingress
// sockets. It is safe to call for non-TCP connections.
func TuneTCPConn(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok || tcp == nil {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	_ = tcp.SetReadBuffer(tcpSocketBufferBytes)
	_ = tcp.SetWriteBuffer(tcpSocketBufferBytes)
}

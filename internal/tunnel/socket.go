package tunnel

import (
	"net"
	"time"
)

const (
	// Screen updates arrive as short UDP bursts. Four MiB gives the application
	// enough time to drain them before the OS starts discarding datagrams.
	udpSocketBufferBytes = 4 << 20
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
// sockets. Read and write buffers intentionally stay at the operating-system
// defaults so kernels such as Linux can autotune them for high-BDP paths. It is
// safe to call for non-TCP connections.
func TuneTCPConn(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok || tcp == nil {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
}

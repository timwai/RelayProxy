package tunnel

import (
	"net"
	"time"
)

const (
	// Screen updates arrive as short UDP bursts. Four MiB gives the application
	// enough time to drain them before the OS starts discarding datagrams.
	udpSocketBufferBytes = 4 << 20

	// QUIC benefits from deeper kernel queues on high-bandwidth, high-RTT paths.
	// The kernel can clamp this value to its configured maximum.
	quicUDPSocketBufferBytes = 8 << 20

	// Public Direct carries all proxy streams for an Exit over one long-lived
	// UDP socket. Give that shared socket extra burst headroom on high-BDP WANs
	// without increasing the per-session memory target used by P2P QUIC.
	directQUICUDPSocketBufferBytes = 16 << 20
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

// TuneQUICUDPConn raises kernel queues for high-throughput QUIC paths. Keeping
// this separate from TuneUDPConn avoids changing the buffering profile of
// latency-sensitive UDP workloads such as desktop streaming and P2P punching.
func TuneQUICUDPConn(conn *net.UDPConn) {
	if conn == nil {
		return
	}
	_ = conn.SetReadBuffer(quicUDPSocketBufferBytes)
	_ = conn.SetWriteBuffer(quicUDPSocketBufferBytes)
}

// TuneDirectQUICUDPConn uses a larger queue target for the stable Public Direct
// listener / client sockets. The OS is still free to clamp these requests to
// its configured maximums.
func TuneDirectQUICUDPConn(conn *net.UDPConn) {
	if conn == nil {
		return
	}
	_ = conn.SetReadBuffer(directQUICUDPSocketBufferBytes)
	_ = conn.SetWriteBuffer(directQUICUDPSocketBufferBytes)
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

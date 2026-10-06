//go:build !linux && !android

package tunnel

import "net"

func udpSocketBufferSizes(*net.UDPConn) (readBytes, writeBytes int) {
	return 0, 0
}

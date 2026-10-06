//go:build linux || android

package tunnel

import (
	"net"

	"golang.org/x/sys/unix"
)

func udpSocketBufferSizes(conn *net.UDPConn) (readBytes, writeBytes int) {
	if conn == nil {
		return 0, 0
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, 0
	}
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		readBytes, controlErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		if controlErr != nil {
			return
		}
		writeBytes, controlErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	}); err != nil || controlErr != nil {
		return 0, 0
	}
	return readBytes, writeBytes
}

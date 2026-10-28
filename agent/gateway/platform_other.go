//go:build !linux

package gateway

import (
	"errors"
	"net"
	"net/netip"
)

func Supported() bool          { return false }
func (s *Server) Start() error { return errors.New("LAN 网关仅支持 Linux") }
func Disable() error           { return nil }
func transparentReply(netip.AddrPort, string) (*net.UDPConn, error) {
	return nil, errors.New("LAN 网关仅支持 Linux")
}

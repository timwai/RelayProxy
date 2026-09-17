//go:build windows

package divert

import (
	"fmt"
	"net"
	"net/netip"
)

func platformCapabilities() Capabilities {
	caps := Capabilities{
		Platform: "windows", TCP: true, UDP: true, IPv6: true, Hostnames: true, HostnameSource: "dns",
		ProcessIdentity: true, OriginalDestination: true, ReplyInjection: true, LoopBypass: true,
	}
	if err := WindowsPlatformReadiness(); err != nil {
		caps.UnavailableReason = err.Error()
	}
	return caps
}

type windowsPacketDevice struct{ handle *windivertHandle }

func (d *windowsPacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	n, addr, err := d.handle.Recv(buffer)
	outbound := addr.outbound()
	return n, packetMetadata{outbound: outbound, capturedOutbound: outbound, ifIndex: addr.ifIndex(), subIfIndex: addr.subIfIndex()}, err
}
func (d *windowsPacketDevice) Send(packet []byte, meta packetMetadata) error {
	var addr windivertAddress
	addr.setOutbound(meta.outbound)
	addr.setIfIndex(meta.ifIndex, meta.subIfIndex)
	addr.setChecksums(len(packet) > 0 && packet[0]>>4 == 6)
	return d.handle.Send(packet, addr)
}
func (d *windowsPacketDevice) Shutdown() error { return d.handle.Shutdown() }
func (d *windowsPacketDevice) Close() error    { return d.handle.Close() }

func startPlatformInterceptor(s *Server) (systemInterceptor, error) {
	if err := prepareLoopGuard(s); err != nil {
		return nil, err
	}
	var listeners []net.Listener
	for _, family := range []struct{ network, address string }{{"tcp4", "0.0.0.0:0"}, {"tcp6", "[::]:0"}} {
		listener, err := net.Listen(family.network, family.address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("创建透明代理 %s 监听失败: %w", family.network, err)
		}
		listeners = append(listeners, listener)
	}
	port4 := listeners[0].Addr().(*net.TCPAddr).Port
	port6 := listeners[1].Addr().(*net.TCPAddr).Port
	filter := windowsInterceptFilter(port4, port6)
	handle, err := openWinDivert(filter)
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, err
	}
	i := newPacketInterceptor(s, &windowsPacketDevice{handle: handle}, listeners, func(protocol Protocol, source, destination netip.AddrPort) (packetProcess, error) {
		process, err := lookupPacketProcess(protocol, source, destination)
		return packetProcess{pid: process.PID, path: process.Path}, err
	})
	i.start()
	return i, nil
}

func windowsInterceptFilter(port4, port6 int) string {
	// Inbound observation supplies DIRECT download counters and DNS responses.
	// The adapter passes ordinary inbound packets through and protects reflection
	// listeners explicitly. This handle's own injections bypass capture.
	return fmt.Sprintf("(outbound and !loopback and (tcp or udp or fragment)) or (inbound and !loopback and (tcp or udp or fragment)) or (inbound and tcp and (tcp.DstPort == %d or tcp.DstPort == %d))", port4, port6)
}

//go:build windows

package divert

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/sys/windows"
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

func openWindowsPacketDevice(filter string) (packetDevice, error) {
	if device, err := openWindowsServicePacketDevice(filter); err == nil {
		return device, nil
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil, fmt.Errorf("RelayProxy Network Service 未运行；请重新启用系统透明代理以完成一次管理员安装")
	}
	handle, err := openWinDivert(filter)
	if err != nil {
		return nil, err
	}
	return &windowsPacketDevice{handle: handle}, nil
}

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
	filter := windowsInterceptFilter(port4, port6, s.guard)
	device, err := openWindowsPacketDevice(filter)
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		return nil, err
	}
	i := newPacketInterceptor(s, device, listeners, func(protocol Protocol, source, destination netip.AddrPort) (packetProcess, error) {
		process, err := lookupPacketProcess(protocol, source, destination)
		return packetProcess{pid: process.PID, path: process.Path, aliases: process.Aliases, services: process.Services}, err
	})
	i.start()
	return i, nil
}

func windowsInterceptFilter(port4, port6 int, guard LoopGuard) string {
	// Only take ownership of outbound traffic that requires a routing decision.
	// Ordinary inbound traffic must remain in the Windows network stack; using a
	// catch-all inbound handle turns every download packet into a mandatory
	// userspace reinjection and can blackhole the whole host if metadata differs.
	outbound := "(outbound and !loopback and (tcp or udp or fragment)"
	if bypass := windowsRelayBypassFilter(guard); bypass != "" {
		outbound += " and !(" + bypass + ")"
	}
	outbound += ")"

	// Listener-directed inbound packets are reserved for the transparent TCP
	// reflection path. Real Internet inbound packets are otherwise untouched.
	reflection := fmt.Sprintf("(inbound and !loopback and tcp and (tcp.DstPort == %d or tcp.DstPort == %d))", port4, port6)
	return outbound + " or " + reflection
}

func windowsRelayBypassFilter(guard LoopGuard) string {
	if len(guard.RelayIPs) == 0 || len(guard.RelayPorts) == 0 {
		return ""
	}
	ports := make([]string, 0, len(guard.RelayPorts))
	seenPorts := make(map[int]struct{}, len(guard.RelayPorts))
	for _, port := range guard.RelayPorts {
		if port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seenPorts[port]; ok {
			continue
		}
		seenPorts[port] = struct{}{}
		ports = append(ports, fmt.Sprintf("%d", port))
	}
	if len(ports) == 0 {
		return ""
	}
	tcpPorts := make([]string, 0, len(ports))
	udpPorts := make([]string, 0, len(ports))
	for _, port := range ports {
		tcpPorts = append(tcpPorts, "tcp.DstPort == "+port)
		udpPorts = append(udpPorts, "udp.DstPort == "+port)
	}
	transport := "(fragment or (tcp and (" + strings.Join(tcpPorts, " or ") + ")) or (udp and (" + strings.Join(udpPorts, " or ") + ")))"

	terms := make([]string, 0, len(guard.RelayIPs))
	seenIPs := make(map[string]struct{}, len(guard.RelayIPs))
	for _, value := range guard.RelayIPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		key := addr.String()
		if _, ok := seenIPs[key]; ok {
			continue
		}
		seenIPs[key] = struct{}{}
		if addr.Is4() {
			terms = append(terms, "(ip and ip.DstAddr == "+key+" and "+transport+")")
		} else {
			terms = append(terms, "(ipv6 and ipv6.DstAddr == "+key+" and "+transport+")")
		}
	}
	return strings.Join(terms, " or ")
}

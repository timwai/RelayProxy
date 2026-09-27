//go:build windows

package divert

import (
	"errors"
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

type windowsPacketDevice struct {
	handle         *windivertHandle
	removeFirewall bool
}

func (d *windowsPacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	n, addr, err := d.handle.Recv(buffer)
	outbound := addr.outbound()
	return n, packetMetadata{outbound: outbound, capturedOutbound: outbound, ifIndex: addr.ifIndex(), subIfIndex: addr.subIfIndex()}, err
}
func (d *windowsPacketDevice) Send(packet []byte, meta packetMetadata) error {
	var addr windivertAddress
	addr.setOutbound(meta.outbound)
	addr.setIfIndex(meta.ifIndex, meta.subIfIndex)
	return d.handle.Send(packet, addr)
}
func (d *windowsPacketDevice) Shutdown() error { return d.handle.Shutdown() }
func (d *windowsPacketDevice) Close() error {
	if d == nil {
		return nil
	}
	err := d.handle.Close()
	if d.removeFirewall {
		err = errors.Join(err, removeWindowsTransparentFirewallRule())
	}
	return err
}

func openWindowsPacketDevice(filter string, tcpPorts []uint16) (packetDevice, error) {
	if device, err := openWindowsServicePacketDevice(filter, tcpPorts); err == nil {
		return device, nil
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil, fmt.Errorf("RelayProxy Network Service 未运行或无法配置 Windows Firewall；请在 Network Service 中执行安装/修复")
	}
	if err := installWindowsTransparentFirewallRule(tcpPorts); err != nil {
		return nil, fmt.Errorf("配置透明代理 Windows Firewall 入站规则失败: %w", err)
	}
	handle, err := openWinDivert(filter)
	if err != nil {
		_ = removeWindowsTransparentFirewallRule()
		return nil, err
	}
	return &windowsPacketDevice{handle: handle, removeFirewall: true}, nil
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
	device, err := openWindowsPacketDevice(filter, []uint16{uint16(port4), uint16(port6)})
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
	outbound := "(outbound and !loopback and (tcp or udp)"
	for _, clause := range windowsRelayBypassClauses(guard) {
		outbound += " and " + clause
	}
	outbound += ")"

	// Keep ordinary inbound traffic in the Windows stack. Only the transparent
	// TCP reflection path and DNS responses used for hostname attribution are
	// observed. This preserves domain rules without making all downloads depend
	// on userspace reinjection.
	reflection := fmt.Sprintf("(inbound and !loopback and tcp and (tcp.DstPort == %d or tcp.DstPort == %d))", port4, port6)
	dnsResponse := "(inbound and !loopback and udp and udp.SrcPort == 53)"
	return outbound + " or " + reflection + " or " + dnsResponse
}

func windowsRelayBypassClauses(guard LoopGuard) []string {
	clauses := make([]string, 0, len(guard.RelayIPs))
	seen := make(map[string]struct{}, len(guard.RelayIPs))
	for _, value := range guard.RelayIPs {
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		key := addr.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		// Exclude the complete Relay server address, not just one port. The Relay
		// control/data plane must never depend on the transparent proxy it keeps
		// alive. Applying ! only to WinDivert's boolean family field avoids the
		// unsupported negation of a compound expression.
		if addr.Is4() {
			clauses = append(clauses, "(!ip or ip.DstAddr != "+key+")")
		} else {
			clauses = append(clauses, "(!ipv6 or ipv6.DstAddr != "+key+")")
		}
	}
	return clauses
}

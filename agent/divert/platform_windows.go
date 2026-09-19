//go:build windows

package divert

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
)

func platformCapabilities() Capabilities {
	backend, err := selectWindowsBackend()
	caps := Capabilities{
		Platform: "windows", Backend: backend,
		TCP: true, UDP: true, IPv6: true, Hostnames: true, HostnameSource: "dns",
		ProcessIdentity: true, OriginalDestination: true, ReplyInjection: true, LoopBypass: true,
	}
	if err != nil {
		caps.UnavailableReason = err.Error()
	}
	return caps
}

// WindowsPlatformReadiness is side-effect free with respect to interception:
// it opens an already installed WFP device or validates WinDivert dependencies,
// but never installs a driver, opens a listener, or changes filtering policy.
func WindowsPlatformReadiness() error {
	_, err := selectWindowsBackend()
	return err
}

func windowsBackendPreference() (string, error) {
	requested := strings.ToLower(strings.TrimSpace(os.Getenv("RELAYPROXY_WINDOWS_BACKEND")))
	if requested == "" {
		requested = "auto"
	}
	if requested != "auto" && requested != "wfp" && requested != "windivert" {
		return "", fmt.Errorf("unknown RELAYPROXY_WINDOWS_BACKEND %q", requested)
	}
	return requested, nil
}

func selectWindowsBackend() (string, error) {
	requested, err := windowsBackendPreference()
	if err != nil {
		return "", err
	}

	if requested == "auto" || requested == "wfp" {
		if err := wfpPlatformReadiness(); err == nil {
			return "wfp", nil
		} else if requested == "wfp" || runtime.GOARCH != "amd64" {
			return "wfp", err
		}
	}
	if runtime.GOARCH != "amd64" {
		return "wfp", errors.New("Windows ARM64 系统透明代理需要 RelayProxyWfp ARM64 驱动")
	}
	if err := winDivertPlatformReadiness(); err != nil {
		return "windivert", err
	}
	return "windivert", nil
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
	backend, err := selectWindowsBackend()
	if err != nil {
		return nil, err
	}
	switch backend {
	case "wfp":
		return startWFPInterceptor(s)
	case "windivert":
		return startWinDivertInterceptor(s)
	default:
		return nil, fmt.Errorf("unsupported Windows interception backend %q", backend)
	}
}

func startWinDivertInterceptor(s *Server) (systemInterceptor, error) {
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
	return fmt.Sprintf("(outbound and !loopback and (tcp or udp or fragment)) or (inbound and !loopback and (tcp or udp or fragment)) or (inbound and tcp and (tcp.DstPort == %d or tcp.DstPort == %d))", port4, port6)
}

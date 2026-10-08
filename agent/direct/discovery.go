package direct

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"relayproxy/internal/protocol"
)

func DiscoverEndpointCandidates(listenerPort uint16, manualAdvertise string) ([]protocol.PublicDirectEndpointCandidate, error) {
	if listenerPort == 0 {
		return nil, errors.New("public direct listener port is required")
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	return endpointCandidates(listenerPort, manualAdvertise, addrs)
}

func endpointCandidates(listenerPort uint16, manualAdvertise string, addrs []net.Addr) ([]protocol.PublicDirectEndpointCandidate, error) {
	result := make([]protocol.PublicDirectEndpointCandidate, 0)
	seen := make(map[string]struct{})
	for _, address := range addrs {
		ip := addressIP(address)
		if !ip.IsValid() || !ip.Is6() || !isGlobalDirectIP(ip) {
			continue
		}
		endpoint := net.JoinHostPort(ip.String(), strconv.Itoa(int(listenerPort)))
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		result = append(result, protocol.PublicDirectEndpointCandidate{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  endpoint,
			Source:   protocol.PublicDirectEndpointIPv6,
		})
	}

	manualAdvertise = strings.TrimSpace(manualAdvertise)
	if manualAdvertise != "" {
		host, portText, err := net.SplitHostPort(manualAdvertise)
		if err != nil || strings.TrimSpace(host) == "" {
			return nil, errors.New("manual public direct advertise address must be host:port")
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("manual public direct advertise port is invalid")
		}
		endpoint := net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
		if _, ok := seen[endpoint]; !ok {
			result = append(result, protocol.PublicDirectEndpointCandidate{
				Protocol: protocol.PublicDirectEndpointProtocolUDP,
				Address:  endpoint,
				Source:   protocol.PublicDirectEndpointManual,
			})
		}
	}
	return result, nil
}

func addressIP(address net.Addr) netip.Addr {
	switch value := address.(type) {
	case *net.IPNet:
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			return ip.Unmap()
		}
	case *net.IPAddr:
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			return ip.Unmap()
		}
	default:
		text := strings.TrimSpace(address.String())
		if slash := strings.IndexByte(text, '/'); slash >= 0 {
			text = text[:slash]
		}
		if ip, err := netip.ParseAddr(text); err == nil {
			return ip.Unmap()
		}
	}
	return netip.Addr{}
}

func isGlobalDirectIP(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

// localEndpointReachable is a conservative, side-effect-free family/route
// preflight. In particular, Windows can create an udp6 socket even when it has
// no IPv6 route, only to fail at sendto with WSAENETUNREACH.
func localEndpointReachable(address string) bool {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Legacy verified hostname without a Server-pinned dialAddress.
		// DNS family choice stays with the normal dialer.
		return true
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return true
	}
	if !ip.Is6() || !ip.IsGlobalUnicast() {
		return false
	}
	if !localHasIPv6Source() {
		return false
	}
	// UDP connect asks the kernel for a route/source address without sending a
	// datagram or consuming a one-time Public Direct ticket.
	conn, err := net.DialUDP("udp6", nil, &net.UDPAddr{IP: net.IP(ip.AsSlice()), Port: port})
	if err != nil {
		return false
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return false
	}
	source, ok := netip.AddrFromSlice(local.IP)
	return ok && isUsableIPv6Source(source)
}

func isUsableIPv6Source(ip netip.Addr) bool {
	return ip.Is6() && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLoopback() && !ip.IsUnspecified()
}

func localHasIPv6Source() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if ip := addressIP(address); isUsableIPv6Source(ip) {
				return true
			}
		}
	}
	return false
}

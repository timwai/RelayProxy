// Package upnp implements the small subset of UPnP IGD needed by the
// RelayProxy P2P transport. Discovery uses SSDP, while mappings are managed
// through WANIPConnection/WANPPPConnection SOAP actions.
package upnp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	ssdpAddress         = "239.255.255.250:1900"
	discoveryWindow     = 1500 * time.Millisecond
	mappingLeaseSeconds = uint32(3600)
	maxDescriptionBytes = 1 << 20
	maxSOAPBytes        = 1 << 20
	maxSSDPResponses    = 32
)

var (
	ErrUnavailable    = errors.New("UPnP IGD is unavailable")
	ErrPermanentLease = errors.New("UPnP router only supports permanent mappings; refusing unsafe permanent port exposure")
	ErrNonPublicWAN   = errors.New("UPnP gateway WAN address is not publicly routable")
)

type service struct {
	serviceType string
	controlURL  *url.URL
	gatewayIP   netip.Addr // pinned during SSDP description discovery
	localIP     netip.Addr // interface that actually received the SSDP reply
}

// MappingUpdate reports when a UPnP candidate becomes invalid or changes.
// Consumers must withdraw the old address from rendezvous signaling.
type MappingUpdate struct {
	Address netip.AddrPort
	Healthy bool
	Reason  string
}

type Mapping struct {
	service        service
	internalClient string
	internalPort   uint16
	externalPort   uint16
	externalIP     netip.Addr
	leaseSeconds   uint32
	done           chan struct{}
	closeOnce      sync.Once
	opMu           sync.Mutex // serializes renew, remap, and final delete
	statusMu       sync.RWMutex
	status         MappingUpdate
	updates        chan MappingUpdate
	closeErr       error
}

type rootDescription struct {
	URLBase string            `xml:"URLBase"`
	Device  deviceDescription `xml:"device"`
}

type deviceDescription struct {
	Services []serviceDescription `xml:"serviceList>service"`
	Devices  []deviceDescription  `xml:"deviceList>device"`
}

type serviceDescription struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

type soapFault struct {
	Status      int
	Code        int
	Description string
}

func (e *soapFault) Error() string {
	if e == nil {
		return "UPnP SOAP error"
	}
	if e.Code != 0 && e.Description != "" {
		return fmt.Sprintf("UPnP SOAP error %d: %s", e.Code, e.Description)
	}
	if e.Code != 0 {
		return fmt.Sprintf("UPnP SOAP error %d", e.Code)
	}
	if e.Status != 0 {
		return fmt.Sprintf("UPnP SOAP HTTP status %d", e.Status)
	}
	return "UPnP SOAP error"
}

// MapUDP discovers a local IGD and maps an external UDP port to internalPort.
// The returned Mapping refreshes finite leases and removes the mapping on Close.
func MapUDP(ctx context.Context, internalPort int) (*Mapping, netip.AddrPort, error) {
	if internalPort < 1 || internalPort > 65535 {
		return nil, netip.AddrPort{}, fmt.Errorf("invalid UPnP internal UDP port %d", internalPort)
	}
	services, err := discoverServices(ctx)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	var lastErr error
	for _, svc := range services {
		// Select the LAN-side source address using the pinned gateway IP,
		// not a hostname that could resolve differently on a later lookup.
		pinnedURL := *svc.controlURL
		if svc.gatewayIP.IsValid() {
			port := svc.controlURL.Port()
			if port == "" {
				port = "80"
			}
			pinnedURL.Host = net.JoinHostPort(svc.gatewayIP.String(), port)
		}
		// Use the exact interface that received SSDP; the SOAP HTTP
		// client is bound to that same local address. Never substitute
		// another interface when networks have overlapping subnets.
		internalIP := svc.localIP
		var err error
		if !internalIP.IsValid() {
			internalIP, err = localIPv4For(ctx, &pinnedURL)
		}
		if err != nil {
			lastErr = err
			continue
		}
		externalIP, err := svc.externalIPAddress(ctx)
		if err != nil {
			lastErr = err
			continue
		}
		externalPort, leaseSeconds, err := svc.addAvailableUDPMapping(ctx, internalIP.String(), uint16(internalPort))
		if err != nil {
			lastErr = err
			continue
		}
		address := netip.AddrPortFrom(externalIP, externalPort)
		m := &Mapping{
			service: svc, internalClient: internalIP.String(),
			internalPort: uint16(internalPort), externalPort: externalPort,
			externalIP: externalIP, leaseSeconds: leaseSeconds,
			done: make(chan struct{}), updates: make(chan MappingUpdate, 1),
			status: MappingUpdate{Address: address, Healthy: true},
		}
		if ctx.Err() != nil {
			_ = m.Close()
			return nil, netip.AddrPort{}, ctx.Err()
		}
		go m.refreshLoop()
		return m, address, nil
	}
	if lastErr != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
	}
	return nil, netip.AddrPort{}, ErrUnavailable
}

// Updates returns a bounded, coalesced stream of state transitions.
func (m *Mapping) Updates() <-chan MappingUpdate {
	if m == nil {
		return nil
	}
	return m.updates
}

func (m *Mapping) Status() MappingUpdate {
	if m == nil {
		return MappingUpdate{Healthy: false, Reason: "UPnP mapping is unavailable"}
	}
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.status
}

// publish is called only while opMu is held so updates cannot overtake Close.
func (m *Mapping) publish(next MappingUpdate) {
	m.statusMu.Lock()
	prior := m.status
	if prior == next {
		m.statusMu.Unlock()
		return
	}
	m.status = next
	m.statusMu.Unlock()
	if m.updates != nil {
		select {
		case m.updates <- next:
		default:
			select {
			case <-m.updates:
			default:
			}
			select {
			case m.updates <- next:
			default:
			}
		}
	}
}

func (m *Mapping) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		if m.done != nil {
			close(m.done)
		}
		// Wait for in-flight SOAP AddPortMapping to complete, preventing
		// a racing refresh from reopening a successfully removed port.
		m.opMu.Lock()
		defer m.opMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A lease may have expired and the port may now belong to another
		// device. Never delete a mapping without checking its ownership.
		ownerIP, ownerPort, err := m.service.mappingOwner(ctx, m.externalPort)
		if err == nil && (ownerIP != m.internalClient || ownerPort != m.internalPort) {
			err = errors.New("UPnP mapping ownership changed; refusing to delete another device's port")
		}
		if err == nil {
			err = m.service.deletePortMapping(ctx, m.externalPort)
		}
		m.closeErr = err
		m.publish(MappingUpdate{Healthy: false, Reason: "closed"})
	})
	return m.closeErr
}

func (m *Mapping) refreshLoop() {
	interval := time.Duration(m.leaseSeconds) * time.Second / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.refresh(ctx)
		cancel()
		next := interval
		if err != nil {
			// Retry promptly after router reboot, IP change, or a timeout.
			// The ongoing lease is never extended without confirmation.
			next = 30 * time.Second
		}
		timer.Reset(next)
	}
}

// refresh is intentionally separate from the timer for deterministic tests.
func (m *Mapping) refresh(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	select {
	case <-m.done:
		return net.ErrClosed
	default:
	}
	err := m.service.addPortMapping(ctx, m.externalPort, m.internalPort, m.internalClient, m.leaseSeconds)
	if soapErrorCode(err) == 718 {
		// A different owner now occupies the port. Try a fresh random
		// port rather than deleting or overwriting the other mapping.
		var nextPort uint16
		nextPort, _, err = m.service.addAvailableUDPMapping(ctx, m.internalClient, m.internalPort)
		if err == nil {
			m.externalPort = nextPort
		}
	}
	if err != nil {
		m.publish(MappingUpdate{Healthy: false, Reason: err.Error()})
		return err
	}
	ip, err := m.service.externalIPAddress(ctx)
	if err != nil {
		m.publish(MappingUpdate{Healthy: false, Reason: err.Error()})
		return err
	}
	m.externalIP = ip
	m.publish(MappingUpdate{Healthy: true, Address: netip.AddrPortFrom(ip, m.externalPort)})
	return nil
}

type discoveredGateway struct {
	location string
	sender   netip.Addr
	localIP  netip.Addr
}

type networkInterfaceIPv4 struct {
	address netip.Addr
	subnet  *net.IPNet
	index   int
}

// activeLANInterfaces discovers multicast-capable on-link interfaces. The
// preferred system egress interface is tried first, with remaining physical
// routes as a fallback when the primary router has no IGD service.
func activeLANInterfaces() []networkInterfaceIPv4 {
	preferred := defaultRouteIPv4()
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []networkInterfaceIPv4
	seen := make(map[netip.Addr]bool)
	for _, iface := range ifaces {
		if iface.Flags&(net.FlagUp|net.FlagMulticast) != net.FlagUp|net.FlagMulticast ||
			iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addresses {
			subnet, ok := raw.(*net.IPNet)
			if !ok || subnet.IP.To4() == nil {
				continue
			}
			ip, valid := netip.AddrFromSlice(subnet.IP.To4())
			if !valid || !ip.IsPrivate() || seen[ip] {
				continue
			}
			seen[ip] = true
			out = append(out, networkInterfaceIPv4{address: ip, subnet: subnet, index: iface.Index})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].address == preferred {
			return true
		}
		if out[j].address == preferred {
			return false
		}
		return out[i].address.Less(out[j].address)
	})
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

func defaultRouteIPv4() netip.Addr {
	conn, err := net.DialTimeout("udp4", "1.1.1.1:53", 250*time.Millisecond)
	if err != nil {
		return netip.Addr{}
	}
	defer conn.Close()
	if ip, ok := netip.AddrFromSlice(conn.LocalAddr().(*net.UDPAddr).IP); ok {
		return ip.Unmap()
	}
	return netip.Addr{}
}

func discoverOnInterface(ctx context.Context, iface networkInterfaceIPv4, deadline time.Time) []discoveredGateway {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IP(iface.address.AsSlice())})
	if err != nil {
		return nil
	}
	defer conn.Close()
	if iface.index != 0 {
		if nic, err := net.InterfaceByIndex(iface.index); err == nil {
			if err := ipv4.NewPacketConn(conn).SetMulticastInterface(nic); err != nil {
				return nil
			}
		}
	}
	_ = conn.SetDeadline(deadline)
	remote, err := net.ResolveUDPAddr("udp4", ssdpAddress)
	if err != nil {
		return nil
	}
	for _, st := range []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"upnp:rootdevice",
	} {
		request := "M-SEARCH * HTTP/1.1\r\n" +
			"HOST: " + ssdpAddress + "\r\n" +
			"MAN: \"ssdp:discover\"\r\n" +
			"MX: 1\r\n" +
			"ST: " + st + "\r\n\r\n"
		_, _ = conn.WriteToUDP([]byte(request), remote)
	}
	locations := make(map[string]discoveredGateway)
	buffer := make([]byte, 64*1024)
	for len(locations) < maxSSDPResponses {
		if ctx.Err() != nil {
			break
		}
		n, from, err := conn.ReadFromUDP(buffer)
		if err != nil {
			break
		}
		if !strings.HasPrefix(string(buffer[:n]), "HTTP/1.1 200") &&
			!strings.HasPrefix(string(buffer[:n]), "HTTP/1.0 200") {
			continue
		}
		sender, ok := netip.AddrFromSlice(from.IP)
		if !ok {
			continue
		}
		sender = sender.Unmap()
		if !onInterfaceSubnet(sender, iface) {
			continue
		}
		if location := ssdpHeader(string(buffer[:n]), "location"); location != "" {
			locations[location] = discoveredGateway{location: location, sender: sender, localIP: iface.address}
		}
	}
	result := make([]discoveredGateway, 0, len(locations))
	for _, gateway := range locations {
		result = append(result, gateway)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].location < result[j].location })
	return result
}

func onInterfaceSubnet(gateway netip.Addr, iface networkInterfaceIPv4) bool {
	return gateway.Is4() && !gateway.IsLoopback() &&
		iface.subnet != nil && gateway != iface.address &&
		iface.subnet.Contains(net.IP(gateway.AsSlice()))
}

func discoverServices(ctx context.Context) ([]service, error) {
	interfaces := activeLANInterfaces()
	if len(interfaces) == 0 {
		return nil, ErrUnavailable
	}
	deadline := time.Now().Add(discoveryWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	// Discover on every eligible interface concurrently, avoiding arbitrary
	// OS multicast routing decisions that otherwise favor VPN adapters.
	found := make(chan []discoveredGateway, len(interfaces))
	for _, iface := range interfaces {
		go func(iface networkInterfaceIPv4) {
			found <- discoverOnInterface(ctx, iface, deadline)
		}(iface)
	}
	var locations []discoveredGateway
	for range interfaces {
		locations = append(locations, (<-found)...)
	}
	if len(locations) == 0 {
		return nil, ErrUnavailable
	}
	preferred := defaultRouteIPv4()
	sort.SliceStable(locations, func(i, j int) bool {
		if locations[i].localIP == preferred {
			return true
		}
		if locations[j].localIP == preferred {
			return false
		}
		return locations[i].location < locations[j].location
	})
	var result []service
	var lastErr error
	for _, gateway := range locations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		found, err := fetchServices(ctx, gateway.location, gateway.sender, gateway.localIP)
		if err != nil {
			lastErr = err
			continue
		}
		result = append(result, found...)
	}
	if len(result) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
		}
		return nil, ErrUnavailable
	}
	// Prefer the active/default interface before comparing IGD service
	// versions; an IGD v2 on a disconnected VM NIC must not trump Wi-Fi.
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].localIP == preferred && result[j].localIP != preferred {
			return true
		}
		if result[j].localIP == preferred && result[i].localIP != preferred {
			return false
		}
		return serviceRank(result[i].serviceType) > serviceRank(result[j].serviceType)
	})
	return result, nil
}

func ssdpHeader(packet, wanted string) string {
	for _, line := range strings.Split(strings.ReplaceAll(packet, "\r\n", "\n"), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), wanted) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fetchServices(ctx context.Context, rawLocation string, sender netip.Addr, localIPs ...netip.Addr) ([]service, error) {
	location, err := url.Parse(strings.TrimSpace(rawLocation))
	if err != nil || location.Scheme != "http" || location.Hostname() == "" || location.User != nil {
		return nil, errors.New("invalid UPnP device description URL")
	}
	gatewayIP, err := resolveGatewayIPv4(ctx, location.Hostname())
	if err != nil {
		return nil, err
	}
	if sender.IsValid() && gatewayIP != sender.Unmap() {
		return nil, errors.New("UPnP description host differs from SSDP response sender")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	localIP := netip.Addr{}
	if len(localIPs) != 0 {
		localIP = localIPs[0]
	}
	if localIP.IsValid() && !gatewayIsOnSelectedInterface(gatewayIP, localIP) {
		return nil, errors.New("UPnP gateway is not reachable through the selected interface")
	}
	client := gatewayHTTPClient(location.Hostname(), gatewayIP, localIP)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("UPnP description HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDescriptionBytes))
	if err != nil {
		return nil, err
	}
	var root rootDescription
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	base := location
	if strings.TrimSpace(root.URLBase) != "" {
		if candidate, parseErr := url.Parse(strings.TrimSpace(root.URLBase)); parseErr == nil &&
			candidate.Scheme == "http" && candidate.User == nil &&
			strings.EqualFold(candidate.Hostname(), location.Hostname()) {
			base = candidate
		}
	}
	var descriptions []serviceDescription
	collectServiceDescriptions(root.Device, &descriptions)
	result := make([]service, 0, len(descriptions))
	seen := map[string]struct{}{}
	for _, item := range descriptions {
		if serviceRank(item.ServiceType) == 0 {
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(item.ControlURL))
		if err != nil {
			continue
		}
		control := base.ResolveReference(ref)
		if control.Scheme != "http" || control.Hostname() == "" || control.User != nil ||
			!strings.EqualFold(control.Hostname(), location.Hostname()) {
			continue
		}
		key := item.ServiceType + "|" + control.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, service{serviceType: strings.TrimSpace(item.ServiceType), controlURL: control, gatewayIP: gatewayIP, localIP: localIP})
	}
	if len(result) == 0 {
		return nil, errors.New("UPnP IGD has no WAN connection service")
	}
	return result, nil
}

func collectServiceDescriptions(device deviceDescription, out *[]serviceDescription) {
	*out = append(*out, device.Services...)
	for _, child := range device.Devices {
		collectServiceDescriptions(child, out)
	}
}

func serviceRank(serviceType string) int {
	switch strings.TrimSpace(serviceType) {
	case "urn:schemas-upnp-org:service:WANIPConnection:2":
		return 30
	case "urn:schemas-upnp-org:service:WANIPConnection:1":
		return 20
	case "urn:schemas-upnp-org:service:WANPPPConnection:1":
		return 10
	default:
		return 0
	}
}

// onLinkGatewayIPv4 rejects public, loopback and off-link SSDP sources;
// private network addresses are not by themselves proof of locality.
func onLinkGatewayIPv4(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsValid() || (!ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
		return false
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if network, ok := addr.(*net.IPNet); ok && network.IP.To4() != nil &&
				network.Contains(net.IP(ip.AsSlice())) {
				return true
			}
		}
	}
	return false
}

func gatewayIsOnSelectedInterface(gateway, local netip.Addr) bool {
	if !gateway.Is4() || !local.Is4() {
		return false
	}
	interfaces := activeLANInterfaces()
	for _, iface := range interfaces {
		if iface.address == local {
			return onInterfaceSubnet(gateway, iface)
		}
	}
	return false
}

func resolveGatewayIPv4(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if onLinkGatewayIPv4(ip) {
			return ip, nil
		}
		return netip.Addr{}, errors.New("UPnP gateway is not an on-link private IPv4 address")
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, ip := range addrs {
		if onLinkGatewayIPv4(ip) {
			return ip.Unmap(), nil
		}
	}
	return netip.Addr{}, errors.New("UPnP hostname has no on-link private IPv4 address")
}

// gatewayHTTPClient disables proxy use and redirects, and pins every request
// to the previously vetted gateway IP. A hostname cannot rebind to another
// target between SSDP, description GET, and subsequent SOAP calls.
func gatewayHTTPClient(host string, gatewayIP netip.Addr, localIPs ...netip.Addr) *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if gatewayIP.IsValid() {
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			targetHost, port, err := net.SplitHostPort(addr)
			if err != nil || !strings.EqualFold(targetHost, host) {
				return nil, errors.New("UPnP tried to contact a different gateway")
			}
			var dialer net.Dialer
			if len(localIPs) > 0 && localIPs[0].Is4() {
				dialer.LocalAddr = &net.TCPAddr{IP: net.IP(localIPs[0].AsSlice())}
			}
			return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(gatewayIP.String(), port))
		}
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("UPnP control redirects are disabled")
		},
	}
}

func localIPv4For(ctx context.Context, controlURL *url.URL) (netip.Addr, error) {
	if controlURL == nil {
		return netip.Addr{}, errors.New("missing UPnP control URL")
	}
	port := controlURL.Port()
	if port == "" {
		port = "80"
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(controlURL.Hostname(), port))
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	udp, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, errors.New("cannot determine UPnP internal client address")
	}
	ip, ok := netip.AddrFromSlice(udp.IP)
	if !ok {
		return netip.Addr{}, errors.New("invalid UPnP internal client address")
	}
	ip = ip.Unmap()
	if !ip.Is4() || ip.IsUnspecified() {
		return netip.Addr{}, errors.New("UPnP requires an IPv4 internal client address")
	}
	return ip, nil
}

func (s service) externalIPAddress(ctx context.Context) (netip.Addr, error) {
	body, err := s.soap(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return netip.Addr{}, err
	}
	value := xmlElementText(body, "NewExternalIPAddress")
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, err
	}
	ip = ip.Unmap()
	if !isPublicWANIPv4(ip) {
		return netip.Addr{}, fmt.Errorf("%w: %s", ErrNonPublicWAN, ip)
	}
	return ip, nil
}

func isPublicWANIPv4(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	// CGNAT, protocol benchmarking and documentation prefixes are not
	// Internet-routable, despite IsGlobalUnicast returning true.
	for _, block := range []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
	} {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func (s service) addAvailableUDPMapping(ctx context.Context, internalClient string, internalPort uint16) (uint16, uint32, error) {
	ports := []uint16{internalPort}
	for len(ports) < 8 {
		var raw [2]byte
		if _, err := rand.Read(raw[:]); err != nil {
			break
		}
		port := uint16(1024 + int(binary.BigEndian.Uint16(raw[:]))%(65535-1024))
		duplicate := false
		for _, existing := range ports {
			if existing == port {
				duplicate = true
				break
			}
		}
		if !duplicate {
			ports = append(ports, port)
		}
	}
	for _, externalPort := range ports {
		err := s.addPortMapping(ctx, externalPort, internalPort, internalClient, mappingLeaseSeconds)
		if err == nil {
			return externalPort, mappingLeaseSeconds, nil
		}
		code := soapErrorCode(err)
		if code == 725 {
			// An abnormal agent termination cannot delete an infinite lease.
			return 0, 0, ErrPermanentLease
		}
		if code == 724 || code != 718 {
			return 0, 0, err
		}
	}
	return 0, 0, errors.New("UPnP router has no available external UDP port")
}

func (s service) addPortMapping(ctx context.Context, externalPort, internalPort uint16, internalClient string, lease uint32) error {
	args := map[string]string{
		"NewRemoteHost":             "",
		"NewExternalPort":           strconv.Itoa(int(externalPort)),
		"NewProtocol":               "UDP",
		"NewInternalPort":           strconv.Itoa(int(internalPort)),
		"NewInternalClient":         internalClient,
		"NewEnabled":                "1",
		"NewPortMappingDescription": "RelayProxy P2P",
		"NewLeaseDuration":          strconv.FormatUint(uint64(lease), 10),
	}
	_, err := s.soap(ctx, "AddPortMapping", args)
	return err
}

// mappingOwner verifies that the external UDP mapping still points to this
// Agent before Close deletes it. Failing closed is safer than deleting another
// device's mapping if the router recycled the external port.
func (s service) mappingOwner(ctx context.Context, externalPort uint16) (string, uint16, error) {
	body, err := s.soap(ctx, "GetSpecificPortMappingEntry", map[string]string{
		"NewRemoteHost": "", "NewExternalPort": strconv.Itoa(int(externalPort)), "NewProtocol": "UDP",
	})
	if err != nil {
		return "", 0, err
	}
	client := xmlElementText(body, "NewInternalClient")
	port, err := strconv.ParseUint(xmlElementText(body, "NewInternalPort"), 10, 16)
	if err != nil || client == "" || port == 0 {
		return "", 0, errors.New("UPnP router returned incomplete mapping ownership")
	}
	return client, uint16(port), nil
}

func (s service) deletePortMapping(ctx context.Context, externalPort uint16) error {
	args := map[string]string{
		"NewRemoteHost":   "",
		"NewExternalPort": strconv.Itoa(int(externalPort)),
		"NewProtocol":     "UDP",
	}
	_, err := s.soap(ctx, "DeletePortMapping", args)
	return err
}

func (s service) soap(ctx context.Context, action string, args map[string]string) ([]byte, error) {
	if s.controlURL == nil || s.serviceType == "" {
		return nil, errors.New("invalid UPnP service")
	}
	var payload strings.Builder
	payload.WriteString(`<?xml version="1.0"?>`)
	payload.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:`)
	payload.WriteString(action)
	payload.WriteString(` xmlns:u="`)
	payload.WriteString(xmlEscape(s.serviceType))
	payload.WriteString(`">`)
	if len(args) > 0 {
		keys := make([]string, 0, len(args))
		for key := range args {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			payload.WriteString("<")
			payload.WriteString(key)
			payload.WriteString(">")
			payload.WriteString(xmlEscape(args[key]))
			payload.WriteString("</")
			payload.WriteString(key)
			payload.WriteString(">")
		}
	}
	payload.WriteString(`</u:`)
	payload.WriteString(action)
	payload.WriteString(`></s:Body></s:Envelope>`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.controlURL.String(), strings.NewReader(payload.String()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+s.serviceType+"#"+action+`"`)
	req.Header.Set("Connection", "close")
	resp, err := gatewayHTTPClient(s.controlURL.Hostname(), s.gatewayIP, s.localIP).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSOAPBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || hasSOAPFault(body) {
		code, _ := strconv.Atoi(xmlElementText(body, "errorCode"))
		return nil, &soapFault{Status: resp.StatusCode, Code: code, Description: xmlElementText(body, "errorDescription")}
	}
	return body, nil
}

func hasSOAPFault(body []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "Fault" {
			return true
		}
	}
}

func soapErrorCode(err error) int {
	var fault *soapFault
	if errors.As(err, &fault) {
		return fault.Code
	}
	return 0
}

func xmlElementText(body []byte, localName string) string {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != localName {
			continue
		}
		var value string
		if err := decoder.DecodeElement(&value, &start); err == nil {
			return strings.TrimSpace(value)
		}
		return ""
	}
}

func xmlEscape(value string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(value))
	return out.String()
}

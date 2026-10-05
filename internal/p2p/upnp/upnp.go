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
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ssdpAddress         = "239.255.255.250:1900"
	discoveryWindow     = 1500 * time.Millisecond
	mappingLeaseSeconds = uint32(3600)
	maxDescriptionBytes = 1 << 20
	maxSOAPBytes        = 1 << 20
	serviceCacheTTL     = 5 * time.Minute
)

var ErrUnavailable = errors.New("UPnP IGD is unavailable")

var serviceCache = struct {
	sync.Mutex
	networkKey string
	expiresAt  time.Time
	services   []service
}{}

type service struct {
	serviceType string
	controlURL  *url.URL
	controlIP   netip.Addr
}

type soapArgument struct {
	name  string
	value string
}

type Mapping struct {
	service        service
	internalClient string
	internalPort   uint16
	externalPort   uint16
	leaseSeconds   uint32
	refreshCancel  context.CancelFunc
	refreshWG      sync.WaitGroup
	closeOnce      sync.Once
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
	services, cacheKey, fromCache, err := discoverServicesCached(ctx)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		for _, svc := range services {
			internalIP, err := localIPv4For(ctx, svc)
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
			m := &Mapping{
				service:         svc,
				internalClient:  internalIP.String(),
				internalPort:    uint16(internalPort),
				externalPort:    externalPort,
				leaseSeconds:    leaseSeconds,
			}
			if leaseSeconds > 0 {
				refreshCtx, cancel := context.WithCancel(context.Background())
				m.refreshCancel = cancel
				m.refreshWG.Add(1)
				go m.refreshLoop(refreshCtx)
			}
			return m, netip.AddrPortFrom(externalIP, externalPort), nil
		}

		if !fromCache {
			break
		}
		invalidateServiceCache(cacheKey)
		services, err = discoverServices(ctx)
		if err != nil {
			lastErr = err
			break
		}
		storeServicesInCache(cacheKey, services)
		fromCache = false
	}
	if lastErr != nil {
		return nil, netip.AddrPort{}, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
	}
	return nil, netip.AddrPort{}, ErrUnavailable
}

func (m *Mapping) Close() error {
	if m == nil {
		return nil
	}
	var closeErr error
	m.closeOnce.Do(func() {
		if m.refreshCancel != nil {
			m.refreshCancel()
		}
		m.refreshWG.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeErr = m.service.deletePortMapping(ctx, m.externalPort)
	})
	return closeErr
}

func mappingRefreshInterval(leaseSeconds uint32) time.Duration {
	interval := time.Duration(leaseSeconds) * time.Second / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	return interval
}

func mappingRefreshRetryDelay(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 5 * time.Second
	case failures == 2:
		return 15 * time.Second
	case failures == 3:
		return 30 * time.Second
	default:
		return time.Minute
	}
}

func (m *Mapping) refreshLoop(ctx context.Context) {
	defer m.refreshWG.Done()
	delay := mappingRefreshInterval(m.leaseSeconds)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := m.service.addPortMapping(requestCtx, m.externalPort, m.internalPort, m.internalClient, m.leaseSeconds)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				failures++
				delay = mappingRefreshRetryDelay(failures)
				log.Printf("[P2P][UPnP] refresh UDP mapping %d->%s:%d failed (retry in %s): %v",
					m.externalPort, m.internalClient, m.internalPort, delay, err)
			} else {
				if failures > 0 {
					log.Printf("[P2P][UPnP] UDP mapping %d->%s:%d refresh recovered",
						m.externalPort, m.internalClient, m.internalPort)
				}
				failures = 0
				delay = mappingRefreshInterval(m.leaseSeconds)
			}
			timer.Reset(delay)
		}
	}
}

func discoverServicesCached(ctx context.Context) ([]service, string, bool, error) {
	key := localNetworkCacheKey()
	if key != "" {
		serviceCache.Lock()
		if serviceCache.networkKey == key && time.Now().Before(serviceCache.expiresAt) && len(serviceCache.services) > 0 {
			services := append([]service(nil), serviceCache.services...)
			serviceCache.Unlock()
			return services, key, true, nil
		}
		serviceCache.Unlock()
	}

	services, err := discoverServices(ctx)
	if err != nil {
		return nil, key, false, err
	}
	if key != "" {
		storeServicesInCache(key, services)
	}
	return services, key, false, nil
}

func storeServicesInCache(key string, services []service) {
	if key == "" || len(services) == 0 {
		return
	}
	serviceCache.Lock()
	serviceCache.networkKey = key
	serviceCache.expiresAt = time.Now().Add(serviceCacheTTL)
	serviceCache.services = append([]service(nil), services...)
	serviceCache.Unlock()
}

func invalidateServiceCache(key string) {
	if key == "" {
		return
	}
	serviceCache.Lock()
	if serviceCache.networkKey == key {
		serviceCache.networkKey = ""
		serviceCache.expiresAt = time.Time{}
		serviceCache.services = nil
	}
	serviceCache.Unlock()
}

func localNetworkCacheKey() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	values := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, raw := range addrs {
			ipText := raw.String()
			if slash := strings.IndexByte(ipText, '/'); slash >= 0 {
				ipText = ipText[:slash]
			}
			ip, err := netip.ParseAddr(ipText)
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				values = append(values, strconv.Itoa(iface.Index)+"="+ip.String())
			}
		}
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func discoverServices(ctx context.Context) ([]service, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(discoveryWindow)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	remote, err := net.ResolveUDPAddr("udp4", ssdpAddress)
	if err != nil {
		return nil, err
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

	locations := map[string]struct{}{}
	buffer := make([]byte, 64*1024)
	for {
		n, _, readErr := conn.ReadFromUDP(buffer)
		if readErr != nil {
			if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			break
		}
		if location := ssdpHeader(string(buffer[:n]), "location"); location != "" {
			locations[location] = struct{}{}
		}
	}
	if len(locations) == 0 {
		return nil, ErrUnavailable
	}

	var result []service
	var lastErr error
	for raw := range locations {
		found, err := fetchServices(ctx, raw)
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
	sort.SliceStable(result, func(i, j int) bool {
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

func fetchServices(ctx context.Context, rawLocation string) ([]service, error) {
	location, err := url.Parse(strings.TrimSpace(rawLocation))
	if err != nil || location.Scheme != "http" || location.Hostname() == "" {
		return nil, errors.New("invalid UPnP device description URL")
	}
	locationIPs, err := resolveLocalGatewayHost(ctx, location.Hostname())
	if err != nil {
		return nil, err
	}
	resp, _, err := doPinnedRequest(ctx, http.MethodGet, location, locationIPs, "", nil)
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
		if candidate, parseErr := url.Parse(strings.TrimSpace(root.URLBase)); parseErr == nil {
			candidate = location.ResolveReference(candidate)
			if candidate.Scheme == "http" && candidate.Hostname() != "" {
				if candidateIPs, resolveErr := resolveLocalGatewayHost(ctx, candidate.Hostname()); resolveErr == nil {
					if _, ok := commonLocalAddress(locationIPs, candidateIPs); ok {
						base = candidate
					}
				}
			}
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
		if control.Scheme != "http" || control.Hostname() == "" {
			continue
		}
		controlIPs, resolveErr := resolveLocalGatewayHost(ctx, control.Hostname())
		if resolveErr != nil {
			continue
		}
		controlIP, ok := commonLocalAddress(locationIPs, controlIPs)
		if !ok {
			continue
		}
		key := item.ServiceType + "|" + control.String() + "|" + controlIP.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, service{
			serviceType: strings.TrimSpace(item.ServiceType),
			controlURL:  control,
			controlIP:   controlIP,
		})
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
	switch {
	case strings.HasSuffix(serviceType, ":WANIPConnection:2"):
		return 30
	case strings.HasSuffix(serviceType, ":WANIPConnection:1"):
		return 20
	case strings.HasSuffix(serviceType, ":WANPPPConnection:1"):
		return 10
	default:
		return 0
	}
}

func resolveLocalGatewayHost(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.Is4() && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
			return []netip.Addr{ip}, nil
		}
		return nil, errors.New("UPnP endpoint is not on a local gateway address")
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	result := make([]netip.Addr, 0, len(addrs))
	seen := map[netip.Addr]struct{}{}
	for _, ip := range addrs {
		ip = ip.Unmap()
		if !ip.Is4() || (!ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		result = append(result, ip)
	}
	if len(result) == 0 {
		return nil, errors.New("UPnP endpoint hostname did not resolve to a local gateway address")
	}
	return result, nil
}

func commonLocalAddress(a, b []netip.Addr) (netip.Addr, bool) {
	set := make(map[netip.Addr]struct{}, len(a))
	for _, ip := range a {
		set[ip.Unmap()] = struct{}{}
	}
	for _, ip := range b {
		ip = ip.Unmap()
		if _, ok := set[ip]; ok {
			return ip, true
		}
	}
	return netip.Addr{}, false
}

func pinnedHTTPClient(target *url.URL, pinnedIP netip.Addr) *http.Client {
	port := target.Port()
	if port == "" {
		port = "80"
	}
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{}
			return dialer.DialContext(ctx, "tcp4", net.JoinHostPort(pinnedIP.String(), port))
		},
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("UPnP HTTP redirects are disabled")
		},
	}
}

func doPinnedRequest(
	ctx context.Context,
	method string,
	target *url.URL,
	pinnedIPs []netip.Addr,
	body string,
	headers map[string]string,
) (*http.Response, netip.Addr, error) {
	if target == nil || target.Scheme != "http" || target.Hostname() == "" || len(pinnedIPs) == 0 {
		return nil, netip.Addr{}, errors.New("invalid pinned UPnP HTTP target")
	}
	var lastErr error
	for _, pinnedIP := range pinnedIPs {
		req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(body))
		if err != nil {
			return nil, netip.Addr{}, err
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := pinnedHTTPClient(target, pinnedIP).Do(req)
		if err == nil {
			return resp, pinnedIP, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, netip.Addr{}, ctx.Err()
		}
	}
	if lastErr != nil {
		return nil, netip.Addr{}, lastErr
	}
	return nil, netip.Addr{}, errors.New("UPnP HTTP request failed")
}

func localIPv4For(ctx context.Context, svc service) (netip.Addr, error) {
	if svc.controlURL == nil || !svc.controlIP.IsValid() || !svc.controlIP.Is4() {
		return netip.Addr{}, errors.New("missing UPnP control endpoint")
	}
	port := svc.controlURL.Port()
	if port == "" {
		port = "80"
	}
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "udp4", net.JoinHostPort(svc.controlIP.String(), port))
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
	if !ip.Is4() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return netip.Addr{}, errors.New("invalid UPnP external IPv4 address")
	}
	return ip, nil
}

func (s service) addAvailableUDPMapping(ctx context.Context, internalClient string, internalPort uint16) (uint16, uint32, error) {
	ports := []uint16{internalPort}
	for len(ports) < 8 {
		var raw [2]byte
		if _, err := rand.Read(raw[:]); err != nil {
			break
		}
		port := uint16(1024 + int(binary.BigEndian.Uint16(raw[:]))%(65535-1024+1))
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
			if permanentErr := s.addPortMapping(ctx, externalPort, internalPort, internalClient, 0); permanentErr == nil {
				return externalPort, 0, nil
			} else {
				return 0, 0, permanentErr
			}
		}
		if code != 718 {
			return 0, 0, err
		}
	}
	return 0, 0, errors.New("UPnP router has no available external UDP port")
}

func (s service) addPortMapping(ctx context.Context, externalPort, internalPort uint16, internalClient string, lease uint32) error {
	args := []soapArgument{
		{name: "NewRemoteHost", value: ""},
		{name: "NewExternalPort", value: strconv.Itoa(int(externalPort))},
		{name: "NewProtocol", value: "UDP"},
		{name: "NewInternalPort", value: strconv.Itoa(int(internalPort))},
		{name: "NewInternalClient", value: internalClient},
		{name: "NewEnabled", value: "1"},
		{name: "NewPortMappingDescription", value: "RelayProxy P2P"},
		{name: "NewLeaseDuration", value: strconv.FormatUint(uint64(lease), 10)},
	}
	_, err := s.soap(ctx, "AddPortMapping", args)
	return err
}

func (s service) deletePortMapping(ctx context.Context, externalPort uint16) error {
	args := []soapArgument{
		{name: "NewRemoteHost", value: ""},
		{name: "NewExternalPort", value: strconv.Itoa(int(externalPort))},
		{name: "NewProtocol", value: "UDP"},
	}
	_, err := s.soap(ctx, "DeletePortMapping", args)
	return err
}

func (s service) soap(ctx context.Context, action string, args []soapArgument) ([]byte, error) {
	if s.controlURL == nil || s.serviceType == "" || !s.controlIP.IsValid() || !s.controlIP.Is4() {
		return nil, errors.New("invalid UPnP service")
	}
	var payload strings.Builder
	payload.WriteString(`<?xml version="1.0"?>`)
	payload.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:`)
	payload.WriteString(action)
	payload.WriteString(` xmlns:u="`)
	payload.WriteString(xmlEscape(s.serviceType))
	payload.WriteString(`">`)
	for _, arg := range args {
		payload.WriteString("<")
		payload.WriteString(arg.name)
		payload.WriteString(">")
		payload.WriteString(xmlEscape(arg.value))
		payload.WriteString("</")
		payload.WriteString(arg.name)
		payload.WriteString(">")
	}
	payload.WriteString(`</u:`)
	payload.WriteString(action)
	payload.WriteString(`></s:Body></s:Envelope>`)

	headers := map[string]string{
		"Content-Type": `text/xml; charset="utf-8"`,
		"SOAPAction":   `"` + s.serviceType + "#" + action + `"`,
		"Connection":   "close",
	}
	resp, _, err := doPinnedRequest(ctx, http.MethodPost, s.controlURL, []netip.Addr{s.controlIP}, payload.String(), headers)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSOAPBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code, _ := strconv.Atoi(xmlElementText(body, "errorCode"))
		return nil, &soapFault{Status: resp.StatusCode, Code: code, Description: xmlElementText(body, "errorDescription")}
	}
	return body, nil
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

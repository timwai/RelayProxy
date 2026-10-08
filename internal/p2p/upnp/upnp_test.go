package upnp

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSDPHeader(t *testing.T) {
	packet := "HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=120\r\nLOCATION: http://192.168.1.1/igd.xml\r\n\r\n"
	if got := ssdpHeader(packet, "location"); got != "http://192.168.1.1/igd.xml" {
		t.Fatalf("location=%q", got)
	}
}

func TestServiceRankPrefersWANIPV2(t *testing.T) {
	cases := map[string]int{
		"urn:schemas-upnp-org:service:WANIPConnection:2":  30,
		"urn:schemas-upnp-org:service:WANIPConnection:1":  20,
		"urn:schemas-upnp-org:service:WANPPPConnection:1": 10,
		"urn:schemas-upnp-org:service:Layer3Forwarding:1": 0,
		"evil:WANIPConnection:2":                          0,
	}
	for serviceType, want := range cases {
		if got := serviceRank(serviceType); got != want {
			t.Fatalf("serviceRank(%q)=%d, want %d", serviceType, got, want)
		}
	}
}

func TestSOAPMappingActions(t *testing.T) {
	var actions []string
	var bodies []string
	var parseErrors []error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), "\"")
		actions = append(actions, action)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		var document struct{}
		if err := xml.Unmarshal(body, &document); err != nil {
			parseErrors = append(parseErrors, err)
		}
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.HasSuffix(action, "#GetExternalIPAddress"):
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewExternalIPAddress>8.8.8.8</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
		default:
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		}
	}))
	defer server.Close()
	control, err := url.Parse(server.URL + "/control")
	if err != nil {
		t.Fatal(err)
	}
	svc := service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2", controlURL: control}
	ctx := context.Background()

	ip, err := svc.externalIPAddress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ip != netip.MustParseAddr("8.8.8.8") {
		t.Fatalf("external IP=%s", ip)
	}
	if err := svc.addPortMapping(ctx, 32123, 32123, "192.168.1.20", 3600); err != nil {
		t.Fatal(err)
	}
	if err := svc.deletePortMapping(ctx, 32123); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 3 {
		t.Fatalf("actions=%v", actions)
	}
	if len(parseErrors) != 0 {
		t.Fatalf("invalid SOAP XML: %v", parseErrors)
	}
	if !strings.Contains(bodies[1], "<NewProtocol>UDP</NewProtocol>") ||
		!strings.Contains(bodies[1], "<NewInternalClient>192.168.1.20</NewInternalClient>") ||
		!strings.Contains(bodies[1], "<NewExternalPort>32123</NewExternalPort>") {
		t.Fatalf("unexpected AddPortMapping body: %s", bodies[1])
	}
}

func TestSOAPFaultCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	svc := service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: control}
	err := svc.addPortMapping(context.Background(), 30000, 30000, "192.168.1.10", 3600)
	if err == nil {
		t.Fatal("expected SOAP fault")
	}
	if got := soapErrorCode(err); got != 718 {
		t.Fatalf("SOAP error code=%d, want 718: %v", got, err)
	}
}

func TestNonPublicWANAddressesAreRejected(t *testing.T) {
	for _, raw := range []string{
		"192.168.1.1", "10.1.2.3", "100.64.5.6", "100.127.255.254",
		"169.254.10.1", "198.18.0.1", "198.51.100.12", "203.0.113.50",
		"127.0.0.1", "224.0.0.1",
	} {
		if isPublicWANIPv4(netip.MustParseAddr(raw)) {
			t.Fatalf("non-public WAN address was accepted: %s", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1"} {
		if !isPublicWANIPv4(netip.MustParseAddr(raw)) {
			t.Fatalf("public WAN address was rejected: %s", raw)
		}
	}
}

func TestUPnPPermanentMappingIsRefused(t *testing.T) {
	var permanentAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "<NewLeaseDuration>0</NewLeaseDuration>") {
			permanentAttempts.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>725</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	svc := service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: control}
	_, _, err := svc.addAvailableUDPMapping(context.Background(), "192.168.1.10", 34000)
	if !errors.Is(err, ErrPermanentLease) {
		t.Fatalf("expected permanent lease refusal: %v", err)
	}
	if permanentAttempts.Load() != 0 {
		t.Fatal("unsafe permanent mapping was attempted")
	}
}

func TestUPnPHTTPDisablesProxyAndRedirects(t *testing.T) {
	var redirected atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	gatewayURL, _ := url.Parse(server.URL)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	client := gatewayHTTPClient("router.example.test", netip.MustParseAddr("127.0.0.1"))
	resp, err := client.Get("http://router.example.test:" + gatewayURL.Port() + "/description")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || redirected.Load() {
		t.Fatalf("redirect was not rejected: err=%v visited=%v", err, redirected.Load())
	}
}

func TestMappingCloseWaitsForRefreshAndDoesNotReopen(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), "\"")
		mu.Lock()
		actions = append(actions, action)
		mu.Unlock()
		switch {
		case strings.HasSuffix(action, "#AddPortMapping"):
			close(started)
			<-release
		case strings.HasSuffix(action, "#GetSpecificPortMappingEntry"):
			_, _ = io.WriteString(w, "<root><NewInternalClient>192.168.1.10</NewInternalClient><NewInternalPort>34000</NewInternalPort></root>")
		case strings.HasSuffix(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, "<root><NewExternalIPAddress>8.8.8.8</NewExternalIPAddress></root>")
		default:
			_, _ = io.WriteString(w, "<ok/>")
		}
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	m := &Mapping{
		service:        service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: control},
		internalClient: "192.168.1.10", internalPort: 34000, externalPort: 34000,
		externalIP: netip.MustParseAddr("8.8.8.8"), leaseSeconds: 3600,
		done: make(chan struct{}), updates: make(chan MappingUpdate, 1),
	}
	refreshed := make(chan struct{})
	go func() {
		_ = m.refresh(context.Background())
		close(refreshed)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("mapping refresh never started")
	}
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close did not wait for refresh: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-refreshed
	if err := <-closed; err != nil {
		t.Fatalf("mapping cleanup failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 4 || !strings.HasSuffix(actions[len(actions)-1], "#DeletePortMapping") {
		t.Fatalf("mapping requests incorrectly ordered: %v", actions)
	}
	if err := m.refresh(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed mapping was refreshed: %v", err)
	}
}

func TestCloseRejectsChangedMappingOwner(t *testing.T) {
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), "\"")
		if strings.HasSuffix(action, "#GetSpecificPortMappingEntry") {
			_, _ = io.WriteString(w, "<root><NewInternalClient>192.168.1.99</NewInternalClient><NewInternalPort>34567</NewInternalPort></root>")
			return
		}
		deletes.Add(1)
		_, _ = io.WriteString(w, "<ok/>")
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	m := &Mapping{
		service:        service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: control},
		internalClient: "192.168.1.10", internalPort: 34000, externalPort: 34000,
		done: make(chan struct{}),
	}
	if err := m.Close(); err == nil {
		t.Fatal("expected a changed mapping owner to prevent deletion")
	}
	if deletes.Load() != 0 {
		t.Fatal("Close deleted another device's port mapping")
	}
}

func TestSOAPFaultReturnedWithHTTP200IsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	svc := service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1", controlURL: control}
	err := svc.addPortMapping(context.Background(), 34567, 34567, "192.168.1.10", 3600)
	if got := soapErrorCode(err); got != 718 {
		t.Fatalf("HTTP 200 SOAP Fault was not rejected: code=%d err=%v", got, err)
	}
}

func TestGatewayDiscoveryStaysOnReceivingIPv4Interface(t *testing.T) {
	primary := networkInterfaceIPv4{
		address: netip.MustParseAddr("192.168.10.30"),
		subnet:  &net.IPNet{IP: net.ParseIP("192.168.10.30"), Mask: net.CIDRMask(24, 32)},
	}
	secondary := networkInterfaceIPv4{
		address: netip.MustParseAddr("10.12.5.30"),
		subnet:  &net.IPNet{IP: net.ParseIP("10.12.5.30"), Mask: net.CIDRMask(24, 32)},
	}
	gateway := netip.MustParseAddr("192.168.10.1")
	if !onInterfaceSubnet(gateway, primary) {
		t.Fatal("local gateway was incorrectly rejected")
	}
	if onInterfaceSubnet(gateway, secondary) {
		t.Fatal("SSDP gateway from another interface was accepted")
	}
	if onInterfaceSubnet(primary.address, primary) {
		t.Fatal("own network interface should not be accepted as the gateway")
	}
	if onInterfaceSubnet(netip.MustParseAddr("8.8.8.8"), primary) {
		t.Fatal("unrelated public endpoint was accepted as the gateway")
	}
}

func TestUPnPClientPinsSourceAndRemoteIPv4(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<ok/>")
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := gatewayHTTPClient("gateway.example.test", netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.1"))
	response, err := client.Get("http://gateway.example.test:" + serverURL.Port() + "/")
	if err != nil {
		t.Fatalf("pinned gateway request failed: %v", err)
	}
	_ = response.Body.Close()
	if _, err := client.Get("http://different-gateway.example.test:" + serverURL.Port() + "/"); err == nil {
		t.Fatal("UPnP HTTP client accepted a different gateway host")
	}
}

func TestK2PRouterMappingWithPrivateWANStillSendsAddPortMapping(t *testing.T) {
	var mu sync.Mutex
	var actions []string
	var addBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), "\"")
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		actions = append(actions, action)
		if strings.HasSuffix(action, "#AddPortMapping") {
			addBody = string(body)
		}
		mu.Unlock()
		switch {
		case strings.HasSuffix(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, "<root><NewExternalIPAddress>100.64.10.7</NewExternalIPAddress></root>")
		case strings.HasSuffix(action, "#GetSpecificPortMappingEntry"):
			_, _ = io.WriteString(w, "<root><NewInternalClient>192.168.31.8</NewInternalClient><NewInternalPort>20900</NewInternalPort></root>")
		default:
			_, _ = io.WriteString(w, "<ok/>")
		}
	}))
	defer server.Close()
	control, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		controlURL: control, localIP: netip.MustParseAddr("192.168.31.8"),
	}
	mapping, address, err := mapUDPOnService(context.Background(), svc, 20900, 20900, 20999)
	if err != nil {
		t.Fatalf("K2P UPnP mapping was incorrectly blocked by CGNAT: %v", err)
	}
	if address.String() != "100.64.10.7:20900" || IsPublicWANIPv4(address.Addr()) {
		t.Fatalf("CGNAT WAN address was incorrectly advertised: %s", address)
	}
	if err := mapping.refresh(context.Background()); err != nil {
		t.Fatalf("K2P mapping could not renew behind CGNAT: %v", err)
	}
	if err := mapping.Close(); err != nil {
		t.Fatalf("K2P router mapping cleanup failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(addBody, "<NewInternalClient>192.168.31.8</NewInternalClient>") ||
		!strings.Contains(addBody, "<NewInternalPort>20900</NewInternalPort>") ||
		!strings.Contains(addBody, "<NewExternalPort>20900</NewExternalPort>") {
		t.Fatalf("K2P was not sent the expected UPnP SOAP AddPortMapping request: %s", addBody)
	}
	var adds, deletes int
	for _, action := range actions {
		if strings.HasSuffix(action, "#AddPortMapping") {
			adds++
		}
		if strings.HasSuffix(action, "#DeletePortMapping") {
			deletes++
		}
	}
	if adds != 2 || deletes != 1 {
		t.Fatalf("incorrect K2P mapping lifecycle: add=%d delete=%d actions=%v", adds, deletes, actions)
	}
}

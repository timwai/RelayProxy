package upnp

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
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
		"urn:schemas-upnp-org:service:WANIPConnection:2": 30,
		"urn:schemas-upnp-org:service:WANIPConnection:1": 20,
		"urn:schemas-upnp-org:service:WANPPPConnection:1": 10,
		"urn:schemas-upnp-org:service:Layer3Forwarding:1": 0,
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
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewExternalIPAddress>198.51.100.25</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
		default:
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		}
	}))
	defer server.Close()
	control, err := url.Parse(server.URL + "/control")
	if err != nil {
		t.Fatal(err)
	}
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2",
		controlURL:  control,
		controlIP:   netip.MustParseAddr("127.0.0.1"),
	}
	ctx := context.Background()

	ip, err := svc.externalIPAddress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ip != netip.MustParseAddr("198.51.100.25") {
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
	assertElementOrder(t, bodies[1], []string{
		"NewRemoteHost", "NewExternalPort", "NewProtocol", "NewInternalPort",
		"NewInternalClient", "NewEnabled", "NewPortMappingDescription", "NewLeaseDuration",
	})
	assertElementOrder(t, bodies[2], []string{"NewRemoteHost", "NewExternalPort", "NewProtocol"})
}

func TestSOAPFaultCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`)
	}))
	defer server.Close()
	control, _ := url.Parse(server.URL)
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		controlURL:  control,
		controlIP:   netip.MustParseAddr("127.0.0.1"),
	}
	err := svc.addPortMapping(context.Background(), 30000, 30000, "192.168.1.10", 3600)
	if err == nil {
		t.Fatal("expected SOAP fault")
	}
	if got := soapErrorCode(err); got != 718 {
		t.Fatalf("SOAP error code=%d, want 718: %v", got, err)
	}
}


func assertElementOrder(t *testing.T, body string, names []string) {
	t.Helper()
	last := -1
	for _, name := range names {
		index := strings.Index(body, "<"+name+">")
		if index < 0 {
			t.Fatalf("missing <%s> in %s", name, body)
		}
		if index <= last {
			t.Fatalf("SOAP argument %s is out of order in %s", name, body)
		}
		last = index
	}
}

func TestCommonLocalAddressAllowsDifferentNamesForSameRouterIP(t *testing.T) {
	locationIPs := []netip.Addr{netip.MustParseAddr("192.168.1.1")}
	controlIPs := []netip.Addr{netip.MustParseAddr("192.168.1.1"), netip.MustParseAddr("192.168.1.2")}
	got, ok := commonLocalAddress(locationIPs, controlIPs)
	if !ok || got != netip.MustParseAddr("192.168.1.1") {
		t.Fatalf("commonLocalAddress=%s,%t", got, ok)
	}
}

func TestMappingRefreshRetryDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 5 * time.Second},
		{2, 15 * time.Second},
		{3, 30 * time.Second},
		{4, time.Minute},
		{20, time.Minute},
	}
	for _, tc := range cases {
		if got := mappingRefreshRetryDelay(tc.failures); got != tc.want {
			t.Fatalf("mappingRefreshRetryDelay(%d)=%s, want %s", tc.failures, got, tc.want)
		}
	}
}

func TestSOAPRedirectIsRejected(t *testing.T) {
	targetHits := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case targetHits <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	control, err := url.Parse(redirect.URL + "/control")
	if err != nil {
		t.Fatal(err)
	}
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2",
		controlURL:  control,
		controlIP:   netip.MustParseAddr("127.0.0.1"),
	}
	if _, err := svc.externalIPAddress(context.Background()); err == nil {
		t.Fatal("SOAP redirect unexpectedly succeeded")
	}
	select {
	case <-targetHits:
		t.Fatal("SOAP client followed redirect to another endpoint")
	default:
	}
}

func TestMappingCloseCancelsRefreshBeforeDelete(t *testing.T) {
	events := make(chan string, 4)
	addStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), """)
		switch {
		case strings.HasSuffix(action, "#AddPortMapping"):
			events <- "add-start"
			close(addStarted)
			<-r.Context().Done()
			events <- "add-cancel"
		case strings.HasSuffix(action, "#DeletePortMapping"):
			events <- "delete"
			fmt.Fprint(w, `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		}
	}))
	defer server.Close()

	control, err := url.Parse(server.URL + "/control")
	if err != nil {
		t.Fatal(err)
	}
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2",
		controlURL:  control,
		controlIP:   netip.MustParseAddr("127.0.0.1"),
	}
	refreshCtx, refreshCancel := context.WithCancel(context.Background())
	m := &Mapping{
		service:         svc,
		internalClient:  "192.168.1.20",
		internalPort:    32123,
		externalPort:    32123,
		leaseSeconds:    3600,
		refreshCancel:   refreshCancel,
	}
	m.refreshWG.Add(1)
	go func() {
		defer m.refreshWG.Done()
		_ = svc.addPortMapping(refreshCtx, 32123, 32123, "192.168.1.20", 3600)
	}()

	select {
	case <-addStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh request did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel and wait for refresh")
	}

	want := []string{"add-start", "add-cancel", "delete"}
	for i, expected := range want {
		select {
		case got := <-events:
			if got != expected {
				t.Fatalf("event %d=%q, want %q", i, got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing event %q", expected)
		}
	}
}

func TestServiceCacheStoreAndInvalidate(t *testing.T) {
	control, _ := url.Parse("http://192.168.1.1/control")
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		controlURL:  control,
		controlIP:   netip.MustParseAddr("192.168.1.1"),
	}
	storeServicesInCache("network-a", []service{svc})
	serviceCache.Lock()
	if serviceCache.networkKey != "network-a" || len(serviceCache.services) != 1 {
		serviceCache.Unlock()
		t.Fatal("UPnP service cache was not populated")
	}
	serviceCache.Unlock()

	invalidateServiceCache("network-b")
	serviceCache.Lock()
	if serviceCache.networkKey != "network-a" {
		serviceCache.Unlock()
		t.Fatal("unrelated network invalidated cache")
	}
	serviceCache.Unlock()

	invalidateServiceCache("network-a")
	serviceCache.Lock()
	defer serviceCache.Unlock()
	if serviceCache.networkKey != "" || len(serviceCache.services) != 0 {
		t.Fatal("UPnP service cache was not invalidated")
	}
}

package upnp

import (
	"context"
	"encoding/xml"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
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

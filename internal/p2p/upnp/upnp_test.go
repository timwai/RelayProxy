package upnp

import (
	"context"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := strings.Trim(r.Header.Get("SOAPAction"), """)
		actions = append(actions, action)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
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
	svc := service{serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2", controlURL: control}
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

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
	"sync"
	"testing"
)

func TestTCPFileMappingUsesSeparateProtocolForLeaseAndOwnedCleanup(t *testing.T) {
	var mu sync.Mutex
	actions := []string{}
	bodies := []string{}
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		actions = append(actions, r.Header.Get("SOAPAction"))
		bodies = append(bodies, string(data))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		switch {
		case strings.Contains(r.Header.Get("SOAPAction"), "GetExternalIPAddress"):
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewExternalIPAddress>8.8.8.8</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`)
		case strings.Contains(r.Header.Get("SOAPAction"), "GetSpecificPortMappingEntry"):
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:GetSpecificPortMappingEntryResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:2"><NewInternalClient>192.168.1.10</NewInternalClient><NewInternalPort>32450</NewInternalPort></u:GetSpecificPortMappingEntryResponse></s:Body></s:Envelope>`)
		default:
			fmt.Fprint(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body/></s:Envelope>`)
		}
	}))
	defer router.Close()
	control, err := url.Parse(router.URL + "/igdupnp/control")
	if err != nil {
		t.Fatal(err)
	}
	svc := service{
		serviceType: "urn:schemas-upnp-org:service:WANIPConnection:2",
		controlURL:  control,
		localIP:     netip.MustParseAddr("192.168.1.10"),
	}
	ctx := context.Background()
	mapping, address, err := mapPortOnService(ctx, svc, 32450, 0, 0, "TCP")
	if err != nil {
		t.Fatalf("TCP UPnP mapping: %v", err)
	}
	if !IsPublicWANIPv4(address.Addr()) || mapping.mappingProtocol() != "TCP" {
		t.Fatalf("invalid WAN TCP candidate: %v (%s)", address, mapping.mappingProtocol())
	}
	if err := mapping.refresh(ctx); err != nil {
		t.Fatalf("TCP lease renewal failed: %v", err)
	}
	if err := mapping.Close(); err != nil {
		t.Fatalf("TCP owned cleanup failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	add, remove, owner := 0, 0, 0
	for i, action := range actions {
		switch {
		case strings.Contains(action, "#AddPortMapping"):
			add++
			if !strings.Contains(bodies[i], "<NewProtocol>TCP</NewProtocol>") {
				t.Fatalf("UDP mapping accidentally opened for file TCP: %s", bodies[i])
			}
		case strings.Contains(action, "#DeletePortMapping"):
			remove++
			if !strings.Contains(bodies[i], "<NewProtocol>TCP</NewProtocol>") {
				t.Fatalf("failed to delete own TCP mapping: %s", bodies[i])
			}
		case strings.Contains(action, "#GetSpecificPortMappingEntry"):
			owner++
			if !strings.Contains(bodies[i], "<NewProtocol>TCP</NewProtocol>") {
				t.Fatalf("TCP mapping ownership was not checked: %s", bodies[i])
			}
		}
	}
	if add != 2 || owner != 1 || remove != 1 {
		t.Fatalf("lease lifecycle: add=%d owner=%d remove=%d, actions=%v", add, owner, remove, actions)
	}
}

func TestTCPMappingProtocolDefaultsToUDPOnlyForLegacyMappings(t *testing.T) {
	m := &Mapping{}
	if m.mappingProtocol() != "UDP" {
		t.Fatal("legacy P2P mapping protocol changed")
	}
	m.protocol = "TCP"
	if m.mappingProtocol() != "TCP" {
		t.Fatal("file-sharing TCP protocol not selected")
	}
}

func TestMapTCPRejectsInvalidPortWithoutDiscovery(t *testing.T) {
	for _, p := range []int{-1, 0, 65536} {
		if _, _, err := MapTCP(context.Background(), p); err == nil {
			t.Fatalf("accepted invalid TCP listener port %d", p)
		}
	}
}

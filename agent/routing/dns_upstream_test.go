package routing

import "testing"

func TestDNSUpstreamsValidation(t *testing.T) {
	valid := []DNSUpstream{{URL: "https://dns.example.net/dns-query", BootstrapIP: "10.0.0.53"}}
	if err := ValidateDNSUpstreams(valid); err != nil {
		t.Fatalf("a private DoH server reached through selected exit should be valid: %v", err)
	}
	for _, tt := range []struct {
		name string
		url  string
		ip   string
	}{
		{"insecure HTTP", "http://dns.example.net/dns-query", "10.0.0.53"},
		{"missing pinned bootstrap IP", "https://dns.example.net/dns-query", ""},
		{"bootstrap hostname would need DNS", "https://dns.example.net/dns-query", "dns.example.net"},
		{"loopback bootstrap", "https://dns.example.net/dns-query", "127.0.0.1"},
		{"linklocal", "https://dns.example.net/dns-query", "169.254.1.1"},
		{"bare IP certificate host", "https://1.1.1.1/dns-query", "1.1.1.1"},
		{"user info", "https://user:password@dns.example.net/dns-query", "10.0.0.53"},
		{"custom port", "https://dns.example.net:8443/dns-query", "10.0.0.53"},
		{"URL query injection", "https://dns.example.net/dns-query?x=y", "10.0.0.53"},
		{"missing path", "https://dns.example.net", "10.0.0.53"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateDNSUpstreams([]DNSUpstream{{URL: tt.url, BootstrapIP: tt.ip}}); err == nil {
				t.Fatal("invalid DoH upstream was accepted")
			}
		})
	}
	if err := ValidateDNSUpstreams(make([]DNSUpstream, 9)); err == nil {
		t.Fatal("unbounded custom DNS upstream list was accepted")
	}
}

func TestDNSUpstreamConfigCloneIsIndependent(t *testing.T) {
	original := Config{DNSUpstreams: []DNSUpstream{{URL: "https://dns.example.net/dns-query", BootstrapIP: "10.0.0.53"}}}
	cloned := CloneConfig(original)
	cloned.DNSUpstreams[0].BootstrapIP = "10.0.0.54"
	if original.DNSUpstreams[0].BootstrapIP != "10.0.0.53" {
		t.Fatal("published routing config leaked a mutable DNS list")
	}
	engine, err := NewEngine(original)
	if err != nil {
		t.Fatal(err)
	}
	got := engine.DNSUpstreams()
	got[0].BootstrapIP = "192.0.2.10"
	if engine.DNSUpstreams()[0].BootstrapIP != "10.0.0.53" {
		t.Fatal("engine DNS upstream accessor did not return a defensive copy")
	}
}

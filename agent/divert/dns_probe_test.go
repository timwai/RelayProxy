package divert

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSProbeUsesActiveExitAndReportsPerResolverFailures(t *testing.T) {
	var ips []string
	var ports []uint16
	var exits []string
	s := newTestServer(t, Options{
		Config:     Config{DefaultAction: ActionProxy},
		ProxyReady: func() bool { return true },
		DNSExitID:  func() string { return "overseas-dns" },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			exits = append(exits, exit)
			ips = append(ips, host)
			ports = append(ports, port)
			return nil, errors.New("simulated selected exit failure")
		}},
	})
	report, err := s.ProbeDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ExitID != "overseas-dns" || report.Custom {
		t.Fatalf("unexpected DNS probe exit: %+v", report)
	}
	if want := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "9.9.9.9"}; !slices.Equal(ips, want) {
		t.Fatalf("DNS probe did not use all pinned resolvers: %v", ips)
	}
	if !slices.Equal(ports, []uint16{443, 443, 443, 853}) {
		t.Fatalf("unexpected DNS probe ports: %v", ports)
	}
	if len(report.Results) != 4 {
		t.Fatalf("expected one result per upstream, got %d", len(report.Results))
	}
	for i, result := range report.Results {
		if result.OK || result.Error == "" || result.Address != ips[i] || result.LatencyMS < 0 {
			t.Fatalf("probe %d lost failure diagnostics: %+v", i, result)
		}
		if exits[i] != "overseas-dns" {
			t.Fatalf("probe %d escaped DNS exit: %q", i, exits[i])
		}
	}
}

func TestDNSProbeCustomResolversNeverFallBackToPublic(t *testing.T) {
	var ips []string
	s := newTestServer(t, Options{
		Config:      Config{DefaultAction: ActionProxy},
		ProxyReady:  func() bool { return true },
		DNSUpstreams: func() []DNSUpstream {
			return []DNSUpstream{{URL: "https://resolver.private.example/dns-query", BootstrapIP: "10.0.0.53"}}
		},
		Dialer: &testDialer{tcp: func(_ context.Context, _, host string, _ uint16) (net.Conn, error) {
			ips = append(ips, host)
			return nil, errors.New("test private DNS unavailable")
		}},
	})
	report, err := s.ProbeDNS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Custom || len(report.Results) != 1 ||
		report.Results[0].Name != "resolver.private.example" ||
		!slices.Equal(ips, []string{"10.0.0.53"}) {
		t.Fatalf("custom probe leaked to built-in resolver: %+v ips=%v", report, ips)
	}
}

func TestDNSProbeCancelledContextSkipsFallback(t *testing.T) {
	called := false
	s := newTestServer(t, Options{
		Config: Config{DefaultAction: ActionProxy},
		Dialer: &testDialer{tcp: func(_ context.Context, _, _ string, _ uint16) (net.Conn, error) {
			called = true
			return nil, errors.New("should not dial")
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ProbeDNS(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("cancelled DNS probe initiated an upstream dial")
	}
}

func TestDNSProbeResponseCodeLabels(t *testing.T) {
	if got := dnsmessage.RCodeSuccess.String(); got == "" {
		t.Fatal("success DNS RCode string is empty")
	}
}

package routing

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

type dnsModeTunnel struct {
	hostTCP string
	hostUDP string
	exitID  string
	err     error
}

func (s *dnsModeTunnel) DialTCP(_ context.Context, exit, host string, _ uint16) (net.Conn, error) {
	s.exitID, s.hostTCP = exit, host
	return nil, s.err
}
func (s *dnsModeTunnel) DialUDP(_ context.Context, exit, host string, _ uint16) (net.PacketConn, error) {
	s.exitID, s.hostUDP = exit, host
	return nil, s.err
}

func TestProxyDNSModePreservesHostnameForTCPAndUDP(t *testing.T) {
	for _, mode := range []DNSMode{"", DNSModeProxy} {
		engine, err := NewEngine(Config{Mode: ModeGlobalProxy, DNSMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("dial probe")
		tunnel := &dnsModeTunnel{err: sentinel}
		d := NewRoutingDialer(engine, tunnel)
		if _, err := d.DialTCP(context.Background(), "exit1", "play.google.com", 443); !errors.Is(err, sentinel) {
			t.Fatalf("mode %q: TCP error %v", mode, err)
		}
		if _, err := d.DialUDP(context.Background(), "exit1", "play.google.com", 443); !errors.Is(err, sentinel) {
			t.Fatalf("mode %q: UDP error %v", mode, err)
		}
		if tunnel.hostTCP != "play.google.com" || tunnel.hostUDP != "play.google.com" {
			t.Fatalf("proxy DNS mode resolved hostname locally: TCP=%q UDP=%q", tunnel.hostTCP, tunnel.hostUDP)
		}
	}
}

func TestLocalDNSModeResolvesBeforeSendingToExit(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeGlobalProxy, DNSMode: DNSModeLocal})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("dial probe")
	tunnel := &dnsModeTunnel{err: sentinel}
	d := NewRoutingDialer(engine, tunnel)
	for _, selected := range []bool{false, true} {
		var target string
		if selected {
			selectedDialer := SelectedExitDialer{Routing: d}
			if _, err := selectedDialer.DialTCP(context.Background(), "exit1", "localhost", 443); !errors.Is(err, sentinel) {
				t.Fatalf("selected TCP error: %v", err)
			}
			target = tunnel.hostTCP
		} else {
			if _, err := d.DialTCP(context.Background(), "exit1", "localhost", 443); !errors.Is(err, sentinel) {
				t.Fatalf("TCP error: %v", err)
			}
			target = tunnel.hostTCP
		}
		if ip, err := netip.ParseAddr(target); err != nil || !ip.IsLoopback() {
			t.Fatalf("local DNS did not send loopback IP (selected=%v): %q", selected, target)
		}
	}
	if _, err := d.DialUDP(context.Background(), "exit1", "localhost", 443); !errors.Is(err, sentinel) {
		t.Fatalf("UDP error: %v", err)
	}
	if _, err := netip.ParseAddr(tunnel.hostUDP); err != nil {
		t.Fatalf("local DNS forwarded UDP hostname: %q", tunnel.hostUDP)
	}
	// Changing the mode takes effect with the existing routing dialer.
	if err := engine.Reload(Config{Mode: ModeGlobalProxy, DNSMode: DNSModeProxy}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DialTCP(context.Background(), "exit1", "play.google.com", 443); !errors.Is(err, sentinel) || tunnel.hostTCP != "play.google.com" {
		t.Fatalf("hot DNS mode update did not preserve remote hostname: %q %v", tunnel.hostTCP, err)
	}
}

func TestLocalDNSModeNeverFallsBackToProxyResolution(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeGlobalProxy, DNSMode: DNSModeLocal})
	if err != nil {
		t.Fatal(err)
	}
	tunnel := &dnsModeTunnel{err: errors.New("should not dial")}
	d := NewRoutingDialer(engine, tunnel)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.DialTCP(ctx, "exit1", "domain.example", 443); err == nil {
		t.Fatal("local lookup with canceled context succeeded")
	}
	if tunnel.hostTCP != "" {
		t.Fatal("local DNS failure silently sent hostname to exit")
	}
}

func TestDNSModeValidationAndMatchedRuleDisplay(t *testing.T) {
	if err := ValidateConfig(Config{Mode: ModeRule, DNSMode: "automatic"}); err == nil {
		t.Fatal("unsupported DNS mode accepted")
	}
	engine, err := NewEngine(Config{
		Mode: ModeRule, DNSMode: DNSModeProxy, DefaultAction: ActionReject,
		Rules: []Rule{{Name: "", Enabled: true, Processes: []string{"chrome.exe"}, Action: ActionProxy}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := engine.DecideFlow(Flow{Process: "CHROME.EXE", Port: 443, Protocol: "tcp"})
	if !got.Matched || got.Rule != "rule #1" {
		t.Fatalf("unnamed matched rule not identifiable in telemetry: %+v", got)
	}
}

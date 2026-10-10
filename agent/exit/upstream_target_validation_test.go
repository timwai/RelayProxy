package exit

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestUpstreamTargetsRejectRequestLineInjection(t *testing.T) {
	for _, host := range []string{
		"example.com\r\nX-Injected: yes",
		"example.com\nHost: attacker.test",
		"example.com\tattacker",
		"example.com/path",
		"example.com?query",
		"example.com\\host",
		"example.com:443:other",
		"fe80::1%en0",
	} {
		t.Run(strings.ReplaceAll(host, "\n", "_"), func(t *testing.T) {
			if _, err := encodeSOCKS5Address(host, 443); err == nil {
				t.Fatalf("SOCKS5 allowed unsafe target %q", host)
			}
			// CONNECT validation must run before the outbound proxy dial.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := dialHTTPConnect(ctx, UpstreamConfig{
				Mode: UpstreamHTTP, Address: "127.0.0.1:1",
			}, net.JoinHostPort(host, "443"))
			if conn != nil {
				_ = conn.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "invalid upstream target") {
				t.Fatalf("HTTP CONNECT target %q was not rejected locally: %v", host, err)
			}
		})
	}
	for _, host := range []string{"example.org", "_dns._tcp.example.org", "127.0.0.1", "2001:db8::1"} {
		if err := validateUpstreamTargetHost(host); err != nil {
			t.Fatalf("valid target %q rejected: %v", host, err)
		}
		if _, err := encodeSOCKS5Address(host, 853); err != nil {
			t.Fatalf("SOCKS5 rejected valid target %q: %v", host, err)
		}
	}
	ip := netip.MustParseAddr("2001:db8::1")
	if ip.Zone() != "" {
		t.Fatal("test address unexpectedly scoped")
	}
}

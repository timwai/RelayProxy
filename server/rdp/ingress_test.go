package rdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"relayproxy/server/repository"
)

func TestIngressSourceLimiterIsBounded(t *testing.T) {
	limiter := newSourceLimiter(2)
	if !limiter.allow("192.0.2.1") || !limiter.allow("192.0.2.1") || limiter.allow("192.0.2.1") {
		t.Fatal("per-source ingress limit was not enforced")
	}
	endpoint := &ingressEndpoint{item: &repository.RDPIngress{SourceCIDRs: []string{"192.0.2.0/24"}, ExpiresAt: nil}, limiter: newSourceLimiter(10)}
	if !endpoint.allow("192.0.2.1") || endpoint.allow("198.51.100.1") {
		t.Fatal("source CIDR policy was not enforced")
	}
}

func TestIngressManagerDisabledIsExplicit(t *testing.T) {
	manager := NewIngressManager(context.Background(), nil, nil, IngressConfig{})
	if manager.Enabled() {
		t.Fatal("disabled ingress manager reported as enabled")
	}
	if !errors.Is(manager.Reload(), ErrIngressDisabled) {
		t.Fatal("disabled ingress reload did not return ErrIngressDisabled")
	}
	_ = manager.Close()
}

func TestIngressManagerBindsTCPAndUDPAllocation(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	manager := NewIngressManager(context.Background(), nil, nil, IngressConfig{
		Enabled: true,
		Listen:  "127.0.0.1:0",
	})
	defer manager.Close()
	item := &repository.RDPIngress{ID: "rdping_test", ListenPort: port}
	if err := manager.open(item); err != nil {
		t.Fatalf("open allocation failed: %v", err)
	}
	manager.mu.Lock()
	endpoint := manager.endpoints[item.ID]
	manager.mu.Unlock()
	if endpoint == nil || endpoint.tcp == nil || endpoint.udp == nil {
		t.Fatal("allocation did not retain both TCP and UDP listeners")
	}
	if got := endpoint.tcp.Addr().String(); got != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Fatalf("TCP listener=%q, want port %d", got, port)
	}
	if got := endpoint.udp.LocalAddr().(*net.UDPAddr).Port; got != port {
		t.Fatalf("UDP listener port=%d, want %d", got, port)
	}
	status := manager.EndpointStatus(item.ID)
	if !status.TCPListening || !status.UDPListening || status.ActiveUDP != 0 {
		t.Fatalf("unexpected live endpoint status: %+v", status)
	}
}

package app

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/gateway"
	"relayproxy/server/session"
)

func TestAutoQUICRecoveryReauthenticatesAndRestoresRDPUDP(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	reservedUDP, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reservedUDP.Close()
	sessions := session.NewManager()
	router := gateway.NewStreamRouter(sessions, nil, nil, nil)
	router.SetRDPChecker(func(controller, target string) (bool, error) {
		return controller == "controller" && target == "host", nil
	})
	gatewayConfig := gateway.GatewayConfig{
		TCPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		TLSConfig:        &tls.Config{Certificates: []tls.Certificate{certificate}},
		ServerInstanceID: "quic-recovery-test",
		ResolveIdentity: func(shortID string) (gateway.IdentityAuthorization, error) {
			return gateway.IdentityAuthorization{IdentityID: "identity-test", IdentityName: shortID}, nil
		},
		AuthorizeIdentityDevice: func(_ string, hello protocol.DeviceHello, identity gateway.IdentityAuthorization) (gateway.DeviceAuthorization, error) {
			capability := protocol.CapabilityRDPClient
			if hello.DeviceName == "host" {
				capability = protocol.CapabilityRDPHost
			}
			return gateway.DeviceAuthorization{
				State: "approved", DeviceID: hello.DeviceName, IdentityID: identity.IdentityID,
				ApprovedCapabilities: []string{capability},
			}, nil
		},
		RecheckIdentityDevice: func(string, string, string) bool { return true },
	}
	startGateway := func(cfg gateway.GatewayConfig) *gateway.Gateway {
		t.Helper()
		relay := gateway.NewGateway(cfg, sessions, router)
		if err := relay.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = relay.Close() })
		return relay
	}
	relay := startGateway(gatewayConfig)
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, peer, err := target.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = target.WriteToUDP(buffer[:n], peer)
		}
	}()
	disabled := false
	base := AgentConfig{
		IdentityID: "a1b2c3d4e5f6g7h8", Mode: "CLIENT", ServerAddress: "127.0.0.1",
		TCPPort: relay.TCPAddr().(*net.TCPAddr).Port, InsecureTLS: true,
		SOCKS5Enabled: &disabled, HTTPEnabled: &disabled, P2PEnabled: &disabled,
		ConnectTimeout: 3 * time.Second,
	}
	controllerConfig := base
	controllerConfig.DeviceName, controllerConfig.TransportMode = "controller", "quic_only"
	controllerConfig.QUICPort = relay.QUICAddr().(*net.UDPAddr).Port
	controller, err := NewAgent(controllerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	waitAgentReady(t, controller)
	hostConfig := base
	hostConfig.DeviceName, hostConfig.TransportMode = "host", "auto"
	hostConfig.QUICPort = reservedUDP.LocalAddr().(*net.UDPAddr).Port
	hostConfig.RDPAddress = target.LocalAddr().String()
	host, err := NewAgent(hostConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	// Shorten only test scheduling; production callers use the shared 15s default.
	_ = host.tunnelMgr.Close()
	host.tunnelMgr = tunnel.NewTunnelManager(tunnel.ManagerConfig{
		ServerAddress: hostConfig.ServerAddress, TCPPort: hostConfig.TCPPort, QUICPort: hostConfig.QUICPort,
		Mode: tunnel.ModeAuto, TLSConfig: &tls.Config{InsecureSkipVerify: true},
		ConnectTimeout: 500 * time.Millisecond, AutoFallbackDelay: 10 * time.Millisecond,
		AutoQUICRetryInterval: 20 * time.Millisecond,
	}, host.onTunnelStateChange)
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	waitAgentReady(t, host)
	if host.Status().Transport != "tls" {
		t.Fatal("host did not begin with a working TLS fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err = controller.rawDialer.DialRDPUDP(ctx, "host")
	var relayError *protocol.RelayError
	if !errors.As(err, &relayError) || relayError.Code != protocol.ErrCodeDatagramRequired {
		t.Fatalf("TLS target unexpectedly accepted RDP UDP: %v", err)
	}
	// Make QUIC available at the host's configured port without touching either
	// agent. Both gateways share the same authenticated device registry.
	restoredConfig := gatewayConfig
	restoredConfig.TCPAddr = ""
	restoredConfig.QUICAddr = reservedUDP.LocalAddr().String()
	_ = reservedUDP.Close()
	startGateway(restoredConfig)
	deadline := time.Now().Add(6 * time.Second)
	for {
		status := host.Status()
		registered, ok := sessions.Get("host")
		if status.Connected && status.Transport == "quic" && ok && registered.Transport == tunnel.TransportQUIC {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host did not reauthenticate after recovery: status=%+v metrics=%+v", status, host.Diagnostics().Connection)
		}
		time.Sleep(5 * time.Millisecond)
	}
	packet, err := controller.rawDialer.DialRDPUDP(ctx, "host")
	if err != nil {
		t.Fatalf("QUIC recovery did not restore RDP UDP admission: %v", err)
	}
	defer packet.Close()
	_ = packet.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := packet.WriteTo([]byte("RDP UDP recovered"), nil); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	n, _, err := packet.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "RDP UDP recovered" {
		t.Fatalf("RDP UDP round trip after QUIC recovery: payload=%q error=%v", buffer[:n], err)
	}
	if got := host.Diagnostics().Connection.QUICRecoveries; got != 1 {
		t.Fatalf("QUIC recovery count=%d, want 1", got)
	}
}

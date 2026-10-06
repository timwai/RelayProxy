package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"relayproxy/agent/client"
	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
	"relayproxy/server/gateway"
	"relayproxy/server/session"
)

func startRelayPair(t *testing.T, clientMode, exitMode string, checker *acl.Checker) (*Agent, *Agent, *session.Manager) {
	t.Helper()
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	router := gateway.NewStreamRouter(sessions, checker, func(clientID, exitID string) (bool, error) {
		return (clientID == "client" || clientID == "new-client") && exitID == "exit", nil
	}, nil)
	gateway := gateway.NewGateway(gateway.GatewayConfig{
		TCPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
		TLSConfig:        &tls.Config{Certificates: []tls.Certificate{certificate}},
		ServerInstanceID: "transport-test",
		ResolveIdentity: func(shortID string) (gateway.IdentityAuthorization, error) {
			return gateway.IdentityAuthorization{IdentityID: "identity-test", IdentityName: shortID}, nil
		},
		AuthorizeIdentityDevice: func(_ string, hello protocol.DeviceHello, identity gateway.IdentityAuthorization) (gateway.DeviceAuthorization, error) {
			deviceID := "client"
			caps := []string{protocol.CapabilityProxyClient}
			for _, capability := range hello.RequestedCapabilities {
				if capability == protocol.CapabilityProxyExit {
					deviceID, caps = "exit", []string{protocol.CapabilityProxyExit}
				}
			}
			return gateway.DeviceAuthorization{State: "approved", DeviceID: deviceID, IdentityID: identity.IdentityID, ApprovedCapabilities: caps}, nil
		},
		RecheckIdentityDevice: func(string, string, string) bool { return true },
		ListProxyExits: func(clientID, _, _ string) ([]protocol.ProxyExit, error) {
			if clientID != "client" && clientID != "new-client" {
				return []protocol.ProxyExit{}, nil
			}
			return []protocol.ProxyExit{{
				DeviceID:            "exit",
				Name:                "Test Exit",
				IdentityName:        "Test Identity",
				AuthorizationSource: "same_identity",
				Online:              true,
			}}, nil
		},
	}, sessions, router)
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	disabled := false
	base := AgentConfig{
		ServerAddress: "127.0.0.1", TCPPort: gateway.TCPAddr().(*net.TCPAddr).Port, QUICPort: gateway.QUICAddr().(*net.UDPAddr).Port,
		InsecureTLS: true, SOCKS5Enabled: &disabled, HTTPEnabled: &disabled,
		IdentityID: "a1b2c3d4e5f6g7h8", DefaultExitID: "exit", ConnectTimeout: 3 * time.Second,
	}
	exitCfg := base
	exitCfg.Mode, exitCfg.TransportMode = "EXIT", exitMode
	exitCfg.AllowInternet, exitCfg.AllowPrivateNet, exitCfg.AllowLoopback = true, true, true
	exitAgent, err := NewAgent(exitCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exitAgent.Close() })
	if err := exitAgent.Start(); err != nil {
		t.Fatal(err)
	}
	clientCfg := base
	clientCfg.Mode, clientCfg.TransportMode = "CLIENT", clientMode
	clientAgent, err := NewAgent(clientCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientAgent.Close() })
	if err := clientAgent.Start(); err != nil {
		t.Fatal(err)
	}
	waitAgentReady(t, exitAgent)
	waitAgentReady(t, clientAgent)
	return clientAgent, exitAgent, sessions
}

func waitAgentReady(t *testing.T, agent *Agent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !agent.Status().Connected && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !agent.Status().Connected {
		t.Fatal("agent never completed authentication")
	}
}

func TestStatusCarriesAuthorizedProxyExitInventory(t *testing.T) {
	central, err := acl.NewChecker(acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	clientAgent, _, _ := startRelayPair(t, "quic_only", "quic_only", central)
	status := clientAgent.Status()
	if len(status.ProxyExits) != 1 {
		t.Fatalf("status proxy exits = %+v", status.ProxyExits)
	}
	exit := status.ProxyExits[0]
	if exit.DeviceID != "exit" || exit.Name != "Test Exit" || exit.IdentityName != "Test Identity" ||
		exit.AuthorizationSource != "same_identity" || !exit.Online {
		t.Fatalf("unexpected status proxy exit: %+v", exit)
	}
}

func TestStatusUsesApprovedCapabilitiesWithoutReducingFutureRequests(t *testing.T) {
	agent, err := NewAgent(AgentConfig{Mode: "BOTH"})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()

	agent.mu.Lock()
	agent.approvedMode = modeForApprovedCapabilities([]string{protocol.CapabilityProxyClient})
	agent.mu.Unlock()
	if got := agent.Status().Mode; got != "CLIENT" {
		t.Fatalf("status mode = %q, want server-approved CLIENT", got)
	}
	if got := agent.Config().Mode; got != "BOTH" {
		t.Fatalf("requested mode was reduced to %q; reconnect must continue declaring supported capabilities", got)
	}

	for _, tc := range []struct {
		name string
		caps []string
		want string
	}{
		{"both", []string{protocol.CapabilityProxyClient, protocol.CapabilityProxyExit}, "BOTH"},
		{"exit", []string{protocol.CapabilityProxyExit}, "EXIT"},
		{"none", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modeForApprovedCapabilities(tc.caps); got != tc.want {
				t.Fatalf("mode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRelayTransportMatrix(t *testing.T) {
	for _, clientMode := range []string{"quic_only", "tcp_only"} {
		for _, exitMode := range []string{"quic_only", "tcp_only"} {
			t.Run(clientMode+"_"+exitMode, func(t *testing.T) {
				central, err := acl.NewChecker(acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true})
				if err != nil {
					t.Fatal(err)
				}
				clientAgent, _, _ := startRelayPair(t, clientMode, exitMode, central)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				tcpTarget, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer tcpTarget.Close()
				tcpDone := make(chan error, 1)
				go func() {
					conn, err := tcpTarget.Accept()
					if err != nil {
						tcpDone <- err
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
					request, err := io.ReadAll(conn)
					if err == nil && !bytes.Equal(request, []byte("request")) {
						err = errors.New("unexpected request")
					}
					if err == nil {
						_, err = conn.Write([]byte("response-after-eof"))
					}
					tcpDone <- err
				}()
				conn, err := clientAgent.dialer.DialTCP(ctx, "exit", "127.0.0.1", uint16(tcpTarget.Addr().(*net.TCPAddr).Port))
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
				if _, err := conn.Write([]byte("request")); err != nil {
					t.Fatal(err)
				}
				if err := conn.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
					t.Fatal(err)
				}
				response, err := io.ReadAll(conn)
				if err != nil || string(response) != "response-after-eof" {
					t.Fatalf("TCP half close truncated response: %q, %v", response, err)
				}
				if err := <-tcpDone; err != nil {
					t.Fatal(err)
				}
				diagnostics := clientAgent.Status().TunnelDiagnostics
				if diagnostics == nil || diagnostics.Local == "" || diagnostics.Remote == "" {
					t.Fatalf("missing relay endpoint diagnostics: %+v", diagnostics)
				}
				if clientMode == "quic_only" {
					if diagnostics.QUIC == nil || diagnostics.QUIC.BytesReceived == 0 || diagnostics.QUIC.PacketsSent == 0 {
						t.Fatalf("missing live QUIC counters: %+v", diagnostics.QUIC)
					}
				} else if diagnostics.QUIC != nil {
					t.Fatal("TCP session reported QUIC counters")
				}

				udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				sources := make(chan int, 16)
				udpDone := make(chan struct{})
				go func() {
					defer close(udpDone)
					buffer := make([]byte, 65535)
					for {
						n, source, err := udpTarget.ReadFromUDP(buffer)
						if err != nil {
							return
						}
						sources <- source.Port
						for range 2 {
							if _, err := udpTarget.WriteToUDP(buffer[:n], source); err != nil {
								return
							}
						}
					}
				}()
				defer func() { _ = udpTarget.Close(); <-udpDone }()
				port := uint16(udpTarget.LocalAddr().(*net.UDPAddr).Port)
				packets, err := clientAgent.dialer.DialUDP(ctx, "exit", "127.0.0.1", port)
				if err != nil {
					t.Fatal(err)
				}
				defer packets.Close()
				wantMode := protocol.UDPModeStream
				if clientMode == "quic_only" && exitMode == "quic_only" {
					wantMode = protocol.UDPModeDatagram
				}
				if got := packets.(interface{ UDPMode() string }).UDPMode(); got != wantMode {
					t.Fatalf("UDP mode=%s, want %s", got, wantMode)
				}
				_ = packets.SetDeadline(time.Now().Add(4 * time.Second))
				firstPort := 0
				for _, payload := range [][]byte{[]byte("first"), {}, bytes.Repeat([]byte("f"), 4096), []byte("last")} {
					if n, err := packets.WriteTo(payload, udpTarget.LocalAddr()); err != nil || n != len(payload) {
						t.Fatalf("UDP write: n=%d err=%v", n, err)
					}
					for range 2 {
						buffer := make([]byte, 8192)
						n, _, err := packets.ReadFrom(buffer)
						if err != nil || !bytes.Equal(buffer[:n], payload) {
							t.Fatalf("UDP response: n=%d want=%d err=%v", n, len(payload), err)
						}
					}
					sourcePort := <-sources
					if firstPort == 0 {
						firstPort = sourcePort
					} else if sourcePort != firstPort {
						t.Fatalf("UDP association changed source port: %d -> %d", firstPort, sourcePort)
					}
				}
				_ = packets.Close()
				required, err := clientAgent.rawDialer.DialUDPWithOptions(ctx, "exit", "127.0.0.1", port, proxy.UDPDialOptions{DatagramRequired: true})
				if wantMode == protocol.UDPModeDatagram {
					if err != nil {
						t.Fatal(err)
					}
					_ = required.Close()
				} else if err == nil {
					_ = required.Close()
					t.Fatal("required UDP silently used a reliable stream")
				} else if !strings.Contains(err.Error(), protocol.ErrCodeDatagramRequired) {
					t.Fatalf("unexpected required error: %v", err)
				}
			})
		}
	}
}

func TestExitSpeedTestMeasuresBothRelayDirections(t *testing.T) {
	for _, tc := range []struct{ mode, path string }{
		{"quic_only", protocol.P2PPathRelayQUIC},
		{"tcp_only", protocol.P2PPathRelayTLS},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			central, err := acl.NewChecker(acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true})
			if err != nil {
				t.Fatal(err)
			}
			clientAgent, _, _ := startRelayPair(t, tc.mode, tc.mode, central)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			result, err := clientAgent.RunSpeedTest(ctx, "exit", 500*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			for name, direction := range map[string]client.SpeedTestDirectionResult{"upload": result.Upload, "download": result.Download} {
				if direction.Bytes == 0 || direction.DurationMS < 400 || direction.BytesPerSecond <= 0 || direction.Megabits <= 0 {
					t.Fatalf("%s speed test missing measurement: %+v", name, direction)
				}
				if direction.Path != tc.path {
					t.Fatalf("%s path = %q, want %q", name, direction.Path, tc.path)
				}
			}
		})
	}
}

func TestInvalidPoliciesAndRoleChangesCannotActivateExit(t *testing.T) {
	for _, mode := range []string{"CLIENT", "EXIT", "BOTH"} {
		if agent, err := NewAgent(AgentConfig{Mode: mode, AccessCIDRs: []string{"127.0.0.1/999"}}); err == nil {
			_ = agent.Close()
			t.Fatalf("invalid ACL accepted for mode %s", mode)
		}
	}
	agent, err := NewAgent(AgentConfig{Mode: "CLIENT", Routing: routing.Config{Mode: routing.ModeRule, DefaultAction: routing.ActionReject}})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if agent.Config().Mode != "CLIENT" || agent.exitHandler != nil {
		t.Fatal("role switch changed live exit state")
	}
	err = agent.ApplyPolicies(routing.Config{Mode: routing.ModeDirect}, divert.Config{Rules: []divert.Rule{{Process: "*", Action: divert.ActionDirect, CIDRs: []string{"invalid"}}}})
	if err == nil {
		t.Fatal("invalid policy was applied")
	}
	if action, _ := agent.routingEngine.Match("example.com", 443); action != routing.ActionReject {
		t.Fatalf("failed update changed policy: %s", action)
	}
}

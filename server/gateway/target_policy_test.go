package gateway

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type policyTestStream struct{ net.Conn }

func (s *policyTestStream) CloseWrite() error { return s.Conn.Close() }

type policyTestSession struct {
	stream tunnel.TunnelStream
	opens  atomic.Int64
}

func (s *policyTestSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	s.opens.Add(1)
	if s.stream == nil {
		return nil, errors.New("unexpected open")
	}
	return s.stream, nil
}
func (s *policyTestSession) AcceptStream(ctx context.Context) (tunnel.TunnelStream, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*policyTestSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (*policyTestSession) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (*policyTestSession) RemoteAddr() net.Addr            { return &net.TCPAddr{} }
func (*policyTestSession) Done() <-chan struct{}           { return nil }
func (s *policyTestSession) Close() error {
	if s.stream != nil {
		return s.stream.Close()
	}
	return nil
}

func TestRelayBindsTargetPolicyAndRejectsLegacyExit(t *testing.T) {
	for _, kind := range []protocol.FrameType{protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP} {
		for _, scenario := range []string{"central", "no-central", "legacy-exit"} {
			t.Run(string(rune('0'+kind))+"_"+scenario, func(t *testing.T) {
				var checker *acl.Checker
				if scenario != "no-central" {
					var err error
					checker, err = acl.NewChecker(acl.Policy{ID: "central", AllowInternet: true, AccessMode: acl.AccessModeAllow, AccessHosts: []string{"example.com"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				manager := session.NewManager()
				caps := []string{"exit", protocol.CapabilityTargetACL}
				if scenario == "legacy-exit" {
					caps = []string{"exit"}
				}
				upstream, exitPeer := net.Pipe()
				defer upstream.Close()
				defer exitPeer.Close()
				exitSession := &policyTestSession{stream: &policyTestStream{upstream}}
				manager.Register(&session.DeviceSession{DeviceID: "exit", Mode: "EXIT", Grants: []string{protocol.CapabilityProxyExit}, Capabilities: caps, Tunnel: exitSession})
				router := NewStreamRouter(manager, checker, func(string, string) (bool, error) { return true, nil }, nil)
				left, right := net.Pipe()
				defer left.Close()
				defer right.Close()
				_ = right.SetDeadline(time.Now().Add(time.Second))
				seen := make(chan *acl.Policy, 1)
				if scenario != "legacy-exit" {
					go func() {
						if _, err := protocol.ReadStreamHeader(exitPeer); err != nil {
							return
						}
						if kind == protocol.FrameTypeOpenTCP {
							var req protocol.OpenTCPRequest
							if protocol.ReadJSON(exitPeer, &req) != nil {
								return
							}
							seen <- req.RelayPolicy
							_ = protocol.WriteJSON(exitPeer, protocol.OpenTCPResponse{Success: false, ErrorCode: "TEST_EXIT_STOP"})
						} else {
							var req protocol.OpenUDPRequest
							if protocol.ReadJSON(exitPeer, &req) != nil {
								return
							}
							seen <- req.RelayPolicy
							_ = protocol.WriteJSON(exitPeer, protocol.OpenUDPResponse{Success: false, ErrorCode: "TEST_EXIT_STOP"})
						}
					}()
				}
				done := make(chan struct{})
				go func() {
					router.HandleClientStream(context.Background(), &policyTestStream{left}, &session.DeviceSession{
						DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
					})
					close(done)
				}()
				if err := protocol.WriteStreamHeader(right, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: kind, ExitDeviceID: "exit"}); err != nil {
					t.Fatal(err)
				}
				forged := &acl.Policy{ID: "client-forged", AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true}
				if kind == protocol.FrameTypeOpenTCP {
					if err := protocol.WriteJSON(right, protocol.OpenTCPRequest{Host: "example.com", Port: 443, RelayPolicy: forged}); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := protocol.WriteJSON(right, protocol.OpenUDPRequest{Host: "example.com", Port: 443, RelayPolicy: forged}); err != nil {
						t.Fatal(err)
					}
				}
				var response protocol.OpenTCPResponse
				if err := protocol.ReadJSON(right, &response); err != nil {
					t.Fatal(err)
				}
				if response.Success {
					t.Fatal("request unexpectedly succeeded")
				}
				if scenario == "legacy-exit" {
					if exitSession.opens.Load() != 0 || response.ErrorCode != "ACL_DENIED" {
						t.Fatalf("legacy exit bypassed policy enforcement: %+v", response)
					}
				} else {
					if response.ErrorCode != "TEST_EXIT_STOP" {
						t.Fatalf("did not reach exit: %+v", response)
					}
					policy := <-seen
					if scenario == "no-central" {
						if policy != nil {
							t.Fatal("forwarded a client-supplied policy")
						}
					} else if policy == nil || policy.ID != "central" || policy.AllowLoopback || policy.AllowPrivateNetwork || policy.AccessMode != acl.AccessModeAllow {
						t.Fatalf("central policy overwritten: %+v", policy)
					}
				}
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("router did not finish")
				}
			})
		}
	}
}

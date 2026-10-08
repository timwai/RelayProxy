package direct

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestSelectPublicEndpointUsesStablePreference(t *testing.T) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	path := protocol.ProxyPublicDirectPath{
		Available: true,
		Transport: "quic",
		Endpoints: []protocol.PublicDirectEndpoint{
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "[2001:4860:4860::8888]:35820", Source: protocol.PublicDirectEndpointIPv6, Verified: true, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "203.0.113.20:35820", Source: protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "exit.example.com:35820", Source: protocol.PublicDirectEndpointManual, Verified: true, CertFingerprint: fingerprint},
		},
	}
	got, ok := selectPublicEndpoint(path)
	if !ok {
		t.Fatal("no public direct endpoint selected")
	}
	if got.Source != protocol.PublicDirectEndpointManual || got.Address != "exit.example.com:35820" {
		t.Fatalf("selected endpoint=%+v, want verified manual endpoint", got)
	}

	path.Endpoints = path.Endpoints[:2]
	got, ok = selectPublicEndpoint(path)
	if !ok || got.Source != protocol.PublicDirectEndpointObserved {
		t.Fatalf("selected endpoint=%+v, want observed IPv4 before IPv6", got)
	}
}

func TestSelectPublicEndpointIgnoresUnverifiedHigherPriority(t *testing.T) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	path := protocol.ProxyPublicDirectPath{
		Available: true,
		Transport: "quic",
		Endpoints: []protocol.PublicDirectEndpoint{
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "exit.example.com:35820", Source: protocol.PublicDirectEndpointManual, Verified: false, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "203.0.113.20:35820", Source: protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint},
		},
	}
	got, ok := selectPublicEndpoint(path)
	if !ok || got.Source != protocol.PublicDirectEndpointObserved {
		t.Fatalf("selected endpoint=%+v, want verified observed endpoint", got)
	}
}

type clientManagerTestSession struct {
	once       sync.Once
	done       chan struct{}
	closeCalls atomic.Int32
}

func newClientManagerTestSession() *clientManagerTestSession {
	return &clientManagerTestSession{done: make(chan struct{})}
}

func (s *clientManagerTestSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *clientManagerTestSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *clientManagerTestSession) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *clientManagerTestSession) RemoteAddr() net.Addr            { return &net.UDPAddr{} }
func (s *clientManagerTestSession) LocalAddr() net.Addr             { return &net.UDPAddr{} }

func (s *clientManagerTestSession) Close() error {
	s.closeCalls.Add(1)
	s.signalDone()
	return nil
}

func (s *clientManagerTestSession) Done() <-chan struct{} { return s.done }

func (s *clientManagerTestSession) signalDone() {
	s.once.Do(func() { close(s.done) })
}

func TestClientManagerClosesSessionAfterRemoteTermination(t *testing.T) {
	fake := newClientManagerTestSession()
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		Dial: func(context.Context, DialConfig) (tunnel.TunnelSession, error) {
			return fake, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available: true, Transport: "quic",
			Ticket:          []byte("remote-close-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol:        protocol.PublicDirectEndpointProtocolUDP,
				Address:         "203.0.113.20:35820",
				Source:          protocol.PublicDirectEndpointObserved,
				Verified:        true,
				CertFingerprint: fingerprint,
			}},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct client attempt was not started")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}

	fake.signalDone()
	deadline = time.Now().Add(time.Second)
	for fake.closeCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("remotely terminated public direct session was not closed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := manager.ReadyForExit("exit"); ok {
		t.Fatal("remotely terminated public direct session remained selectable")
	}
}

func TestClientManagerClosesReadySessionWhenAuthorizationDisappears(t *testing.T) {
	fake := newClientManagerTestSession()
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		Dial: func(context.Context, DialConfig) (tunnel.TunnelSession, error) {
			return fake, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available: true, Transport: "quic",
			Ticket:          []byte("one-time-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol:        protocol.PublicDirectEndpointProtocolUDP,
				Address:         "203.0.113.20:35820",
				Source:          protocol.PublicDirectEndpointObserved,
				Verified:        true,
				CertFingerprint: fingerprint,
			}},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct client attempt was not started")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}

	manager.UpdateInventory(nil)
	select {
	case <-fake.Done():
	case <-time.After(time.Second):
		t.Fatal("authorization removal did not close the ready public direct session")
	}
	if _, ok := manager.ReadyForExit("exit"); ok {
		t.Fatal("revoked public direct session remained selectable")
	}
}

func TestClientManagerRacesAllVerifiedEndpointsBeforeAuth(t *testing.T) {
	fake := newClientManagerTestSession()
	var raced []DialConfig
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		EndpointUsable: func(string) bool { return true },
		RaceDial: func(_ context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
			raced = append([]DialConfig(nil), configs...)
			return fake, configs[1].Address, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available:       true,
			Transport:       "quic",
			Ticket:          []byte("race-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{
				{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "[2001:4860:4860::8888]:35820", Source: protocol.PublicDirectEndpointIPv6, Verified: true, CertFingerprint: fingerprint},
				{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "203.0.113.20:35820", Source: protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint},
				{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "exit.example.com:35820", Source: protocol.PublicDirectEndpointManual, Verified: true, CertFingerprint: fingerprint},
			},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct race was not started")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("raced public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	if len(raced) != 3 {
		t.Fatalf("raced endpoints=%d, want 3", len(raced))
	}
	if raced[0].Address != "exit.example.com:35820" ||
		raced[1].Address != "203.0.113.20:35820" ||
		raced[2].Address != "[2001:4860:4860::8888]:35820" {
		t.Fatalf("unexpected endpoint race order: %+v", raced)
	}
	status, ok := manager.PathStatus("exit")
	if !ok || status.Endpoint != raced[1].Address || status.State != "READY" {
		t.Fatalf("path status=%+v ok=%v", status, ok)
	}
}

func TestClientManagerIgnoresStaleTicketAttemptResults(t *testing.T) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	inventory := func(ticket string) []protocol.ProxyExit {
		return []protocol.ProxyExit{{
			DeviceID: "exit", Online: true,
			Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
				Available:       true,
				Transport:       "quic",
				Ticket:          []byte(ticket),
				TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
				Endpoints: []protocol.PublicDirectEndpoint{{
					Protocol:        protocol.PublicDirectEndpointProtocolUDP,
					Address:         "203.0.113.20:35820",
					Source:          protocol.PublicDirectEndpointObserved,
					Verified:        true,
					CertFingerprint: fingerprint,
				}},
			}},
		}}
	}

	for _, tc := range []struct {
		name     string
		firstErr error
	}{
		{name: "stale success"},
		{name: "stale failure", firstErr: errors.New("old ticket dial failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldSession := newClientManagerTestSession()
			newSession := newClientManagerTestSession()
			firstStarted := make(chan struct{})
			releaseFirst := make(chan struct{})
			var callsMu sync.Mutex
			calls := 0

			manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
				AttemptTimeout: time.Second,
				RaceDial: func(_ context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
					callsMu.Lock()
					calls++
					call := calls
					callsMu.Unlock()
					if call == 1 {
						close(firstStarted)
						<-releaseFirst
						if tc.firstErr != nil {
							return nil, "", tc.firstErr
						}
						return oldSession, configs[0].Address, nil
					}
					return newSession, configs[0].Address, nil
				},
			})
			defer manager.Close()

			manager.UpdateInventory(inventory("old-ticket"))
			if !manager.EnsureClient("exit") {
				t.Fatal("old-ticket attempt was not started")
			}
			select {
			case <-firstStarted:
			case <-time.After(time.Second):
				t.Fatal("old-ticket attempt did not start")
			}

			manager.UpdateInventory(inventory("new-ticket"))
			if manager.EnsureClient("exit") {
				t.Fatal("new-ticket attempt started before stale attempt completed")
			}
			close(releaseFirst)

			deadline := time.Now().Add(time.Second)
			for {
				status, ok := manager.PathStatus("exit")
				if ok && status.State == "AVAILABLE" && status.Error == "" && status.CooldownUntil.IsZero() {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("stale attempt polluted refreshed ticket state: status=%+v ok=%v", status, ok)
				}
				time.Sleep(time.Millisecond)
			}

			if tc.firstErr == nil {
				select {
				case <-oldSession.Done():
				case <-time.After(time.Second):
					t.Fatal("stale successful session was not closed")
				}
			}
			if !manager.EnsureClient("exit") {
				t.Fatal("refreshed ticket could not start after stale attempt completed")
			}
			deadline = time.Now().Add(time.Second)
			for {
				if session, ok := manager.ReadyForExit("exit"); ok {
					if session != newSession {
						t.Fatalf("ready session=%T, want refreshed-ticket session", session)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("refreshed ticket did not become ready")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestClientManagerUsesServerVerifiedDialAddress(t *testing.T) {
	fake := newClientManagerTestSession()
	var dialed string
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		RaceDial: func(_ context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
			if len(configs) != 1 {
				t.Fatalf("dial configs=%d, want 1", len(configs))
			}
			dialed = configs[0].Address
			return fake, configs[0].Address, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available:       true,
			Transport:       "quic",
			Ticket:          []byte("verified-dial-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol:        protocol.PublicDirectEndpointProtocolUDP,
				Address:         "exit.example.com:35820",
				DialAddress:     "203.0.113.20:35820",
				Source:          protocol.PublicDirectEndpointManual,
				Verified:        true,
				CertFingerprint: fingerprint,
			}},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct attempt was not started")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	if dialed != "203.0.113.20:35820" {
		t.Fatalf("dialed address=%q, want Server-verified IP literal", dialed)
	}
	status, ok := manager.PathStatus("exit")
	if !ok || status.Endpoint != dialed {
		t.Fatalf("path status=%+v ok=%v", status, ok)
	}
}

func TestIPv4OnlyClientSkipsIPv6DirectAndUsesIPv4(t *testing.T) {
	fake := newClientManagerTestSession()
	var mu sync.Mutex
	var raced []DialConfig
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000"
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		EndpointUsable: func(address string) bool {
			host, _, err := net.SplitHostPort(address)
			return err == nil && net.ParseIP(host).To4() != nil
		},
		RaceDial: func(_ context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
			mu.Lock()
			raced = append([]DialConfig(nil), configs...)
			mu.Unlock()
			return fake, configs[0].Address, nil
		},
	})
	defer manager.Close()
	makeInventory := func(ticket string, endpoints ...protocol.PublicDirectEndpoint) []protocol.ProxyExit {
		return []protocol.ProxyExit{{
			DeviceID: "exit", Online: true,
			Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
				Available: true, Transport: "quic",
				Ticket: []byte(ticket), TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
				Endpoints: endpoints,
			}},
		}}
	}
	ipv6 := protocol.PublicDirectEndpoint{
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  "[2408:8266:501:6757:b251:8eff:feff:3735]:20800",
		Source:   protocol.PublicDirectEndpointIPv6, Verified: true, CertFingerprint: fingerprint,
	}
	ipv4 := protocol.PublicDirectEndpoint{
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  "203.0.113.20:20800",
		Source:   protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint,
	}
	manager.UpdateInventory(makeInventory("first-ticket", ipv6))
	if manager.PreferForExit("exit") || manager.EnsureClient("exit") {
		t.Fatal("IPv4-only client attempted to dial an IPv6-only public endpoint")
	}
	if status, ok := manager.PathStatus("exit"); !ok ||
		status.State != "UNAVAILABLE" || status.Endpoint != "" ||
		!strings.Contains(status.Error, "IP family") {
		t.Fatalf("unusable IPv6-only Public Direct status=%+v ok=%v", status, ok)
	}
	manager.UpdateInventory(makeInventory("second-ticket", ipv6, ipv4))
	if !manager.PreferForExit("exit") || !manager.EnsureClient("exit") {
		t.Fatal("IPv4 candidate did not restore Public Direct availability")
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, ready := manager.ReadyForExit("exit"); ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPv4 public direct connection did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(raced) != 1 || raced[0].Address != ipv4.Address {
		t.Fatalf("IPv4-only QUIC race included an incompatible endpoint: %+v", raced)
	}
}

func TestServerPinnedIPv6ManualEndpointIsFilteredOnIPv4OnlyClient(t *testing.T) {
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		EndpointUsable: func(address string) bool { return address == "203.0.113.20:20800" },
	})
	defer manager.Close()
	public := protocol.ProxyPublicDirectPath{Endpoints: []protocol.PublicDirectEndpoint{
		{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  "exit.example.com:20800", DialAddress: "[2408:8266:501:6757:b251:8eff:feff:3735]:20800",
			Source: protocol.PublicDirectEndpointManual, Verified: true, CertFingerprint: "sha256:abc",
		},
		{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  "203.0.113.20:20800", Source: protocol.PublicDirectEndpointObserved,
			Verified: true, CertFingerprint: "sha256:abc",
		},
	}}
	items := manager.reachablePublicEndpoints(public)
	if len(items) != 1 || publicEndpointDialAddress(items[0]) != "203.0.113.20:20800" {
		t.Fatalf("Server-pinned IPv6 manual endpoint bypassed local route screening: %+v", items)
	}
}

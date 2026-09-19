package divert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testDialer struct {
	tcp func(context.Context, string, string, uint16) (net.Conn, error)
	udp func(context.Context, string, string, uint16) (net.PacketConn, error)
}

func (d *testDialer) DialTCP(ctx context.Context, exit, host string, port uint16) (net.Conn, error) {
	if d.tcp == nil {
		return nil, errors.New("unexpected TCP dial")
	}
	return d.tcp(ctx, exit, host, port)
}
func (d *testDialer) DialUDP(ctx context.Context, exit, host string, port uint16) (net.PacketConn, error) {
	if d.udp == nil {
		return nil, errors.New("unexpected UDP dial")
	}
	return d.udp(ctx, exit, host, port)
}

func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.Dialer == nil {
		opts.Dialer = &testDialer{}
	}
	server, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func testFlow(protocol Protocol, target *net.UDPAddr) Flow {
	flow := Flow{Process: "browser.exe", ProcessID: 424242, SourceIP: "127.0.0.1", SourcePort: 42000,
		IP: "192.0.2.10", Port: 443, Protocol: protocol}
	if target != nil {
		flow.IP, flow.Port = target.IP.String(), uint16(target.Port)
	}
	return flow
}

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.AcceptTCP()
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	_ = server.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client, server
}

func TestTCPUsesOneFrozenDecisionAndPreservesHalfClose(t *testing.T) {
	client, intercepted := tcpPair(t)
	upstream, target := tcpPair(t)
	var calls atomic.Int32
	dialer := &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
		calls.Add(1)
		if exit != "exit-a" || host != "192.0.2.10" || port != 443 {
			return nil, fmt.Errorf("lost flow metadata: exit=%s host=%s port=%d", exit, host, port)
		}
		return upstream, nil
	}}
	server := newTestServer(t, Options{Dialer: dialer, Config: Config{DefaultAction: ActionDirect,
		Rules: []Rule{{Enabled: true, Process: "browser.exe", Action: ActionProxy, ExitID: "exit-a"}}}})
	flow := testFlow(ProtoTCP, nil)
	route, err := server.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
		t.Fatal(err)
	}
	next, err := server.ClassifyFlow(flow)
	if err != nil || next.Decision().Action != ActionReject {
		t.Fatalf("new policy not published: route=%+v err=%v", next, err)
	}
	done := make(chan error, 1)
	go func() { done <- server.ForwardTCP(context.Background(), route, intercepted) }()
	request := []byte("request terminated by TCP EOF")
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(target)
	if err != nil || !bytes.Equal(got, request) {
		t.Fatalf("target read=%q err=%v", got, err)
	}
	reply := []byte("response after request EOF")
	if _, err := target.Write(reply); err != nil {
		t.Fatal(err)
	}
	_ = target.CloseWrite()
	got, err = io.ReadAll(client)
	if err != nil || !bytes.Equal(got, reply) {
		t.Fatalf("client read=%q err=%v", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("dial count=%d", calls.Load())
	}
}

type udpObservation struct {
	source  string
	payload string
}

func udpReplyServer(t *testing.T, replies int) (*net.UDPAddr, <-chan udpObservation) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	observations := make(chan udpObservation, 128)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, maxUDPPayload)
		for {
			n, source, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			observations <- udpObservation{source: source.String(), payload: string(buf[:n])}
			for i := 0; i < replies; i++ {
				_, _ = conn.WriteToUDP([]byte(fmt.Sprintf("%s/%d", buf[:n], i)), source)
			}
		}
	}()
	t.Cleanup(func() { _ = conn.Close(); <-done })
	return conn.LocalAddr().(*net.UDPAddr), observations
}

func connectedUDPDialer(calls *atomic.Int32) *testDialer {
	return &testDialer{udp: func(ctx context.Context, _ string, host string, port uint16) (net.PacketConn, error) {
		calls.Add(1)
		conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			return nil, err
		}
		return conn.(*net.UDPConn), nil
	}}
}

func TestUDPReusesAssociationAndForwardsAllReplies(t *testing.T) {
	target, observations := udpReplyServer(t, 2)
	var calls atomic.Int32
	server := newTestServer(t, Options{Dialer: connectedUDPDialer(&calls)})
	flow := testFlow(ProtoUDP, target)
	route, err := server.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replies := make(chan string, 8)
	respond := func(_ context.Context, key FlowKey, payload []byte) error {
		if key != route.Key() {
			return errors.New("reply lost original five-tuple")
		}
		replies <- string(payload)
		return nil
	}
	for _, payload := range []string{"first", "", "second", "third"} {
		if err := server.ForwardUDP(ctx, route, []byte(payload), respond); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]bool{"first/0": true, "first/1": true, "/0": true, "/1": true, "second/0": true, "second/1": true, "third/0": true, "third/1": true}
	for len(want) > 0 {
		select {
		case reply := <-replies:
			if !want[reply] {
				t.Fatalf("unexpected reply %q", reply)
			}
			delete(want, reply)
		case <-ctx.Done():
			t.Fatalf("missing replies: %v", want)
		}
	}
	var source string
	for range 4 {
		observation := <-observations
		if source == "" {
			source = observation.source
		} else if source != observation.source {
			t.Fatalf("UDP source port changed: %s -> %s", source, observation.source)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same flow opened %d UDP sockets", calls.Load())
	}
	if err := server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
		t.Fatal(err)
	}
	same, err := server.ClassifyFlow(flow)
	if err != nil || same != route || same.Decision().Action != ActionProxy {
		t.Fatalf("existing association was reclassified: %v %v", same, err)
	}
	flow.SourcePort++
	newFlow, err := server.ClassifyFlow(flow)
	if err != nil || newFlow.Decision().Action != ActionReject {
		t.Fatalf("new association ignored new policy: %v %v", newFlow, err)
	}
}

func TestUDPRenewalPreservesFrozenDecisionAfterTunnelAssociationDies(t *testing.T) {
	target, observations := udpReplyServer(t, 0)
	var calls atomic.Int32
	server := newTestServer(t, Options{
		Dialer: connectedUDPDialer(&calls),
		Config: Config{
			DefaultAction: ActionProxy,
			Rules: []Rule{{
				Name: "frozen",
				Enabled: true,
				Process: "browser.exe",
				Action: ActionProxy,
				ExitID: "exit-a",
			}},
		},
	})

	route, err := server.ClassifyFlow(testFlow(ProtoUDP, target))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.PinUDPAssociation(route); err != nil {
		t.Fatal(err)
	}
	if err := server.ForwardUDP(context.Background(), route, []byte("before"), func(context.Context, FlowKey, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observations:
		if got.payload != "before" {
			t.Fatalf("first payload=%q", got.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("first UDP packet not forwarded")
	}

	// Simulate the tunnel-backed PacketConn dying while the Windows UDP flow
	// remains alive. New policy must not rewrite the already-authorized OS flow.
	server.removeUDPAssociation(route.udp)
	if server.UDPAssociationActive(route) {
		t.Fatal("dead UDP association still reported active")
	}
	if err := server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
		t.Fatal(err)
	}

	fresh, err := server.RenewUDPAssociation(route)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == route {
		t.Fatal("renewal reused the dead classification object")
	}
	if fresh.Decision() != route.Decision() {
		t.Fatalf("renewal changed frozen decision: before=%+v after=%+v", route.Decision(), fresh.Decision())
	}
	if err := server.PinUDPAssociation(fresh); err != nil {
		t.Fatal(err)
	}
	if err := server.ForwardUDP(context.Background(), fresh, []byte("after"), func(context.Context, FlowKey, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observations:
		if got.payload != "after" {
			t.Fatalf("renewed payload=%q", got.payload)
		}
	case <-time.After(time.Second):
		t.Fatal("renewed UDP packet not forwarded")
	}
	if calls.Load() != 2 {
		t.Fatalf("UDP dials=%d want 2", calls.Load())
	}
}

func TestUDPSameClientDifferentDestinationsRemainSeparate(t *testing.T) {
	a, seenA := udpReplyServer(t, 1)
	b, seenB := udpReplyServer(t, 1)
	var calls atomic.Int32
	server := newTestServer(t, Options{Dialer: connectedUDPDialer(&calls)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replies := make(chan FlowKey, 2)
	respond := func(_ context.Context, key FlowKey, _ []byte) error { replies <- key; return nil }
	for _, target := range []*net.UDPAddr{a, b} {
		route, err := server.ClassifyFlow(testFlow(ProtoUDP, target))
		if err != nil {
			t.Fatal(err)
		}
		if err := server.ForwardUDP(ctx, route, []byte(target.String()), respond); err != nil {
			t.Fatal(err)
		}
	}
	destinations := make(map[string]bool)
	for range 2 {
		select {
		case key := <-replies:
			destinations[key.Destination.String()] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !destinations[a.String()] || !destinations[b.String()] || calls.Load() != 2 {
		t.Fatalf("destinations=%v dials=%d", destinations, calls.Load())
	}
	if observed := <-seenA; observed.payload != a.String() {
		t.Fatalf("packet misrouted to A: %+v", observed)
	}
	if observed := <-seenB; observed.payload != b.String() {
		t.Fatalf("packet misrouted to B: %+v", observed)
	}
}

func TestUDPCapacityExpiryAndProcessReuse(t *testing.T) {
	server := newTestServer(t, Options{MaxUDPAssociations: 1, UDPIdleTimeout: 80 * time.Millisecond})
	flow := testFlow(ProtoUDP, nil)
	route, err := server.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	other := flow
	other.Port++
	if _, err := server.ClassifyFlow(other); !errors.Is(err, ErrFlowCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	flow.ProcessID++
	replacement, err := server.ClassifyFlow(flow)
	if err != nil || replacement == route {
		t.Fatalf("process reuse retained old flow: %v", err)
	}
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for server.UDPAssociationCount() != 0 {
		select {
		case <-deadline:
			t.Fatal("idle UDP association was not evicted")
		case <-ticker.C:
		}
	}
	if _, err := server.ClassifyFlow(other); err != nil {
		t.Fatalf("expired capacity not reclaimed: %v", err)
	}
	if err := server.ForwardUDP(context.Background(), replacement, nil, func(context.Context, FlowKey, []byte) error { return nil }); !errors.Is(err, ErrAssociationClosed) {
		t.Fatalf("expired token remained usable: %v", err)
	}
}

func TestCloseCancelsPendingDialAndWaitsForFlow(t *testing.T) {
	for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
		t.Run(string(protocol), func(t *testing.T) {
			started := make(chan struct{})
			dialer := &testDialer{
				tcp: func(ctx context.Context, _, _ string, _ uint16) (net.Conn, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
				udp: func(ctx context.Context, _, _ string, _ uint16) (net.PacketConn, error) {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
			server := newTestServer(t, Options{Dialer: dialer})
			route, err := server.ClassifyFlow(testFlow(protocol, nil))
			if err != nil {
				t.Fatal(err)
			}
			forwarded := make(chan error, 1)
			if protocol == ProtoTCP {
				client, intercepted := net.Pipe()
				defer client.Close()
				go func() { forwarded <- server.ForwardTCP(context.Background(), route, intercepted) }()
			} else {
				go func() {
					forwarded <- server.ForwardUDP(context.Background(), route, []byte("request"), func(context.Context, FlowKey, []byte) error { return nil })
				}()
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("dial did not start")
			}
			closed := make(chan struct{})
			go func() { _ = server.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not cancel pending flow")
			}
			if err := <-forwarded; err == nil {
				t.Fatal("cancelled flow unexpectedly succeeded")
			}
			if server.UDPAssociationCount() != 0 {
				t.Fatal("Close retained associations")
			}
		})
	}
}

func TestUnsupportedPlatformFailsBeforeActivation(t *testing.T) {
	server := newTestServer(t, Options{Config: Config{Mode: "divert"}})
	if Preflight(server.engine.Config()) == nil {
		t.Skip("platform is ready; ordinary unit tests must not start interception")
	}
	if err := server.Start(); !errors.Is(err, ErrUnsupportedPlatform) && !errors.Is(err, ErrPlatformNotReady) {
		t.Fatalf("Start error=%v", err)
	}
	if server.Running() || server.ListenAddr() != "" || server.UDPListenAddr() != "" {
		t.Fatal("unsupported interceptor activated a listener")
	}
}

func TestUDPConcurrentFirstPacketsDialOnlyOnce(t *testing.T) {
	target, _ := udpReplyServer(t, 1)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	dialer := &testDialer{udp: func(ctx context.Context, _ string, host string, port uint16) (net.PacketConn, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			return nil, err
		}
		return conn.(*net.UDPConn), nil
	}}
	server := newTestServer(t, Options{Dialer: dialer})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const packets = 12
	replies := make(chan string, packets)
	respond := func(_ context.Context, _ FlowKey, payload []byte) error { replies <- string(payload); return nil }
	errs := make(chan error, packets)
	for i := range packets {
		go func() {
			route, err := server.ClassifyFlow(testFlow(ProtoUDP, target))
			if err == nil {
				err = server.ForwardUDP(ctx, route, []byte(strconv.Itoa(i)), respond)
			}
			errs <- err
		}()
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(release)
	for range packets {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	for range packets {
		select {
		case reply := <-replies:
			if seen[reply] {
				t.Fatalf("duplicate reply %q", reply)
			}
			seen[reply] = true
		case <-ctx.Done():
			t.Fatal("concurrent packets lost")
		}
	}
	if calls.Load() != 1 || server.UDPAssociationCount() != 1 {
		t.Fatalf("dials=%d associations=%d", calls.Load(), server.UDPAssociationCount())
	}
}

func TestCloseUnblocksActiveUDPReader(t *testing.T) {
	target, observed := udpReplyServer(t, 0)
	var calls atomic.Int32
	server := newTestServer(t, Options{Dialer: connectedUDPDialer(&calls), UDPIdleTimeout: time.Hour})
	route, err := server.ClassifyFlow(testFlow(ProtoUDP, target))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ForwardUDP(context.Background(), route, []byte("no reply"), func(context.Context, FlowKey, []byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("request not received")
	}
	done := make(chan struct{})
	go func() { _ = server.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close left the UDP response reader blocked")
	}
	if server.UDPAssociationCount() != 0 {
		t.Fatal("closed server retained UDP sockets")
	}
}

func TestClassificationRejectsMissingMetadataAndNeverDialsDirect(t *testing.T) {
	server := newTestServer(t, Options{Config: Config{DefaultAction: ActionDirect}})
	bad := testFlow(ProtoUDP, nil)
	bad.Process = ""
	if _, err := server.ClassifyFlow(bad); err == nil {
		t.Fatal("missing process identity accepted")
	}
	bad = testFlow(ProtoUDP, nil)
	bad.SourceIP = ""
	if _, err := server.ClassifyFlow(bad); err == nil {
		t.Fatal("incomplete five-tuple accepted")
	}
	route, err := server.ClassifyFlow(testFlow(ProtoUDP, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ForwardUDP(context.Background(), route, nil, func(context.Context, FlowKey, []byte) error { return nil }); !errors.Is(err, ErrNotProxyFlow) {
		t.Fatalf("DIRECT flow was re-dialed: %v", err)
	}
}

func TestPolicyPublicationLockOnlyGuardsClassification(t *testing.T) {
	var policies sync.RWMutex
	server := newTestServer(t, Options{PolicyMu: &policies})
	policies.Lock()
	if err := server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
		policies.Unlock()
		t.Fatal(err)
	}
	done := make(chan *ClassifiedFlow, 1)
	go func() { route, _ := server.ClassifyFlow(testFlow(ProtoTCP, nil)); done <- route }()
	select {
	case <-done:
		policies.Unlock()
		t.Fatal("classification ignored policy publication lock")
	case <-time.After(20 * time.Millisecond):
	}
	policies.Unlock()
	select {
	case route := <-done:
		if route == nil || route.Decision().Action != ActionReject {
			t.Fatal("classification observed the wrong policy")
		}
	case <-time.After(time.Second):
		t.Fatal("classification did not resume")
	}
}

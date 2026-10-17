package divert

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

type deferredTestDevice struct{ *testPacketDevice }

func (*deferredTestDevice) deferUDPClassification() {}

func TestDeferredUDPReusesOwnerWithinShortBurst(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}})
	i.device = &deferredTestDevice{device}
	var calls atomic.Int32
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		calls.Add(1)
		return packetProcess{pid: 424242, path: "browser.exe"}, nil
	}
	i.start()
	data, _ := packetTestFixture(false, ProtoUDP, []byte("payload"), false)
	for range 32 {
		if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
			t.Fatal(err)
		}
		expectInterceptedPacket(t, device)
	}
	if calls.Load() != 1 {
		t.Fatalf("process table scanned %d times for one burst", calls.Load())
	}
}

func TestSlowUDPOwnerLookupDoesNotBlockExistingTCP(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionProxy}})
	i.device = &deferredTestDevice{device}
	meta := packetMetadata{outbound: true}
	tcp := interceptedSYN(false)
	if err := i.handlePacket(append([]byte(nil), tcp...), meta); err != nil {
		t.Fatal(err)
	}
	expected := expectInterceptedPacket(t, device)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	i.lookup = func(proto Protocol, _, _ netip.AddrPort) (packetProcess, error) {
		if proto == ProtoUDP {
			close(entered)
			<-release
		}
		return packetProcess{pid: 424242, path: "browser.exe"}, nil
	}
	i.start()
	udp, _ := packetTestFixture(false, ProtoUDP, []byte("payload"), false)
	device.incoming <- capturedTestPacket{data: udp, meta: meta}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("UDP lookup did not start")
	}
	device.incoming <- capturedTestPacket{data: append([]byte(nil), tcp...), meta: meta}
	select {
	case packet := <-device.sent:
		if string(packet.data) != string(expected.data) {
			t.Fatal("TCP forwarding changed")
		}
	case <-time.After(time.Second):
		t.Fatal("TCP waited for slow UDP process lookup")
	}
}

func TestUDPProcessCacheRevalidatesReusedEndpointAndLookupMisses(t *testing.T) {
	source, destination := netip.MustParseAddrPort("192.0.2.1:12000"), netip.MustParseAddrPort("203.0.113.1:443")
	calls := 0
	cache := newUDPProcessCache(func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		calls++
		if calls == 2 {
			return packetProcess{}, errors.New("owner not yet visible")
		}
		return packetProcess{pid: uint32(calls), path: "browser.exe"}, nil
	})
	now := time.Now()
	if got := cache.lookup(source, destination, now); got.pid != 1 {
		t.Fatal(got)
	}
	if got := cache.lookup(source, destination, now); got.pid != 1 || calls != 1 {
		t.Fatal("owner not reused")
	}
	now = now.Add(udpProcessCacheTTL)
	if got := cache.lookup(source, destination, now); got.pid != 0 {
		t.Fatal("stale PID survived refresh failure")
	}
	if got := cache.lookup(source, destination, now); got.pid != 3 {
		t.Fatal("lookup miss cached or new owner ignored")
	}
}

func TestDeferredUDPClassificationDoesNotWaitForItsOwnTunnelDial(t *testing.T) {
	app, _ := packetTestFixture(false, ProtoUDP, []byte("application"), false)
	appPacket, _ := parseIPPacket(app)
	appKey := FlowKey{Protocol: ProtoUDP, Source: appPacket.Source, Destination: appPacket.Destination}
	// Force the tunnel's socket into the same shard as the application's socket.
	var own []byte
	var ownSource netip.AddrPort
	for port := uint16(20000); port < 21000; port++ {
		source := netip.AddrPortFrom(appPacket.Source.Addr(), port)
		key := FlowKey{Protocol: ProtoUDP, Source: source, Destination: appPacket.Destination}
		if flowQueue(key, 8) == flowQueue(appKey, 8) {
			own = append([]byte(nil), app...)
			ownSource = source
			if err := rewriteIPPacket(own, source, appPacket.Destination); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if own == nil {
		t.Fatal("no matching shard")
	}
	var interceptor *packetInterceptor
	allowDial := make(chan struct{})
	defer close(allowDial)
	dialer := &testDialer{udp: func(ctx context.Context, _, _ string, _ uint16) (net.PacketConn, error) {
		if err := interceptor.handlePacket(own, packetMetadata{outbound: true}); err != nil {
			return nil, err
		}
		select {
		case <-allowDial:
		case <-ctx.Done():
		}
		return nil, errors.New("dial complete")
	}}
	i, device := newTestInterceptor(t, Options{Dialer: dialer, Guard: LoopGuard{SelfNames: []string{"relay-agent.exe"}}, Config: Config{DefaultAction: ActionProxy}})
	interceptor = i
	i.device = &deferredTestDevice{device}
	i.lookup = func(_ Protocol, source, _ netip.AddrPort) (packetProcess, error) {
		if source == ownSource {
			return packetProcess{pid: 1234, path: "relay-agent.exe"}, nil
		}
		return packetProcess{pid: 424242, path: "browser.exe"}, nil
	}
	i.start()
	if err := i.handlePacket(app, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-device.sent:
		p, err := parseIPPacket(packet.data)
		if err != nil || p.Source != ownSource {
			t.Fatalf("wrong self-bypass packet: %+v %v", p, err)
		}
	case <-time.After(time.Second):
		t.Fatal("tunnel's own UDP packet waited for its dial to finish")
	}
}

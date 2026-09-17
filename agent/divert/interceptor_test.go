package divert

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capturedTestPacket struct {
	data []byte
	meta packetMetadata
}

type testPacketDevice struct {
	incoming chan capturedTestPacket
	sent     chan capturedTestPacket
	done     chan struct{}
	once     sync.Once
}

func newTestPacketDevice() *testPacketDevice {
	return &testPacketDevice{incoming: make(chan capturedTestPacket, 32), sent: make(chan capturedTestPacket, 64), done: make(chan struct{})}
}
func (d *testPacketDevice) Receive(buffer []byte) (int, packetMetadata, error) {
	select {
	case <-d.done:
		return 0, packetMetadata{}, net.ErrClosed
	case packet := <-d.incoming:
		return copy(buffer, packet.data), packet.meta, nil
	}
}
func (d *testPacketDevice) Send(data []byte, meta packetMetadata) error {
	select {
	case <-d.done:
		return net.ErrClosed
	case d.sent <- capturedTestPacket{data: append([]byte(nil), data...), meta: meta}:
		return nil
	}
}
func (d *testPacketDevice) Shutdown() error { return d.Close() }
func (d *testPacketDevice) Close() error    { d.once.Do(func() { close(d.done) }); return nil }

func newTestInterceptor(t *testing.T, opts Options) (*packetInterceptor, *testPacketDevice) {
	t.Helper()
	s := newTestServer(t, opts)
	d := newTestPacketDevice()
	i := newPacketInterceptor(s, d, nil, func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{pid: 424242, path: "browser.exe"}, nil
	})
	i.ports[false], i.ports[true] = 45001, 45002
	t.Cleanup(i.Close)
	return i, d
}

func expectInterceptedPacket(t *testing.T, device *testPacketDevice) capturedTestPacket {
	t.Helper()
	select {
	case packet := <-device.sent:
		return packet
	case <-time.After(3 * time.Second):
		t.Fatal("interceptor did not inject a packet")
		return capturedTestPacket{}
	}
}

func interceptedSYN(ipv6 bool) []byte {
	data, offset := packetTestFixture(ipv6, ProtoTCP, nil, true)
	data[offset+13] = 0x02
	binary.BigEndian.PutUint32(data[offset+8:], 0)
	packetTestSetChecksums(data, offset, ProtoTCP)
	return data
}

func TestInterceptorTCPRestoresBothEndpointsAndFreezesPolicy(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		t.Run(map[bool]string{false: "ipv4", true: "ipv6"}[ipv6], func(t *testing.T) {
			i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionProxy}})
			original := interceptedSYN(ipv6)
			input, _ := parseIPPacket(original)
			meta := packetMetadata{outbound: true, ifIndex: 123, subIfIndex: 9}
			if err := i.handlePacket(append([]byte(nil), original...), meta); err != nil {
				t.Fatal(err)
			}
			reflected := expectInterceptedPacket(t, device)
			proxy, err := parseIPPacket(reflected.data)
			if err != nil {
				t.Fatal(err)
			}
			if reflected.meta.outbound || reflected.meta.ifIndex != 123 || proxy.Source.Addr() != input.Destination.Addr() || proxy.Destination != netip.AddrPortFrom(input.Source.Addr(), i.ports[ipv6]) {
				t.Fatalf("wrong reflected SYN: %+v %+v", proxy, reflected.meta)
			}
			packetTestAssertChecksums(t, proxy.Bytes, proxy.TransportOffset, ProtoTCP)
			if err := i.server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
				t.Fatal(err)
			}
			if err := i.handlePacket(append([]byte(nil), original...), meta); err != nil {
				t.Fatal(err)
			}
			retransmit := expectInterceptedPacket(t, device)
			if !bytes.Equal(reflected.data, retransmit.data) {
				t.Fatal("retransmitted SYN was reclassified or remapped")
			}
			response := append([]byte(nil), reflected.data...)
			if err := rewriteIPPacket(response, proxy.Destination, proxy.Source); err != nil {
				t.Fatal(err)
			}
			response[proxy.TransportOffset+13] = 0x12
			returnMeta := meta
			returnMeta.ifIndex = 999
			if err := i.handlePacket(response, returnMeta); err != nil {
				t.Fatal(err)
			}
			restored := expectInterceptedPacket(t, device)
			p, _ := parseIPPacket(restored.data)
			if p.Source != input.Destination || p.Destination != input.Source || restored.meta.outbound || restored.meta.ifIndex != 123 {
				t.Fatalf("original tuple lost: %+v", p)
			}
			packetTestAssertChecksums(t, p.Bytes, p.TransportOffset, ProtoTCP)
		})
	}
}

func TestInterceptorReusedPortDoesNotReuseVirtualEndpoint(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionProxy}})
	first := interceptedSYN(false)
	p, _ := parseIPPacket(first)
	meta := packetMetadata{outbound: true}
	if err := i.handlePacket(append([]byte(nil), first...), meta); err != nil {
		t.Fatal(err)
	}
	old := expectInterceptedPacket(t, device)
	oldPacket, _ := parseIPPacket(old.data)
	binary.BigEndian.PutUint32(first[p.TransportOffset+4:], p.TCPSequence+100)
	if err := i.handlePacket(first, meta); err != nil {
		t.Fatal(err)
	}
	current := expectInterceptedPacket(t, device)
	currentPacket, _ := parseIPPacket(current.data)
	if currentPacket.Source == oldPacket.Source {
		t.Fatal("reused client tuple shared a virtual TCP endpoint")
	}
	if len(i.reverse) != 2 {
		t.Fatal("old endpoint was not quarantined")
	}
}

func TestInterceptorDirectRejectAndMetadataFailure(t *testing.T) {
	for _, action := range []Action{ActionDirect, ActionReject} {
		t.Run(string(action), func(t *testing.T) {
			i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: action}})
			original := interceptedSYN(false)
			input, _ := parseIPPacket(original)
			if err := i.handlePacket(append([]byte(nil), original...), packetMetadata{outbound: true}); err != nil {
				t.Fatal(err)
			}
			output := expectInterceptedPacket(t, device)
			p, _ := parseIPPacket(output.data)
			if action == ActionDirect {
				if !output.meta.outbound || !bytes.Equal(output.data, original) {
					t.Fatal("DIRECT packet was not released unchanged")
				}
			} else if output.meta.outbound || p.Source != input.Destination || p.Destination != input.Source || p.TCPFlags&4 == 0 {
				t.Fatal("REJECT did not return a correctly addressed TCP reset")
			}
		})
	}
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionProxy}})
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{}, errors.New("unknown PID")
	}
	if err := i.handlePacket(interceptedSYN(false), packetMetadata{outbound: true}); err == nil {
		t.Fatal("TCP without process identity accepted")
	}
	reset := expectInterceptedPacket(t, device)
	if reset.meta.outbound {
		t.Fatal("unknown TCP escaped")
	}
	udp, _ := packetTestFixture(false, ProtoUDP, []byte("secret"), false)
	if err := i.handlePacket(udp, packetMetadata{outbound: true}); err == nil {
		t.Fatal("UDP without process identity accepted")
	}
	if len(device.sent) != 0 {
		t.Fatal("unknown UDP escaped")
	}
}

func TestInterceptorSelfAndExistingConnectionsBypass(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionReject}})
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{pid: uint32(os.Getpid()), path: "renamed-client.exe"}, nil
	}
	if err := i.handlePacket(interceptedSYN(false), packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	if !expectInterceptedPacket(t, device).meta.outbound {
		t.Fatal("agent's own traffic was intercepted")
	}
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		t.Error("existing TCP tried process lookup")
		return packetProcess{}, nil
	}
	data, offset := packetTestFixture(false, ProtoTCP, []byte("existing"), false)
	binary.BigEndian.PutUint16(data[offset:], 51000)
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	if !expectInterceptedPacket(t, device).meta.outbound {
		t.Fatal("existing connection was migrated midstream")
	}
}

func TestInterceptorPrivateListenerNeverLeaksPackets(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionProxy}})
	data := interceptedSYN(false)
	packet, _ := parseIPPacket(data)
	if err := rewriteIPPacket(data, packet.Source, netip.AddrPortFrom(packet.Destination.Addr(), i.ports[false])); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(data, packetMetadata{outbound: false}); err != nil {
		t.Fatal(err)
	}
	packet, _ = parseIPPacket(data)
	if err := rewriteIPPacket(data, netip.AddrPortFrom(packet.Source.Addr(), i.ports[false]), packet.Destination); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	if len(device.sent) != 0 {
		t.Fatal("unmapped listener or external inbound packet was injected")
	}
}

func TestInterceptorUDPFullReplyPath(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		t.Run(map[bool]string{false: "ipv4", true: "ipv6"}[ipv6], func(t *testing.T) {
			target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			go func() {
				buf := make([]byte, 100)
				n, addr, err := target.ReadFromUDP(buf)
				if err == nil {
					_, _ = target.WriteToUDP(append([]byte("reply:"), buf[:n]...), addr)
					_, _ = target.WriteToUDP([]byte("second"), addr)
				}
			}()
			var calls atomic.Int32
			dialer := &testDialer{udp: func(ctx context.Context, exit, host string, port uint16) (net.PacketConn, error) {
				calls.Add(1)
				if exit != "selected-exit" || port != 443 {
					return nil, errors.New("lost UDP routing decision")
				}
				return net.DialUDP("udp4", nil, target.LocalAddr().(*net.UDPAddr))
			}}
			i, device := newTestInterceptor(t, Options{Dialer: dialer, Config: Config{DefaultAction: ActionReject, Rules: []Rule{{Enabled: true, Process: "browser.exe", Action: ActionProxy, ExitID: "selected-exit"}}}})
			i.server.interceptor = i
			i.start()
			data, _ := packetTestFixture(ipv6, ProtoUDP, []byte("request"), false)
			original, _ := parseIPPacket(data)
			device.incoming <- capturedTestPacket{data: data, meta: packetMetadata{outbound: true, ifIndex: 55}}
			for _, expected := range []string{"reply:request", "second"} {
				response := expectInterceptedPacket(t, device)
				packet, err := parseIPPacket(response.data)
				if err != nil {
					t.Fatal(err)
				}
				if string(packet.Payload) != expected || packet.Source != original.Destination || packet.Destination != original.Source || response.meta.outbound || response.meta.ifIndex != 55 {
					t.Fatalf("bad UDP reply: %+v %+v", packet, response.meta)
				}
				packetTestAssertChecksums(t, packet.Bytes, packet.TransportOffset, ProtoUDP)
			}
			if calls.Load() != 1 {
				t.Fatal("UDP reply path opened more than one tunnel association")
			}
			closed := make(chan struct{})
			go func() { _ = i.server.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("interceptor close did not cancel workers/UDP replies")
			}
			if i.server.Running() {
				t.Fatal("closed interceptor still running")
			}
		})
	}
}

func TestInterceptorCaptureFailureClosesServer(t *testing.T) {
	i, device := newTestInterceptor(t, Options{})
	i.server.interceptor = i
	i.start()
	_ = device.Close()
	select {
	case <-i.server.done:
	case <-time.After(2 * time.Second):
		t.Fatal("capture failure left interception active")
	}
	if i.server.Running() {
		t.Fatal("failed capture still advertised as running")
	}
}

func TestInterceptorRelayDNSAndSelfBypassFullFlowTable(t *testing.T) {
	i, device := newTestInterceptor(t, Options{MaxUDPAssociations: 1, Config: Config{DefaultAction: ActionProxy}, Guard: LoopGuard{RelayHost: "relay.example"}})
	if _, err := i.server.ClassifyFlow(testFlow(ProtoUDP, nil)); err != nil {
		t.Fatal(err)
	}
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{pid: uint32(os.Getpid()), path: "agent.exe"}, nil
	}
	data, offset := packetTestFixture(false, ProtoUDP, []byte("tunnel traffic"), false)
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	if !expectInterceptedPacket(t, device).meta.outbound {
		t.Fatal("flow quota blocked the agent's own transport")
	}
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{}, errors.New("DNS service identity unavailable")
	}
	query := []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 5, 'r', 'e', 'l', 'a', 'y', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0, 0, 1, 0, 1}
	data, offset = packetTestFixture(false, ProtoUDP, query, false)
	binary.BigEndian.PutUint16(data[offset+2:], 53)
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	if !expectInterceptedPacket(t, device).meta.outbound {
		t.Fatal("relay DNS was sent into the tunnel it must establish")
	}
	data, offset = packetTestFixture(false, ProtoUDP, query, false)
	binary.BigEndian.PutUint16(data[offset+2:], 53)
	data[offset+8+13] = 'x'
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err == nil {
		t.Fatal("unrelated DNS was allowed to bypass policy")
	}
}

type interceptedTestConn struct {
	net.Conn
	local, remote netip.AddrPort
}

func (c interceptedTestConn) LocalAddr() net.Addr  { return net.TCPAddrFromAddrPort(c.local) }
func (c interceptedTestConn) RemoteAddr() net.Addr { return net.TCPAddrFromAddrPort(c.remote) }

type interceptedTestListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *interceptedTestListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *interceptedTestListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *interceptedTestListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero, Port: 45001} }

func TestInterceptorAcceptedTCPUsesOriginalRoute(t *testing.T) {
	upstream, target := net.Pipe()
	defer target.Close()
	var calls atomic.Int32
	dialer := &testDialer{tcp: func(ctx context.Context, exit, host string, port uint16) (net.Conn, error) {
		calls.Add(1)
		if exit != "process-exit" || host != "198.51.100.20" || port != 443 {
			return nil, errors.New("accepted TCP lost its original route")
		}
		return upstream, nil
	}}
	i, device := newTestInterceptor(t, Options{Dialer: dialer, Config: Config{DefaultAction: ActionReject, Rules: []Rule{{Enabled: true, Process: "browser.exe", Action: ActionProxy, ExitID: "process-exit"}}}})
	listener := &interceptedTestListener{connections: make(chan net.Conn, 1), done: make(chan struct{})}
	i.listeners = []net.Listener{listener}
	i.server.interceptor = i
	i.start()
	device.incoming <- capturedTestPacket{data: interceptedSYN(false), meta: packetMetadata{outbound: true, ifIndex: 7}}
	reflected := expectInterceptedPacket(t, device)
	nat, _ := parseIPPacket(reflected.data)
	if err := i.server.ReloadRules(Config{DefaultAction: ActionReject}); err != nil {
		t.Fatal(err)
	}
	application, accepted := net.Pipe()
	defer application.Close()
	_ = application.SetDeadline(time.Now().Add(3 * time.Second))
	_ = target.SetDeadline(time.Now().Add(3 * time.Second))
	listener.connections <- interceptedTestConn{Conn: accepted, local: nat.Destination, remote: nat.Source}
	echoDone := make(chan error, 1)
	go func() {
		request := make([]byte, 4)
		_, err := io.ReadFull(target, request)
		if err == nil {
			_, err = target.Write(request)
		}
		echoDone <- err
	}()
	if _, err := application.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(application, reply); err != nil {
		t.Fatal(err)
	}
	if string(reply) != "ping" || calls.Load() != 1 {
		t.Fatal("accepted TCP failed bidirectional tunnel forwarding")
	}
	if err := <-echoDone; err != nil {
		t.Fatal(err)
	}
	if err := i.server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Read(make([]byte, 1)); err == nil {
		t.Fatal("interceptor close left an accepted connection open")
	}
}

package divert

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// packetDevice deliberately has no policy or dialing responsibilities. The
// same interception loop is exercised with an in-memory device in tests.
type packetDevice interface {
	Receive([]byte) (int, packetMetadata, error)
	Send([]byte, packetMetadata) error
	Shutdown() error
	Close() error
}

// packetFinalizer is implemented by capture mechanisms, such as Linux
// NFQUEUE, that retain the original packet until userspace supplies a verdict.
// The finalizer is deliberately optional: WinDivert removes a packet at
// receive time and the in-memory test device has no pending kernel state.
type packetFinalizer interface {
	Finalize(packetMetadata) error
}

// borrowedPacketDevice lets a platform hand the interceptor the userspace
// buffer it already received. Linux NFQUEUE uses it to avoid a second full
// packet copy. The slice is valid until Finalize returns and must not be kept.
type borrowedPacketDevice interface {
	ReceivePacket() ([]byte, packetMetadata, error)
}

// packetAccepter provides a no-payload verdict for packets that were not
// rewritten. Besides avoiding a userspace-to-kernel copy, this preserves the
// kernel's checksum/GSO metadata for direct Linux traffic.
type packetAccepter interface {
	Accept(packetMetadata) error
}

type packetMetadata struct {
	outbound         bool
	capturedOutbound bool
	ifIndex          uint32
	subIfIndex       uint32
	platformToken    any
}

type packetProcess struct {
	pid  uint32
	path string
}

type processLookup func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error)

type tcpRedirect struct {
	route       *ClassifiedFlow
	original    FlowKey
	translated  FlowKey
	sequence    uint32
	lastSeen    time.Time
	finished    time.Time
	accepted    bool
	sentFIN     bool
	receivedFIN bool
	conn        net.Conn
	replyMeta   packetMetadata
}

type interceptedUDP struct {
	route   *ClassifiedFlow
	payload []byte
	meta    packetMetadata
}

type packetInterceptor struct {
	server    *Server
	device    packetDevice
	listeners []net.Listener
	lookup    processLookup
	ctx       context.Context
	cancel    context.CancelFunc
	running   atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup
	mu        sync.Mutex
	tcp       map[FlowKey]*tcpRedirect
	reverse   map[FlowKey]*tcpRedirect
	nextPort  uint16
	ports     map[bool]uint16 // false: IPv4, true: IPv6
	udpQueues []chan interceptedUDP
	logMu     sync.Mutex
	lastLog   time.Time
	dns       *dnsAssociations
}

type systemInterceptor interface {
	Running() bool
	ListenAddr() string
	Close()
}

func newPacketInterceptor(s *Server, device packetDevice, listeners []net.Listener, lookup processLookup) *packetInterceptor {
	ctx, cancel := context.WithCancel(s.ctx)
	i := &packetInterceptor{
		server: s, device: device, listeners: listeners, lookup: lookup,
		ctx: ctx, cancel: cancel, tcp: make(map[FlowKey]*tcpRedirect),
		reverse: make(map[FlowKey]*tcpRedirect), nextPort: 10000,
		ports: make(map[bool]uint16), udpQueues: make([]chan interceptedUDP, 8),
		dns: newDNSAssociations(),
	}
	for _, listener := range listeners {
		addr := listener.Addr().(*net.TCPAddr).AddrPort()
		i.ports[addr.Addr().Is6()] = addr.Port()
	}
	for n := range i.udpQueues {
		i.udpQueues[n] = make(chan interceptedUDP, 64)
	}
	return i
}

func (i *packetInterceptor) start() {
	i.running.Store(true)
	for _, queue := range i.udpQueues {
		i.wg.Add(1)
		go i.forwardDatagrams(queue)
	}
	for _, listener := range i.listeners {
		i.wg.Add(1)
		go i.acceptTCP(listener)
	}
	i.wg.Add(2)
	go i.receive()
	go i.sweepTCP()
}

func (i *packetInterceptor) Running() bool { return i != nil && i.running.Load() }

func (i *packetInterceptor) ListenAddr() string {
	if i == nil || len(i.listeners) == 0 {
		return ""
	}
	return i.listeners[0].Addr().String()
}

func (i *packetInterceptor) report(err error) {
	if err == nil || i.ctx.Err() != nil {
		return
	}
	i.logMu.Lock()
	defer i.logMu.Unlock()
	if time.Since(i.lastLog) >= time.Second {
		i.lastLog = time.Now()
		log.Printf("[divert] %v", err)
	}
}

func (i *packetInterceptor) receive() {
	defer i.wg.Done()
	borrowed, canBorrow := i.device.(borrowedPacketDevice)
	var buffer []byte
	if !canBorrow {
		buffer = make([]byte, 40+65535)
	}
	for {
		var data []byte
		var meta packetMetadata
		var err error
		if canBorrow {
			data, meta, err = borrowed.ReceivePacket()
		} else {
			var n int
			n, meta, err = i.device.Receive(buffer)
			if n > 0 && n <= len(buffer) {
				data = buffer[:n]
			}
		}
		if err != nil {
			if i.ctx.Err() == nil {
				i.report(fmt.Errorf("interception stopped: %w", err))
				i.running.Store(false)
				go i.server.Close()
			}
			return
		}
		if i.ctx.Err() != nil {
			return
		}
		if len(data) == 0 {
			i.report(errors.New("invalid captured packet length"))
			if finalizer, ok := i.device.(packetFinalizer); ok {
				i.report(finalizer.Finalize(meta))
			}
			continue
		}
		handleErr := i.handlePacket(data, meta)
		var finalizeErr error
		if finalizer, ok := i.device.(packetFinalizer); ok {
			finalizeErr = finalizer.Finalize(meta)
		}
		if err := errors.Join(handleErr, finalizeErr); err != nil {
			i.report(err)
		}
	}
}

func (i *packetInterceptor) handlePacket(data []byte, meta packetMetadata) error {
	if !meta.outbound {
		return i.inboundPacket(data, meta)
	}
	packet, err := parseIPPacket(data)
	if err != nil {
		return err // Never leak an unclassifiable packet through a PROXY rule.
	}
	if packet.Protocol == ProtoTCP {
		if port := i.ports[packet.Source.Addr().Is6()]; port != 0 && packet.Source.Port() == port {
			return i.returnTCP(packet, meta)
		}
	}
	if localOnlyPacket(packet) || relayDNSPacket(packet, i.server.guard.RelayHost) {
		return i.sendPacket(packet, meta)
	}
	if packet.Protocol == ProtoTCP {
		return i.outboundTCP(packet, meta)
	}
	process, err := i.lookup(packet.Protocol, packet.Source, packet.Destination)
	if err != nil {
		return fmt.Errorf("UDP process lookup for %s: %w", packet.Source, err)
	}
	flow := i.flowMetadata(packet, process)
	if i.server.guard.MustDirectFlow(flow) {
		return i.sendPacket(packet, meta)
	}
	route, err := i.server.ClassifyFlow(flow)
	if err != nil {
		return err
	}
	switch route.Decision().Action {
	case ActionDirect:
		err := i.sendPacket(packet, meta)
		if err == nil {
			route.traffic.Activate()
			route.traffic.AddUpload(len(packet.Payload))
		}
		return err
	case ActionReject:
		return nil
	case ActionProxy:
		// Hashing the whole tuple keeps each association ordered while a slow
		// tunnel dial cannot block the packet capture loop or TCP handshakes.
		queue := i.udpQueues[flowQueue(route.Key(), len(i.udpQueues))]
		job := interceptedUDP{route: route, payload: append([]byte(nil), packet.Payload...), meta: meta}
		select {
		case queue <- job:
			return nil
		case <-i.ctx.Done():
			return i.ctx.Err()
		default:
			return errors.New("UDP interception queue is full; datagram dropped")
		}
	default:
		return errors.New("invalid interception action")
	}
}

func localOnlyPacket(p ipPacket) bool {
	for _, addr := range []netip.Addr{p.Source.Addr(), p.Destination.Addr()} {
		if addr.IsLoopback() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
			return true
		}
	}
	return false
}

// Windows can send GetAddrInfo DNS queries from the DNS service's PID rather
// than the agent's PID. Keep relay-name resolution independent of the tunnel,
// including after its DNS cache expires during a reconnect.
func relayDNSPacket(p ipPacket, relayHost string) bool {
	if p.Protocol != ProtoUDP || p.Destination.Port() != 53 || relayHost == "" {
		return false
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(p.Payload)
	if err != nil || header.Response || header.OpCode != 0 {
		return false
	}
	questions, err := parser.AllQuestions()
	return err == nil && len(questions) == 1 &&
		strings.EqualFold(strings.TrimSuffix(questions[0].Name.String(), "."), strings.TrimSuffix(relayHost, "."))
}

func packetFlow(p ipPacket, process packetProcess) Flow {
	return Flow{Process: process.path, ProcessID: process.pid, Protocol: p.Protocol,
		SourceIP: p.Source.Addr().String(), SourcePort: p.Source.Port(),
		IP: p.Destination.Addr().String(), Port: p.Destination.Port()}
}

func flowQueue(key FlowKey, count int) int {
	var hash uint32 = 2166136261
	for _, endpoint := range []netip.AddrPort{key.Source, key.Destination} {
		bytes := endpoint.Addr().As16()
		for _, b := range bytes {
			hash = (hash ^ uint32(b)) * 16777619
		}
		hash = (hash ^ uint32(endpoint.Port())) * 16777619
	}
	return int(hash % uint32(count))
}

func (i *packetInterceptor) outboundTCP(p ipPacket, meta packetMetadata) error {
	key := FlowKey{Protocol: ProtoTCP, Source: p.Source, Destination: p.Destination}
	syn := p.TCPFlags&0x12 == 0x02
	i.mu.Lock()
	flow := i.tcp[key]
	if flow != nil && syn && flow.sequence != p.TCPSequence {
		// A reused client port gets a fresh virtual port. The old reverse map
		// stays quarantined so delayed FIN/RST cannot reach the new connection.
		delete(i.tcp, key)
		flow.finished = time.Now()
		flow.route.traffic.Finish("closed", nil)
		if flow.conn != nil {
			_ = flow.conn.Close()
		}
		flow = nil
	}
	i.mu.Unlock()
	if flow == nil {
		if !syn {
			// TCP sessions established before activation cannot be migrated.
			return i.sendPacket(p, meta)
		}
		process, err := i.lookup(ProtoTCP, p.Source, p.Destination)
		if err != nil {
			_ = i.rejectTCP(p, meta)
			return fmt.Errorf("TCP process lookup for %s: %w", p.Source, err)
		}
		metadata := i.flowMetadata(p, process)
		if i.server.guard.MustDirectFlow(metadata) {
			return i.sendPacket(p, meta)
		}
		route, err := i.server.ClassifyFlow(metadata)
		if err != nil {
			_ = i.rejectTCP(p, meta)
			return err
		}
		i.mu.Lock()
		flow, err = i.registerTCP(route, p.TCPSequence, meta)
		i.mu.Unlock()
		if err != nil {
			route.traffic.Finish("failed", err)
			_ = i.rejectTCP(p, meta)
			return err
		}
	}
	i.mu.Lock()
	flow.lastSeen = time.Now()
	if p.TCPFlags&0x04 != 0 {
		flow.finished = time.Now()
	}
	i.mu.Unlock()
	switch flow.route.Decision().Action {
	case ActionDirect:
		err := i.sendPacket(p, meta)
		if err == nil {
			i.trackDirectTCP(flow, p, true)
		}
		return err
	case ActionReject:
		return i.rejectTCP(p, meta)
	case ActionProxy:
		if err := rewriteIPPacket(p.Bytes, flow.translated.Source, flow.translated.Destination); err != nil {
			return err
		}
		meta.outbound = false
		return i.device.Send(p.Bytes, meta)
	default:
		return errors.New("invalid TCP interception action")
	}
}

// registerTCP is called under i.mu, and bounds both live flows and quarantined
// virtual endpoints. Backpressure rejects new connections instead of bypassing.
func (i *packetInterceptor) registerTCP(route *ClassifiedFlow, sequence uint32, meta packetMetadata) (*tcpRedirect, error) {
	if len(i.tcp)+len(i.reverse) >= i.server.opts.MaxTCPFlows*4 {
		return nil, ErrFlowCapacity
	}
	meta.outbound = false
	flow := &tcpRedirect{route: route, original: route.Key(), sequence: sequence, lastSeen: time.Now(), replyMeta: meta}
	if route.Decision().Action == ActionProxy {
		port := i.ports[flow.original.Source.Addr().Is6()]
		if port == 0 {
			return nil, errors.New("no TCP interceptor for address family")
		}
		for attempts := 0; ; attempts++ {
			if attempts >= 64512 {
				return nil, ErrFlowCapacity
			}
			i.nextPort++
			if i.nextPort < 1024 {
				i.nextPort = 1024
			}
			flow.translated = FlowKey{Protocol: ProtoTCP,
				Source:      netip.AddrPortFrom(flow.original.Destination.Addr(), i.nextPort),
				Destination: netip.AddrPortFrom(flow.original.Source.Addr(), port)}
			if i.reverse[flow.translated] == nil {
				break
			}
		}
		i.reverse[flow.translated] = flow
	}
	i.tcp[flow.original] = flow
	return flow, nil
}

func (i *packetInterceptor) returnTCP(p ipPacket, meta packetMetadata) error {
	key := FlowKey{Protocol: ProtoTCP, Source: p.Destination, Destination: p.Source}
	i.mu.Lock()
	flow := i.reverse[key]
	if flow != nil {
		flow.lastSeen = time.Now()
	}
	i.mu.Unlock()
	if flow == nil {
		return nil // No listener-generated packet may escape to the Internet.
	}
	if err := rewriteIPPacket(p.Bytes, flow.original.Destination, flow.original.Source); err != nil {
		return err
	}
	return i.device.Send(p.Bytes, flow.replyMeta)
}

func (i *packetInterceptor) rejectTCP(p ipPacket, meta packetMetadata) error {
	response, err := makeTCPReset(p)
	if err != nil || response == nil {
		return err
	}
	meta.outbound = false
	return i.device.Send(response, meta)
}

func (i *packetInterceptor) sendPacket(packet ipPacket, meta packetMetadata) error {
	if accepter, ok := i.device.(packetAccepter); ok {
		if err := accepter.Accept(meta); err != nil {
			return err
		}
		if meta.outbound && packet.Protocol == ProtoUDP {
			i.dns.query(packet.Source, packet.Destination, packet.Payload)
		}
		return nil
	}
	// Outbound captures can contain hardware-offloaded, unfinished checksums.
	repairPacketChecksums(packet)
	if err := i.device.Send(packet.Bytes, meta); err != nil {
		return err
	}
	if meta.outbound && packet.Protocol == ProtoUDP {
		i.dns.query(packet.Source, packet.Destination, packet.Payload)
	}
	return nil
}

func (i *packetInterceptor) forwardDatagrams(queue <-chan interceptedUDP) {
	defer i.wg.Done()
	for {
		select {
		case <-i.ctx.Done():
			return
		case job := <-queue:
			meta := job.meta
			meta.outbound = false
			i.dns.query(job.route.key.Source, job.route.key.Destination, job.payload)
			err := i.server.ForwardUDP(i.ctx, job.route, job.payload, func(ctx context.Context, key FlowKey, payload []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				response, err := makeUDPReply(key, payload)
				if err != nil {
					return err
				}
				if err := i.device.Send(response, meta); err != nil {
					return err
				}
				i.dns.response(key.Destination, key.Source, payload)
				return nil
			})
			i.report(err)
		}
	}
}

func (i *packetInterceptor) acceptTCP(listener net.Listener) {
	defer i.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if i.ctx.Err() == nil {
				i.report(fmt.Errorf("TCP interceptor stopped: %w", err))
				go i.server.Close()
			}
			return
		}
		remote := normalizedTCPAddr(conn.RemoteAddr())
		local := normalizedTCPAddr(conn.LocalAddr())
		key := FlowKey{Protocol: ProtoTCP, Source: remote, Destination: local}
		i.mu.Lock()
		flow := i.reverse[key]
		if i.ctx.Err() != nil || flow == nil || flow.accepted || !flow.finished.IsZero() {
			i.mu.Unlock()
			_ = conn.Close()
			continue
		}
		flow.accepted, flow.conn = true, conn
		i.wg.Add(1)
		i.mu.Unlock()
		go func() {
			defer i.wg.Done()
			err := i.server.ForwardTCP(i.ctx, flow.route, conn)
			if err != nil {
				flow.route.traffic.Finish("failed", err)
			}
			i.report(err)
			i.mu.Lock()
			flow.finished = time.Now()
			flow.conn = nil
			i.mu.Unlock()
		}()
	}
}

func normalizedTCPAddr(addr net.Addr) netip.AddrPort {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}
	p := tcp.AddrPort()
	return netip.AddrPortFrom(p.Addr().Unmap().WithZone(""), p.Port())
}

func (i *packetInterceptor) sweepTCP() {
	defer i.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-i.ctx.Done():
			return
		case now := <-ticker.C:
			i.sweepDirectTCP(now)
			i.mu.Lock()
			for key, flow := range i.tcp {
				if flow.route.Decision().Action == ActionDirect && flow.finished.IsZero() {
					continue
				}
				if (!flow.finished.IsZero() && now.Sub(flow.finished) > 2*time.Minute) ||
					(!flow.accepted && now.Sub(flow.lastSeen) > 2*time.Minute) {
					flow.route.traffic.Finish("closed", nil)
					delete(i.tcp, key)
				}
			}
			for key, flow := range i.reverse {
				if (!flow.finished.IsZero() && now.Sub(flow.finished) > 2*time.Minute) ||
					(!flow.accepted && now.Sub(flow.lastSeen) > 2*time.Minute) {
					delete(i.reverse, key)
				}
			}
			i.mu.Unlock()
		}
	}
}

func (i *packetInterceptor) Close() {
	i.closeOnce.Do(func() {
		i.running.Store(false)
		i.cancel()
		for _, listener := range i.listeners {
			_ = listener.Close()
		}
		i.mu.Lock()
		for _, flow := range i.tcp {
			flow.route.traffic.Finish("closed", nil)
		}
		for _, flow := range i.reverse {
			if flow.conn != nil {
				_ = flow.conn.Close()
			}
		}
		i.mu.Unlock()
		_ = i.device.Shutdown()
		i.wg.Wait()
		_ = i.device.Close()
	})
}

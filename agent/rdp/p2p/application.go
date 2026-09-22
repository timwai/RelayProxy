package p2p

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/rdp/candidate"
	"relayproxy/internal/rdp/punch"
	"relayproxy/internal/rdp/secure"
	"relayproxy/internal/tunnel"
)

const applicationQueueDepth = 128

type ApplicationHandler func(*Session, *ApplicationPath)

// ApplicationPath exposes one authenticated P2P UDP lease to an application.
// Signaling, candidate discovery, punching, fragmentation, HMAC and replay
// protection remain owned by the existing P2P manager.
type ApplicationPath struct {
	session *Session

	incoming chan []byte
	done     chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
	wire      []byte
}

func newApplicationPath(session *Session) *ApplicationPath {
	return &ApplicationPath{
		session:  session,
		incoming: make(chan []byte, applicationQueueDepth),
		done:     make(chan struct{}),
		wire:     make([]byte, 0, secure.MaxDataPayload+40),
	}
}

func (p *ApplicationPath) Name() string { return "udp_p2p" }

func (p *ApplicationPath) Send(ctx context.Context, payload []byte) error {
	if p == nil || p.session == nil {
		return net.ErrClosed
	}
	if len(payload) == 0 {
		return nil
	}
	if len(payload) > protocol.MaxUDPDatagramPayload {
		return errors.New("P2P application datagram exceeds UDP payload limit")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	session := p.session
	var conn *net.UDPConn
	var remote netip.AddrPort
	var encode *secure.DataCodec
	for {
		session.mu.Lock()
		conn = session.udp
		remote = session.remotePort
		encode = session.udpEncode
		wake := session.remoteWake
		session.mu.Unlock()
		if conn == nil || encode == nil {
			return net.ErrClosed
		}
		if remote.IsValid() {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return net.ErrClosed
		case <-session.closed:
			return net.ErrClosed
		case <-wake:
		}
	}

	packetID := session.udpPacketID.Add(1)
	wire := p.wire
	err := punch.FragmentUDP(packetID, payload, func(frame []byte) error {
		var encodeErr error
		wire, encodeErr = encode.EncodeTo(wire[:0], frame)
		if encodeErr != nil {
			return encodeErr
		}
		_, writeErr := conn.WriteToUDPAddrPort(wire, remote)
		return writeErr
	})
	p.wire = wire
	if err == nil {
		session.pathUDP.Store("udp_p2p")
	}
	return err
}

func (p *ApplicationPath) Receive(ctx context.Context) ([]byte, error) {
	if p == nil {
		return nil, net.ErrClosed
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, net.ErrClosed
	case payload := <-p.incoming:
		return payload, nil
	}
}

func (p *ApplicationPath) deliver(payload []byte) {
	if p == nil || len(payload) == 0 {
		return
	}
	copyPayload := append([]byte(nil), payload...)
	select {
	case <-p.done:
		return
	case p.incoming <- copyPayload:
		return
	default:
	}
	// Prefer the newest interactive media when the consumer falls behind.
	select {
	case <-p.incoming:
	default:
	}
	select {
	case <-p.done:
	case p.incoming <- copyPayload:
	default:
	}
}

func (p *ApplicationPath) closeLocal() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() { close(p.done) })
}

func (p *ApplicationPath) Close() error {
	if p == nil {
		return nil
	}
	if p.session != nil {
		return p.session.Close()
	}
	p.closeLocal()
	return nil
}

func (m *Manager) SetApplicationHandler(purpose string, handler ApplicationHandler) {
	if m == nil || purpose == "" {
		return
	}
	m.mu.Lock()
	if m.applicationHandlers == nil {
		m.applicationHandlers = make(map[string]ApplicationHandler)
	}
	if handler == nil {
		delete(m.applicationHandlers, purpose)
	} else {
		m.applicationHandlers[purpose] = handler
	}
	sessions := make([]*Session, 0)
	if handler != nil {
		for _, session := range m.sessions {
			if session.Purpose == purpose {
				sessions = append(sessions, session)
			}
		}
	}
	m.mu.Unlock()
	for _, session := range sessions {
		m.notifyApplicationReady(session)
	}
}

func (m *Manager) notifyApplicationReady(session *Session) {
	if m == nil || session == nil {
		return
	}
	session.mu.Lock()
	if session.appNotified || session.applicationPath == nil || !session.remotePort.IsValid() {
		session.mu.Unlock()
		return
	}
	path := session.applicationPath
	purpose := session.Purpose
	session.mu.Unlock()

	m.mu.Lock()
	handler := m.applicationHandlers[purpose]
	m.mu.Unlock()
	if handler == nil {
		return
	}

	session.mu.Lock()
	if session.appNotified || session.applicationPath != path || !session.remotePort.IsValid() {
		session.mu.Unlock()
		return
	}
	session.appNotified = true
	session.mu.Unlock()
	go handler(session, path)
}

func (m *Manager) startTargetApplicationUDP(item *Session) (err error) {
	if item == nil {
		return errors.New("nil P2P application session")
	}
	item.mu.Lock()
	select {
	case <-item.closed:
		item.mu.Unlock()
		return net.ErrClosed
	default:
	}
	if item.udp != nil || item.udpStarting {
		item.mu.Unlock()
		return nil
	}
	item.udpStarting = true
	item.mu.Unlock()
	defer func() {
		if err != nil {
			item.mu.Lock()
			item.udpStarting = false
			item.mu.Unlock()
		}
	}()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	tunnel.TuneUDPConn(conn)
	encode, err := secure.NewDataCodec(item.ID, item.Token)
	if err != nil {
		_ = conn.Close()
		return err
	}
	decode, err := secure.NewDataCodec(item.ID, item.Token)
	if err != nil {
		_ = conn.Close()
		return err
	}
	path := newApplicationPath(item)

	item.mu.Lock()
	select {
	case <-item.closed:
		item.udpStarting = false
		item.mu.Unlock()
		path.closeLocal()
		_ = conn.Close()
		return net.ErrClosed
	default:
	}
	if item.udp != nil {
		item.udpStarting = false
		item.mu.Unlock()
		path.closeLocal()
		_ = conn.Close()
		return nil
	}
	item.udp = conn
	item.udpEncode = encode
	item.udpDecode = decode
	item.udpWire = make([]byte, secure.MaxDataPayload+40)
	item.applicationPath = path
	item.udpStarting = false
	item.mu.Unlock()

	udpCandidates := candidate.Discover(conn.LocalAddr().(*net.UDPAddr).Port, 0)
	m.mu.Lock()
	rendezvous := m.rendezvous
	m.mu.Unlock()
	if rendezvous != "" {
		probeCtx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
		if reflexive, probeErr := candidate.ProbeReflexive(probeCtx, rendezvous, conn, "udp"); probeErr == nil {
			udpCandidates = append(udpCandidates, reflexive)
		}
		cancel()
	}
	if len(udpCandidates) > 0 {
		item.publishCandidates("udp", udpCandidates)
	}
	go m.targetApplicationUDPReadLoop(item, path)
	return nil
}

func (m *Manager) targetApplicationUDPReadLoop(item *Session, path *ApplicationPath) {
	buffer := make([]byte, secure.MaxDataPayload+64)
	fragment := make([]byte, protocol.UDPFragmentHeaderSize+protocol.UDPFragmentPayload)
	assembled := make([]byte, protocol.MaxUDPDatagramPayload)

	for {
		item.mu.Lock()
		conn, remotePort, decode := item.udp, item.remotePort, item.udpDecode
		item.mu.Unlock()
		if conn == nil || decode == nil {
			return
		}
		n, source, err := conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			return
		}
		source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())

		packet, punchErr := secure.DecodePunchPacket(buffer[:n], item.Token)
		if punchErr == nil && packet.SessionID == item.ID {
			if packet.Type == secure.PunchRequest || packet.Type == secure.PunchKeep {
				sourceAddr := net.UDPAddrFromAddrPort(source)
				_ = punch.WritePunchAck(conn, sourceAddr, packet, item.Token)
				item.setRemote(sourceAddr, source)
				continue
			}
		}
		if !remotePort.IsValid() || source != remotePort {
			continue
		}
		decoded, err := decode.DecodeTo(buffer[:n], fragment)
		if err != nil {
			continue
		}
		item.mu.Lock()
		reassembler := item.udpReassembler
		item.mu.Unlock()
		if reassembler == nil {
			return
		}
		assembledN, complete, err := reassembler.Feed(fragment[:decoded], assembled)
		if err != nil || !complete {
			continue
		}
		path.deliver(assembled[:assembledN])
		item.pathUDP.Store("udp_p2p")
	}
}

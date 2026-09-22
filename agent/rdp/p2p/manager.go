// Package p2p implements the optional RDP direct path.  It is deliberately
// layered below agent/rdp: the existing relay dialers remain the safe fallback
// when NAT traversal or native datagrams are unavailable.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/rdp/candidate"
	"relayproxy/internal/rdp/punch"
	"relayproxy/internal/rdp/secure"
	"relayproxy/internal/tunnel"
)

const maxP2PTCPConnections = 128

type ControlSender func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error)

type Manager struct {
	ctx        context.Context
	cancel     context.CancelFunc
	send       ControlSender
	targetAddr string
	lease      time.Duration
	mu         sync.Mutex
	tcp        net.Listener
	udp        *net.UDPConn
	candidates []protocol.RDPCandidate
	sessions   map[uint64]*Session
	tcpSem     chan struct{}
	rendezvous string
	closed     atomic.Bool
	targetMode atomic.Bool

	applicationHandlers map[string]ApplicationHandler
}

type Session struct {
	manager         *Manager
	ID              uint64
	Purpose         string
	ControllerID    string
	TargetID        string
	Token           []byte
	ExpiresAt       atomic.Int64
	mu              sync.Mutex
	candidates      []protocol.RDPCandidate
	localCandidates []protocol.RDPCandidate
	remote          *net.UDPAddr
	remotePort      netip.AddrPort
	udp             *net.UDPConn
	localUDP        *net.UDPConn
	udpStarting     bool
	udpEncode       *secure.DataCodec
	udpDecode       *secure.DataCodec
	udpWire         []byte
	udpPacketID     atomic.Uint32
	udpReassembler  *punch.Reassembler
	directConns     map[net.Conn]struct{}
	directPackets   map[net.PacketConn]struct{}
	candidateWake   chan struct{}
	remoteWake      chan struct{}
	closed          chan struct{}
	closeOnce       sync.Once
	onClose         func()
	pathTCP         atomic.Value // string
	pathUDP         atomic.Value // string

	applicationPath *ApplicationPath
	appNotified     bool

	directMetricsMu sync.Mutex
	directRTTMs     float64
	directJitterMs  float64
	directLastRTTMs float64
	directSamples   uint64
}

type DirectPathMetrics struct {
	RTTMs    float64
	JitterMs float64
	Samples  uint64
}

func NewManager(parent context.Context, send ControlSender, targetAddress string, lease time.Duration, rendezvous string) *Manager {
	if lease < 15*time.Second {
		lease = 60 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{
		ctx: ctx, cancel: cancel, send: send, targetAddr: targetAddress, lease: lease, rendezvous: rendezvous,
		sessions: make(map[uint64]*Session), tcpSem: make(chan struct{}, maxP2PTCPConnections),
		applicationHandlers: make(map[string]ApplicationHandler),
	}
	m.targetMode.Store(true)
	return m
}

// SetTargetMode controls whether this Agent exposes an authenticated TCP
// listener for inbound P2P RDP. Controllers keep only an outbound UDP socket;
// publishing a TCP candidate there would create an unnecessary reverse path
// to the controller's own local RDP service.
func (m *Manager) SetTargetMode(enabled bool) {
	if m != nil {
		m.targetMode.Store(enabled)
	}
}

func (m *Manager) Start() error {
	if m == nil || m.send == nil {
		return errors.New("RDP P2P manager requires a control sender")
	}
	if m.closed.Load() {
		return errors.New("RDP P2P manager is closed")
	}
	var tcp net.Listener
	var err error
	if m.targetMode.Load() {
		tcp, err = net.Listen("tcp", ":0")
		if err != nil {
			return err
		}
	}
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		if tcp != nil {
			_ = tcp.Close()
		}
		return err
	}
	tunnel.TuneUDPConn(udp)
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		if tcp != nil {
			_ = tcp.Close()
		}
		_ = udp.Close()
		return net.ErrClosed
	}
	if m.tcpSem == nil {
		m.tcpSem = make(chan struct{}, maxP2PTCPConnections)
	}
	m.tcp, m.udp = tcp, udp
	m.candidates = candidate.Discover(udpPort(udp), tcpPort(tcp))
	m.mu.Unlock()
	if m.rendezvous != "" {
		probeCtx, cancel := context.WithTimeout(m.ctx, 2*time.Second)
		if reflexive, err := candidate.ProbeReflexive(probeCtx, m.rendezvous, udp, "udp"); err == nil {
			m.mu.Lock()
			m.candidates = append(m.candidates, reflexive)
			m.mu.Unlock()
		}
		cancel()
	}
	if tcp != nil {
		go m.acceptTCP()
	}
	// The manager's UDP socket is reserved for controller associations. Target
	// notifications use an isolated per-session socket so one reader can never
	// steal another session's authenticated datagram.
	registerCtx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	response, err := m.send(registerCtx, protocol.RDPControlMessage{Type: protocol.RDPControlRegister, Candidates: m.currentCandidates()})
	cancel()
	if err != nil {
		_ = m.Close()
		return err
	}
	if response.Type == protocol.RDPControlError {
		_ = m.Close()
		return fmt.Errorf("RDP candidate registration failed: %s", response.ErrorMessage)
	}
	if response.RendezvousAddress != "" {
		m.mu.Lock()
		m.rendezvous = response.RendezvousAddress
		if response.LeaseSec > 0 {
			m.lease = time.Duration(response.LeaseSec) * time.Second
		}
		m.mu.Unlock()
	}
	return nil
}

func (m *Manager) Close() error {
	if m == nil || m.closed.Swap(true) {
		return nil
	}
	m.cancel()
	m.mu.Lock()
	tcp, udp := m.tcp, m.udp
	sessions := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		sessions = append(sessions, item)
	}
	m.sessions = make(map[uint64]*Session)
	m.mu.Unlock()
	if tcp != nil {
		_ = tcp.Close()
	}
	if udp != nil {
		_ = udp.Close()
	}
	for _, item := range sessions {
		item.closeLocal()
	}
	return nil
}

func (m *Manager) Candidates() []protocol.RDPCandidate {
	return m.currentCandidates()
}

func (m *Manager) currentCandidates() []protocol.RDPCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]protocol.RDPCandidate(nil), m.candidates...)
}

func (m *Manager) StartController(ctx context.Context, targetID string) (*Session, error) {
	return m.StartControllerForPurpose(ctx, targetID, protocol.P2PPurposeRDP)
}

func (m *Manager) StartControllerForPurpose(ctx context.Context, targetID, purpose string) (*Session, error) {
	if m == nil || m.send == nil {
		return nil, errors.New("P2P manager requires a control sender")
	}
	if m.closed.Load() {
		return nil, net.ErrClosed
	}
	if targetID == "" {
		return nil, errors.New("P2P target id is required")
	}
	if purpose == "" {
		purpose = protocol.P2PPurposeRDP
	}
	if purpose != protocol.P2PPurposeRDP && purpose != protocol.P2PPurposeDesktopMedia {
		return nil, errors.New("unsupported P2P session purpose")
	}
	response, err := m.send(ctx, protocol.RDPControlMessage{
		Type: protocol.RDPControlConnectRequest, Purpose: purpose,
		TargetID: targetID, Candidates: m.currentCandidates(),
	})
	if err != nil {
		return nil, err
	}
	if response.Type == protocol.RDPControlError {
		return nil, fmt.Errorf("P2P direct session rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}
	if response.Type != protocol.RDPControlConnectResponse || response.SessionID == 0 || len(response.SessionToken) < 16 {
		return nil, errors.New("P2P coordinator returned an incomplete session")
	}
	if response.Purpose == "" {
		response.Purpose = purpose
	}
	item := m.newSession(
		response.SessionID, response.Purpose, response.ControllerID, response.TargetID,
		response.SessionToken, response.Candidates, response.LeaseExpiresAt,
	)
	if item == nil {
		return nil, net.ErrClosed
	}
	item.pathTCP.Store("relay")
	item.pathUDP.Store("relay")
	return item, nil
}

// HandleControl consumes a server-pushed notification. It is called by the
// single incoming-stream dispatcher in agent/app, so it never competes with a
// second AcceptStream reader.
func (m *Manager) HandleControl(message protocol.RDPControlMessage) {
	if m == nil || m.closed.Load() {
		return
	}
	switch message.Type {
	case protocol.RDPControlConnectNotify:
		if message.SessionID == 0 || len(message.SessionToken) < 16 {
			return
		}
		purpose := message.Purpose
		if purpose == "" {
			purpose = protocol.P2PPurposeRDP
		}
		item := m.newSession(
			message.SessionID, purpose, message.ControllerID, message.TargetID,
			message.SessionToken, message.Candidates, message.LeaseExpiresAt,
		)
		if item == nil {
			return
		}
		var err error
		if purpose == protocol.P2PPurposeDesktopMedia {
			err = m.startTargetApplicationUDP(item)
		} else {
			err = m.startTargetUDP(item)
		}
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
			log.Printf("[P2P] target UDP direct path purpose=%s unavailable: %v", purpose, err)
		}
	case protocol.RDPControlCandidateUpdate:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil {
			item.setCandidates(message.Candidates, false)
		}
	case protocol.RDPControlLeaseAck:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil && message.LeaseExpiresAt > 0 {
			item.ExpiresAt.Store(message.LeaseExpiresAt)
		}
	case protocol.RDPControlSessionClose:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		delete(m.sessions, message.SessionID)
		m.mu.Unlock()
		if item != nil {
			item.closeLocal()
		}
	}
}

func (m *Manager) newSession(id uint64, purpose, controllerID, targetID string, token []byte, candidates []protocol.RDPCandidate, expires int64) *Session {
	if expires == 0 {
		expires = time.Now().Add(m.lease).UnixMilli()
	}
	item := &Session{
		manager: m, ID: id, Purpose: purpose, ControllerID: controllerID, TargetID: targetID,
		Token: append([]byte(nil), token...), candidates: append([]protocol.RDPCandidate(nil), candidates...),
		localCandidates: m.currentCandidates(), directConns: make(map[net.Conn]struct{}),
		directPackets: make(map[net.PacketConn]struct{}), candidateWake: make(chan struct{}),
		remoteWake: make(chan struct{}), closed: make(chan struct{}), udpReassembler: punch.NewReassembler(),
	}
	item.ExpiresAt.Store(expires)
	item.pathTCP.Store("relay")
	item.pathUDP.Store("relay")
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		item.closeLocal()
		return nil
	}
	if m.sessions == nil {
		m.sessions = make(map[uint64]*Session)
	}
	if old := m.sessions[id]; old != nil {
		old.closeLocal()
	}
	m.sessions[id] = item
	m.mu.Unlock()
	go item.renewLoop()
	return item
}

func (m *Manager) acceptTCP() {
	for {
		conn, err := m.tcp.Accept()
		if err != nil {
			if m.closed.Load() {
				return
			}
			continue
		}
		tunnel.TuneTCPConn(conn)
		select {
		case m.tcpSem <- struct{}{}:
			go func() {
				defer func() { <-m.tcpSem }()
				m.handleTCP(conn)
			}()
		default:
			// Do not let unauthenticated punch attempts consume an unbounded
			// number of goroutines and file descriptors while waiting for the
			// two-second handshake timeout.
			_ = conn.Close()
		}
	}
}

func (m *Manager) handleTCP(conn net.Conn) {
	accepted, id, err := punch.Accept(m.ctx, conn, func(sessionID uint64) ([]byte, bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		item := m.sessions[sessionID]
		if item == nil || item.isExpired() || item.Purpose != protocol.P2PPurposeRDP {
			return nil, false
		}
		return append([]byte(nil), item.Token...), true
	}, 2*time.Second)
	if err != nil {
		return
	}
	m.mu.Lock()
	item := m.sessions[id]
	m.mu.Unlock()
	if item == nil || item.TargetID == "" {
		_ = accepted.Close()
		return
	}
	local, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(m.ctx, "tcp", m.targetAddr)
	if err != nil {
		_ = accepted.Close()
		return
	}
	tunnel.TuneTCPConn(local)
	accepted = item.trackConn(accepted)
	local = item.trackConn(local)
	if accepted == nil || local == nil {
		if accepted != nil {
			_ = accepted.Close()
		}
		if local != nil {
			_ = local.Close()
		}
		return
	}
	item.pathTCP.Store("tcp_p2p")
	_, _ = tunnel.Pipe(m.ctx, accepted, local, 30*time.Minute, nil)
	_ = accepted.Close()
	_ = local.Close()
}

func (m *Manager) startTargetUDP(item *Session) (err error) {
	if item == nil {
		return errors.New("nil RDP session")
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
	localAddr, err := net.ResolveUDPAddr("udp", m.targetAddr)
	if err != nil {
		_ = conn.Close()
		return err
	}
	local, err := net.DialUDP("udp", nil, localAddr)
	if err != nil {
		_ = conn.Close()
		return err
	}
	tunnel.TuneUDPConn(local)
	encode, err := secure.NewDataCodec(item.ID, item.Token)
	if err != nil {
		_ = conn.Close()
		_ = local.Close()
		return err
	}
	decode, err := secure.NewDataCodec(item.ID, item.Token)
	if err != nil {
		_ = conn.Close()
		_ = local.Close()
		return err
	}
	item.mu.Lock()
	select {
	case <-item.closed:
		item.udpStarting = false
		item.mu.Unlock()
		_ = conn.Close()
		_ = local.Close()
		return net.ErrClosed
	default:
	}
	if item.udp != nil {
		item.udpStarting = false
		item.mu.Unlock()
		_ = conn.Close()
		_ = local.Close()
		return nil
	}
	item.udp, item.localUDP, item.udpEncode, item.udpDecode = conn, local, encode, decode
	item.udpWire = make([]byte, secure.MaxDataPayload+40)
	item.udpStarting = false
	item.mu.Unlock()

	// CandidateUpdate is sent after the per-session socket exists. The
	// coordinator forwards it to the controller and keeps the token in memory.
	// A per-session socket has a different NAT mapping from the registration
	// socket, so probe the current port instead of reusing a stale candidate.
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
		select {
		case <-item.closed:
			return net.ErrClosed
		default:
		}
		item.publishCandidates("udp", udpCandidates)
	}
	go m.targetUDPReadLoop(item)
	go m.targetUDPLocalReadLoop(item)
	return nil
}

func (m *Manager) targetUDPReadLoop(item *Session) {
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
		packet, err := secure.DecodePunchPacket(buffer[:n], item.Token)
		if err == nil && packet.SessionID == item.ID {
			if packet.Type == secure.PunchRequest {
				sourceAddr := net.UDPAddrFromAddrPort(source)
				_ = punch.WritePunchAck(conn, sourceAddr, packet, item.Token)
				item.setRemote(sourceAddr, source)
				continue
			}
			if packet.Type == secure.PunchKeep {
				sourceAddr := net.UDPAddrFromAddrPort(source)
				_ = punch.WritePunchAck(conn, sourceAddr, packet, item.Token)
				// A suspended laptop or a NAT rebinding can change the controller's
				// source port. The authenticated keepalive is sufficient proof to
				// refresh the fixed peer used for target-to-controller traffic.
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
		item.mu.Lock()
		local := item.localUDP
		item.mu.Unlock()
		if local != nil {
			_, _ = local.Write(assembled[:assembledN])
			item.pathUDP.Store("udp_p2p")
		}
	}
}

func (m *Manager) targetUDPLocalReadLoop(item *Session) {
	// The direct path fragments before authentication, so the complete UDP
	// payload limit applies here. Keeping the old secure-wire limit would
	// silently truncate a valid 65507-byte local datagram before fragmentation.
	buffer := make([]byte, protocol.MaxUDPDatagramPayload)
	for {
		item.mu.Lock()
		local, conn, encode, wire := item.localUDP, item.udp, item.udpEncode, item.udpWire
		item.mu.Unlock()
		if local == nil || conn == nil || encode == nil {
			return
		}
		n, err := local.Read(buffer)
		if err != nil {
			return
		}
		item.mu.Lock()
		remote, remoteWake := item.remote, item.remoteWake
		item.mu.Unlock()
		if remote == nil {
			for remote == nil {
				select {
				case <-remoteWake:
					item.mu.Lock()
					remote, remoteWake = item.remote, item.remoteWake
					item.mu.Unlock()
				case <-item.closed:
					return
				}
			}
		}
		packetID := item.udpPacketID.Add(1)
		_ = punch.FragmentUDP(packetID, buffer[:n], func(frame []byte) error {
			var encodeErr error
			wire, encodeErr = encode.EncodeTo(wire[:0], frame)
			if encodeErr != nil {
				return encodeErr
			}
			_, writeErr := conn.WriteToUDPAddrPort(wire, remote.AddrPort())
			return writeErr
		})
		item.mu.Lock()
		item.udpWire = wire
		item.mu.Unlock()
	}
}

func (s *Session) setRemote(remote *net.UDPAddr, remotePort netip.AddrPort) {
	s.mu.Lock()
	if s.remotePort == remotePort {
		s.remote = remote
		s.mu.Unlock()
		if s.manager != nil {
			s.manager.notifyApplicationReady(s)
		}
		return
	}
	s.remote, s.remotePort = remote, remotePort
	wake := s.remoteWake
	s.remoteWake = make(chan struct{})
	s.mu.Unlock()
	close(wake)
	if s.manager != nil {
		s.manager.notifyApplicationReady(s)
	}
}

func (s *Session) setCandidates(raw []protocol.RDPCandidate, forward bool) {
	validated, err := candidate.Validate(raw)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.candidates = append([]protocol.RDPCandidate(nil), validated...)
	close(s.candidateWake)
	s.candidateWake = make(chan struct{})
	s.mu.Unlock()
	if forward && s.manager.send != nil {
		ctx, cancel := context.WithTimeout(s.manager.ctx, 5*time.Second)
		_, _ = s.manager.send(ctx, protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate, Purpose: s.Purpose, SessionID: s.ID, ControllerID: s.ControllerID, TargetID: s.TargetID, SessionToken: append([]byte(nil), s.Token...), Candidates: validated})
		cancel()
	}
}

// publishCandidates updates this Agent's advertised endpoints without
// overwriting the peer candidates already received for the same lease. This
// matters for controller UDP: each local association has its own NAT mapping.
func (s *Session) publishCandidates(protocolName string, raw []protocol.RDPCandidate) {
	validated, err := candidate.Validate(raw)
	if err != nil || len(validated) == 0 {
		return
	}
	s.mu.Lock()
	merged := make([]protocol.RDPCandidate, 0, candidate.MaxCandidates)
	merged = append(merged, validated...)
	for _, item := range s.localCandidates {
		if item.Protocol == protocolName || len(merged) >= candidate.MaxCandidates {
			continue
		}
		merged = append(merged, item)
	}
	s.localCandidates = merged
	controllerID, targetID, token := s.ControllerID, s.TargetID, append([]byte(nil), s.Token...)
	s.mu.Unlock()
	if s.manager.send == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.manager.ctx, 5*time.Second)
	_, _ = s.manager.send(ctx, protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate, Purpose: s.Purpose, SessionID: s.ID, ControllerID: controllerID, TargetID: targetID, SessionToken: token, Candidates: merged})
	cancel()
}

func (s *Session) candidateList(ctx context.Context, protocolName string) []protocol.RDPCandidate {
	deadline := time.NewTimer(1500 * time.Millisecond)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		result := make([]protocol.RDPCandidate, 0)
		for _, item := range s.candidates {
			if item.Protocol == protocolName {
				result = append(result, item)
			}
		}
		wake := s.candidateWake
		s.mu.Unlock()
		if len(result) > 0 {
			return result
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-wake:
		}
	}
}

func (s *Session) DialTCP(ctx context.Context) (net.Conn, error) {
	if s == nil || s.Purpose != protocol.P2PPurposeRDP {
		return nil, errors.New("TCP direct path is only available for RDP sessions")
	}
	candidates := s.candidateList(ctx, "tcp")
	if len(candidates) == 0 {
		return nil, errors.New("RDP TCP direct candidates unavailable")
	}
	conn, err := punch.Dial(ctx, candidates, s.ID, s.Token, 1200*time.Millisecond)
	if err != nil {
		return nil, err
	}
	tunnel.TuneTCPConn(conn)
	conn = s.trackConn(conn)
	if conn == nil {
		return nil, net.ErrClosed
	}
	s.pathTCP.Store("tcp_p2p")
	return conn, nil
}

func (s *Session) DialUDP(ctx context.Context) (net.PacketConn, error) {
	candidates := s.candidateList(ctx, "udp")
	if len(candidates) == 0 {
		return nil, errors.New("RDP UDP direct candidates unavailable")
	}
	// Each controller association gets its own socket. Sharing the manager's
	// registration socket would let concurrent PacketConn readers steal one
	// another's authenticated datagrams and would make lease cleanup unable to
	// close only one association.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	tunnel.TuneUDPConn(conn)
	// The controller's registration socket is not the socket used for this
	// association. Publish the actual bound port before punching so the target
	// can validate and reply to the same NAT mapping.
	localCandidates := candidate.Discover(conn.LocalAddr().(*net.UDPAddr).Port, 0)
	if len(localCandidates) > 0 {
		s.publishCandidates("udp", localCandidates)
	}
	s.manager.mu.Lock()
	rendezvous := s.manager.rendezvous
	s.manager.mu.Unlock()
	if rendezvous != "" {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if reflexive, probeErr := candidate.ProbeReflexive(probeCtx, rendezvous, conn, "udp"); probeErr == nil {
			localCandidates = append(localCandidates, reflexive)
			s.publishCandidates("udp", localCandidates)
		}
		cancel()
	}
	result, err := punch.PunchWithDomain(
		ctx, conn, candidates, s.ID, s.Token, 1200*time.Millisecond, securityDomainForPurpose(s.Purpose),
	)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var packetConn *punch.PacketConn
	if s.Purpose == protocol.P2PPurposeDesktopMedia {
		// Relay Desktop path switching runs on a one-second quality cadence.
		// Probe the exact authenticated P2P socket at the same cadence instead
		// of reusing the reliable Relay session's RTT.
		packetConn, err = punch.NewPacketConnWithKeepalive(result, time.Second)
	} else {
		packetConn, err = punch.NewPacketConn(result)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if s.Purpose == protocol.P2PPurposeDesktopMedia {
		packetConn.SetProbeObserver(s.observeDirectPathRTT)
	}
	trackedPacket := s.trackPacketConn(packetConn)
	if trackedPacket == nil {
		return nil, net.ErrClosed
	}
	s.pathUDP.Store("udp_p2p")
	return trackedPacket, nil
}

func (s *Session) observeDirectPathRTT(rtt time.Duration) {
	if s == nil || rtt < 0 {
		return
	}
	sample := float64(rtt.Microseconds()) / 1000
	s.directMetricsMu.Lock()
	defer s.directMetricsMu.Unlock()

	if s.directRTTMs == 0 {
		s.directRTTMs = sample
	} else {
		s.directRTTMs = s.directRTTMs*0.8 + sample*0.2
	}
	if s.directLastRTTMs != 0 {
		delta := sample - s.directLastRTTMs
		if delta < 0 {
			delta = -delta
		}
		if s.directJitterMs == 0 {
			s.directJitterMs = delta
		} else {
			s.directJitterMs = s.directJitterMs*0.8 + delta*0.2
		}
	}
	s.directLastRTTMs = sample
	s.directSamples++
}

func (s *Session) DirectPathMetrics() DirectPathMetrics {
	if s == nil {
		return DirectPathMetrics{}
	}
	s.directMetricsMu.Lock()
	defer s.directMetricsMu.Unlock()
	return DirectPathMetrics{
		RTTMs:    s.directRTTMs,
		JitterMs: s.directJitterMs,
		Samples:  s.directSamples,
	}
}

func (s *Session) PathTCP() string {
	if value := s.pathTCP.Load(); value != nil {
		return value.(string)
	}
	return "relay"
}

func (s *Session) PathUDP() string {
	if value := s.pathUDP.Load(); value != nil {
		return value.(string)
	}
	return "relay"
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeLocal()
	s.manager.mu.Lock()
	if s.manager.sessions[s.ID] == s {
		delete(s.manager.sessions, s.ID)
	}
	s.manager.mu.Unlock()
	if s.manager.send != nil && !s.manager.closed.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = s.manager.send(ctx, protocol.RDPControlMessage{Type: protocol.RDPControlSessionClose, Purpose: s.Purpose, SessionID: s.ID, ControllerID: s.ControllerID, TargetID: s.TargetID, SessionToken: append([]byte(nil), s.Token...)})
		cancel()
	}
	return nil
}

// SetOnClose attaches the local RDP connection cleanup hook. Server lease
// expiry/revocation closes the P2P session asynchronously, so the controller
// listener must be closed as well instead of silently falling back on every
// new local connection.
func (s *Session) SetOnClose(fn func()) {
	if s == nil {
		return
	}
	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		if fn != nil {
			fn()
		}
	default:
		s.onClose = fn
		s.mu.Unlock()
	}
}

func (s *Session) closeLocal() {
	var onClose func()
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		udp, local, reassembler, applicationPath := s.udp, s.localUDP, s.udpReassembler, s.applicationPath
		s.udp, s.localUDP, s.udpReassembler, s.applicationPath = nil, nil, nil, nil
		onClose = s.onClose
		s.onClose = nil
		conns := make([]net.Conn, 0, len(s.directConns))
		for conn := range s.directConns {
			conns = append(conns, conn)
		}
		s.directConns = make(map[net.Conn]struct{})
		packets := make([]net.PacketConn, 0, len(s.directPackets))
		for packet := range s.directPackets {
			packets = append(packets, packet)
		}
		s.directPackets = make(map[net.PacketConn]struct{})
		s.mu.Unlock()
		if udp != nil {
			_ = udp.Close()
		}
		if local != nil {
			_ = local.Close()
		}
		if reassembler != nil {
			reassembler.Close()
		}
		if applicationPath != nil {
			applicationPath.closeLocal()
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
		for _, packet := range packets {
			_ = packet.Close()
		}
	})
	if onClose != nil {
		onClose()
	}
}

func (s *Session) trackConn(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	tracked := &trackedConn{Conn: conn, owner: s}
	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		_ = conn.Close()
		return nil
	default:
		if s.directConns == nil {
			s.directConns = make(map[net.Conn]struct{})
		}
		s.directConns[tracked] = struct{}{}
		s.mu.Unlock()
		return tracked
	}
}

func (s *Session) trackPacketConn(conn net.PacketConn) net.PacketConn {
	if conn == nil {
		return nil
	}
	tracked := &trackedPacketConn{PacketConn: conn, owner: s}
	s.mu.Lock()
	select {
	case <-s.closed:
		s.mu.Unlock()
		_ = conn.Close()
		return nil
	default:
		if s.directPackets == nil {
			s.directPackets = make(map[net.PacketConn]struct{})
		}
		s.directPackets[tracked] = struct{}{}
		s.mu.Unlock()
		return tracked
	}
}

func (s *Session) removeConn(conn net.Conn) {
	s.mu.Lock()
	delete(s.directConns, conn)
	s.mu.Unlock()
}

func (s *Session) removePacketConn(conn net.PacketConn) {
	s.mu.Lock()
	delete(s.directPackets, conn)
	s.mu.Unlock()
}

type trackedConn struct {
	net.Conn
	owner *Session
	once  sync.Once
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		c.owner.removeConn(c)
		err = c.Conn.Close()
	})
	return err
}

type trackedPacketConn struct {
	net.PacketConn
	owner *Session
	once  sync.Once
}

func (c *trackedPacketConn) Close() error {
	var err error
	c.once.Do(func() {
		c.owner.removePacketConn(c)
		err = c.PacketConn.Close()
	})
	return err
}

func (s *Session) isExpired() bool {
	return time.Now().UnixMilli() >= s.ExpiresAt.Load()
}

func (s *Session) renewLoop() {
	interval := s.manager.lease / 2
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-s.manager.ctx.Done():
			return
		case <-ticker.C:
			if s.isExpired() {
				s.removeLocal()
				return
			}
			ctx, cancel := context.WithTimeout(s.manager.ctx, 5*time.Second)
			response, err := s.manager.send(ctx, protocol.RDPControlMessage{Type: protocol.RDPControlLeaseRenew, Purpose: s.Purpose, SessionID: s.ID, ControllerID: s.ControllerID, TargetID: s.TargetID, SessionToken: append([]byte(nil), s.Token...)})
			cancel()
			if err != nil || response.Type == protocol.RDPControlError {
				s.removeLocal()
				return
			}
			if response.LeaseExpiresAt > 0 {
				s.ExpiresAt.Store(response.LeaseExpiresAt)
			}
		}
	}
}

func (s *Session) removeLocal() {
	s.closeLocal()
	if s.manager == nil {
		return
	}
	s.manager.mu.Lock()
	if s.manager.sessions[s.ID] == s {
		delete(s.manager.sessions, s.ID)
	}
	s.manager.mu.Unlock()
}

func tcpPort(listener net.Listener) int {
	if listener == nil {
		return 0
	}
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

func udpPort(conn *net.UDPConn) int {
	if conn == nil {
		return 0
	}
	return conn.LocalAddr().(*net.UDPAddr).Port
}

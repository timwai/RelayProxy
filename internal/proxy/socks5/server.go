package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
)

const proxyCopyBufferSize = 128 * 1024

var proxyCopyBufferPool = sync.Pool{
	New: func() any { return new([proxyCopyBufferSize]byte) },
}

const (
	Version5 = 0x05

	AuthMethodNone         = 0x00
	AuthMethodUserPassword = 0x02
	AuthMethodNoAcceptable = 0xFF

	CmdConnect      = 0x01
	CmdUDPAssociate = 0x03

	AtypIPv4   = 0x01
	AtypDomain = 0x03
	AtypIPv6   = 0x04

	RepSuccess        = 0x00
	RepGeneralFailure = 0x01
	RepNotAllowed     = 0x02
	RepNetUnreachable = 0x03
	RepHostUnreach    = 0x04
	RepConnRefused    = 0x05
	RepCmdNotSupport  = 0x07
	RepAddrNotSupport = 0x08
)

type ServerConfig struct {
	ListenAddr    string // e.g. "127.0.0.1:1080"
	GetExitNodeID func() string
	Dialer        proxy.TunnelDialer
	// Authenticate enables RFC 1929 username/password auth. The returned
	// process metadata is trusted only after this callback accepts the pair.
	Authenticate func(username, password string) (process string, aliases []string, ok bool)
}

type Server struct {
	cfg         ServerConfig
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	listener    net.Listener
	starting    bool
	closed      bool
	conns       map[net.Conn]struct{}
	packetConns map[net.PacketConn]struct{}
	workers     sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:1080"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg: cfg, ctx: ctx, cancel: cancel,
		conns: make(map[net.Conn]struct{}), packetConns: make(map[net.PacketConn]struct{}),
	}
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return net.ErrClosed
	}
	if s.starting || s.listener != nil {
		s.mu.Unlock()
		return errors.New("socks5 server already started")
	}
	s.starting = true
	// Count listener setup as well as the accept loop, so concurrent shutdown
	// also cancels and waits for a pending listen-address lookup.
	s.workers.Add(1)
	s.mu.Unlock()

	l, err := (&net.ListenConfig{}).Listen(s.ctx, "tcp", s.cfg.ListenAddr)
	s.mu.Lock()
	s.starting = false
	if s.closed {
		s.mu.Unlock()
		if l != nil {
			_ = l.Close()
		}
		s.workers.Done()
		return net.ErrClosed
	}
	if err != nil {
		s.mu.Unlock()
		s.workers.Done()
		return fmt.Errorf("socks5 listen failed: %w", err)
	}
	s.listener = l
	s.mu.Unlock()
	// Keep the worker count positive for the whole accept loop. Any handler
	// registrations therefore happen before shutdown can finish waiting.
	log.Printf("[SOCKS5] Server listening on %s", l.Addr().String())

	go s.serve(l)
	return nil
}

func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr()
	}
	return nil
}

// Close cancels listener setup and pending dials, closes both sides of active
// connections, and waits for the accept loop and every handler to finish.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		listener := s.listener
		conns := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			conns = append(conns, conn)
		}
		packetConns := make([]net.PacketConn, 0, len(s.packetConns))
		for conn := range s.packetConns {
			packetConns = append(packetConns, conn)
		}
		s.mu.Unlock()

		s.cancel()
		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				s.closeErr = err
			}
		}
		for _, conn := range conns {
			_ = conn.Close()
		}
		for _, conn := range packetConns {
			_ = conn.Close()
		}
		s.workers.Wait()
	})
	return s.closeErr
}

func (s *Server) serve(listener net.Listener) {
	defer s.workers.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[SOCKS5] Accept error: %v", err)
			continue
		}

		if !s.trackConn(conn) {
			_ = conn.Close()
			return
		}
		s.workers.Add(1)
		go func(c net.Conn) {
			defer s.workers.Done()
			defer s.releaseConn(c)
			if err := s.handleConn(c); err != nil && !errors.Is(err, io.EOF) {
				// Log debug / non-fatal error
			}
		}(conn)
	}
}

func (s *Server) trackConn(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *Server) releaseConn(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *Server) handleConn(conn net.Conn) error {
	// Set handshake timeout to prevent Slowloris attacks
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	// 1. Negotiation handshake
	// +----+----------+----------+
	// |VER | NMETHODS | METHODS  |
	// +----+----------+----------+
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != Version5 {
		return fmt.Errorf("unsupported socks version: %d", buf[0])
	}
	nMethods := int(buf[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	var process string
	var processAliases []string
	if s.cfg.Authenticate != nil {
		if !containsByte(methods, AuthMethodUserPassword) {
			_, _ = conn.Write([]byte{Version5, AuthMethodNoAcceptable})
			return errors.New("username/password authentication is required")
		}
		if _, err := conn.Write([]byte{Version5, AuthMethodUserPassword}); err != nil {
			return err
		}
		var err error
		process, processAliases, err = s.authenticate(conn)
		if err != nil {
			return err
		}
	} else {
		if !containsByte(methods, AuthMethodNone) {
			_, _ = conn.Write([]byte{Version5, AuthMethodNoAcceptable})
			return errors.New("no acceptable auth methods")
		}
		if _, err := conn.Write([]byte{Version5, AuthMethodNone}); err != nil {
			return err
		}
	}

	// 2. Request details
	// +----+-----+-------+------+----------+----------+
	// |VER | CMD |  RSV  | ATYP | DST.ADDR | DST.PORT |
	// +----+-----+-------+------+----------+----------+
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return err
	}

	if reqHeader[0] != Version5 {
		return fmt.Errorf("invalid socks version in request: %d", reqHeader[0])
	}
	if reqHeader[2] != 0 {
		return fmt.Errorf("invalid socks reserved byte: %d", reqHeader[2])
	}
	cmd := reqHeader[1]
	atyp := reqHeader[3]

	if cmd != CmdConnect && cmd != CmdUDPAssociate {
		s.sendReply(conn, RepCmdNotSupport, "0.0.0.0", 0)
		return fmt.Errorf("unsupported command: %d", cmd)
	}

	var host string
	switch atyp {
	case AtypIPv4:
		ipBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, ipBuf); err != nil {
			return err
		}
		host = net.IP(ipBuf).String()
	case AtypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return err
		}
		domainLen := int(lenBuf[0])
		domainBuf := make([]byte, domainLen)
		if _, err := io.ReadFull(conn, domainBuf); err != nil {
			return err
		}
		// PRESERVE DOMAIN STRING WITHOUT RESOLVING!
		host = string(domainBuf)
	case AtypIPv6:
		ipBuf := make([]byte, 16)
		if _, err := io.ReadFull(conn, ipBuf); err != nil {
			return err
		}
		host = net.IP(ipBuf).String()
	default:
		s.sendReply(conn, RepAddrNotSupport, "0.0.0.0", 0)
		return fmt.Errorf("unsupported atyp: %d", atyp)
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return err
	}
	port := binary.BigEndian.Uint16(portBuf)
	if host == "" || (cmd == CmdConnect && port == 0) {
		_ = s.sendReply(conn, RepAddrNotSupport, "0.0.0.0", 0)
		return errors.New("invalid SOCKS5 destination")
	}
	if cmd == CmdUDPAssociate {
		return s.handleUDPAssociate(conn, host, port, process, processAliases)
	}

	exitID := ""
	if s.cfg.GetExitNodeID != nil {
		exitID = s.cfg.GetExitNodeID()
	}

	if s.cfg.Dialer == nil {
		s.sendReply(conn, RepGeneralFailure, "0.0.0.0", 0)
		return errors.New("tunnel dialer not configured")
	}

	// 3. Dial target via tunnel dialer
	dialCtx := proxy.WithClientConn(s.ctx, "socks5", conn)
	dialCtx = proxy.WithClientProcess(dialCtx, process, processAliases)
	targetConn, err := s.cfg.Dialer.DialTCP(dialCtx, exitID, host, port)
	if err != nil {
		if s.ctx.Err() != nil {
			return err
		}
		log.Printf("[SOCKS5] DialTCP failed for %s:%d: %v", host, port, err)
		rep := byte(RepHostUnreach)
		var re *protocol.RelayError
		if errors.As(err, &re) {
			switch re.Code {
			case protocol.ErrCodeConnectionRefused:
				rep = RepConnRefused
			case protocol.ErrCodeACLDenied, protocol.ErrCodeAccessDenied:
				rep = RepNotAllowed
			case protocol.ErrCodeExitOffline, protocol.ErrCodeNetworkUnreach, protocol.ErrCodeHostUnreach:
				rep = RepNetUnreachable
			case protocol.ErrCodeConnectTimeout, protocol.ErrCodeDNSFailed:
				rep = RepHostUnreach
			default:
				rep = RepGeneralFailure
			}
		} else {
			errStr := err.Error()
			if strings.Contains(errStr, "CONNECTION_REFUSED") || strings.Contains(errStr, "refused") {
				rep = RepConnRefused
			} else if strings.Contains(errStr, "ACL_DENIED") || strings.Contains(errStr, "blocked by ACL") {
				rep = RepNotAllowed
			} else if strings.Contains(errStr, "EXIT_OFFLINE") || strings.Contains(errStr, "not found") {
				rep = RepNetUnreachable
			}
		}
		s.sendReply(conn, rep, "0.0.0.0", 0)
		return err
	}
	if !s.trackConn(targetConn) {
		_ = targetConn.Close()
		return net.ErrClosed
	}
	defer s.releaseConn(targetConn)

	// Reply Success
	if err := s.sendReply(conn, RepSuccess, "0.0.0.0", 0); err != nil {
		return err
	}

	// Clear deadline for bidirectional streaming
	_ = conn.SetDeadline(time.Time{})

	// 4. Bidirectional copy
	s.pipe(conn, targetConn)
	return nil
}

func containsByte(values []byte, want byte) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) authenticate(conn net.Conn) (string, []string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", nil, err
	}
	if header[0] != 0x01 || header[1] == 0 {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return "", nil, errors.New("invalid username/password authentication request")
	}
	usernameBytes := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, usernameBytes); err != nil {
		return "", nil, err
	}
	length := []byte{0}
	if _, err := io.ReadFull(conn, length); err != nil {
		return "", nil, err
	}
	if length[0] == 0 {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return "", nil, errors.New("empty SOCKS5 password")
	}
	passwordBytes := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, passwordBytes); err != nil {
		return "", nil, err
	}
	process, aliases, ok := s.cfg.Authenticate(string(usernameBytes), string(passwordBytes))
	if !ok {
		_, _ = conn.Write([]byte{0x01, 0x01})
		return "", nil, errors.New("SOCKS5 authentication rejected")
	}
	if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
		return "", nil, err
	}
	return process, aliases, nil
}

func (s *Server) sendReply(conn net.Conn, rep byte, bndAddr string, bndPort uint16) error {
	ip := net.ParseIP(bndAddr)
	if ip == nil {
		ip = net.IPv4zero
	}
	ip4 := ip.To4()

	resp := make([]byte, 0, 22)
	if ip4 != nil {
		resp = append(resp, Version5, rep, 0x00, AtypIPv4)
		resp = append(resp, ip4...)
	} else {
		resp = append(resp, Version5, rep, 0x00, AtypIPv6)
		resp = append(resp, ip.To16()...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], bndPort)
	resp = append(resp, portBytes[:]...)

	_, err := conn.Write(resp)
	return err
}

const (
	maxUDPTargetsPerAssociation = 64
	udpTargetIdleTimeout        = 2 * time.Minute
	udpTargetSweepInterval      = 30 * time.Second
)

type udpTarget struct {
	key      string
	host     string
	port     uint16
	conn     net.PacketConn
	lastUsed time.Time
}

type udpAssociation struct {
	server     *Server
	conn       *net.UDPConn
	ctx        context.Context
	cancel     context.CancelFunc
	control    net.Conn
	clientMu   sync.RWMutex
	clientAddr *net.UDPAddr
	mu         sync.Mutex
	targets    map[string]*udpTarget
	closed     bool
	workers    sync.WaitGroup
}

func (s *Server) handleUDPAssociate(
	control net.Conn,
	requestedHost string,
	requestedPort uint16,
	process string,
	processAliases []string,
) error {
	peer, ok := control.RemoteAddr().(*net.TCPAddr)
	if !ok || peer.IP == nil || !peer.IP.IsLoopback() {
		s.sendReply(control, RepNotAllowed, "0.0.0.0", 0)
		return errors.New("SOCKS5 UDP association is only allowed from loopback clients")
	}
	if requestedIP := net.ParseIP(requestedHost); requestedIP != nil && !requestedIP.IsUnspecified() && !requestedIP.Equal(peer.IP) {
		s.sendReply(control, RepNotAllowed, "0.0.0.0", 0)
		return errors.New("SOCKS5 UDP client address does not match TCP peer")
	}
	if s.cfg.Dialer == nil {
		s.sendReply(control, RepGeneralFailure, "0.0.0.0", 0)
		return errors.New("tunnel dialer not configured")
	}

	listenIP := peer.IP
	if local, ok := control.LocalAddr().(*net.TCPAddr); ok && local.IP != nil && local.IP.IsLoopback() {
		listenIP = local.IP
	}
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: listenIP})
	if err != nil {
		s.sendReply(control, RepGeneralFailure, "0.0.0.0", 0)
		return fmt.Errorf("start SOCKS5 UDP relay: %w", err)
	}
	if !s.trackPacketConn(udpConn) {
		_ = udpConn.Close()
		return net.ErrClosed
	}
	defer s.releasePacketConn(udpConn)
	defer udpConn.Close()

	bound := udpConn.LocalAddr().(*net.UDPAddr)
	if err := s.sendReply(control, RepSuccess, bound.IP.String(), uint16(bound.Port)); err != nil {
		return err
	}
	_ = control.SetDeadline(time.Time{})

	ctx := proxy.WithClientConn(s.ctx, "socks5-udp", control)
	ctx = proxy.WithClientProcess(ctx, process, processAliases)
	ctx, cancel := context.WithCancel(ctx)
	association := &udpAssociation{
		server: s, conn: udpConn, ctx: ctx, cancel: cancel,
		control: control, targets: make(map[string]*udpTarget),
	}
	association.workers.Add(1)
	go association.reapIdleTargets()
	controlDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, control)
		close(controlDone)
		_ = udpConn.Close()
	}()

	buf := make([]byte, 65535)
	for {
		n, source, readErr := udpConn.ReadFromUDP(buf)
		if readErr != nil {
			break
		}
		if source == nil || !source.IP.Equal(peer.IP) {
			continue
		}
		association.clientMu.Lock()
		if association.clientAddr == nil {
			if requestedPort != 0 && int(requestedPort) != source.Port {
				association.clientMu.Unlock()
				continue
			}
			association.clientAddr = cloneUDPAddr(source)
		} else if !sameUDPAddr(association.clientAddr, source) {
			association.clientMu.Unlock()
			continue
		}
		association.clientMu.Unlock()

		host, port, payload, parseErr := decodeUDPDatagram(buf[:n])
		if parseErr != nil || port == 0 {
			continue
		}
		_ = association.forward(host, port, payload)
	}
	association.close()
	select {
	case <-controlDone:
	case <-s.ctx.Done():
	}
	return nil
}

func (s *Server) trackPacketConn(conn net.PacketConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.packetConns[conn] = struct{}{}
	return true
}

func (s *Server) releasePacketConn(conn net.PacketConn) {
	s.mu.Lock()
	delete(s.packetConns, conn)
	s.mu.Unlock()
}

func (a *udpAssociation) forward(host string, port uint16, payload []byte) error {
	key := net.JoinHostPort(host, strconv.Itoa(int(port)))
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return net.ErrClosed
	}
	target := a.targets[key]
	if target != nil {
		target.lastUsed = time.Now()
		a.mu.Unlock()
		_, err := target.conn.WriteTo(payload, nil)
		if err != nil {
			a.removeTarget(target)
			_ = target.conn.Close()
		}
		return err
	}
	var evicted net.PacketConn
	if len(a.targets) >= maxUDPTargetsPerAssociation {
		var oldest *udpTarget
		for _, candidate := range a.targets {
			if oldest == nil || candidate.lastUsed.Before(oldest.lastUsed) {
				oldest = candidate
			}
		}
		if oldest != nil {
			delete(a.targets, oldest.key)
			evicted = oldest.conn
		}
	}
	a.mu.Unlock()
	if evicted != nil {
		_ = evicted.Close()
	}

	exitID := ""
	if a.server.cfg.GetExitNodeID != nil {
		exitID = a.server.cfg.GetExitNodeID()
	}
	conn, err := a.server.cfg.Dialer.DialUDP(a.ctx, exitID, host, port)
	if err != nil {
		return err
	}
	target = &udpTarget{key: key, host: host, port: port, conn: conn, lastUsed: time.Now()}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = conn.Close()
		return net.ErrClosed
	}
	if existing := a.targets[key]; existing != nil {
		existing.lastUsed = time.Now()
		a.mu.Unlock()
		_ = conn.Close()
		target = existing
	} else {
		a.targets[key] = target
		a.workers.Add(1)
		go a.readTarget(target)
		a.mu.Unlock()
	}
	_, err = target.conn.WriteTo(payload, nil)
	if err != nil {
		a.removeTarget(target)
		_ = target.conn.Close()
	}
	return err
}

func (a *udpAssociation) readTarget(target *udpTarget) {
	defer a.workers.Done()
	buf := make([]byte, 65535)
	for {
		n, _, err := target.conn.ReadFrom(buf)
		if err != nil {
			a.removeTarget(target)
			_ = target.conn.Close()
			return
		}
		packet, err := encodeUDPDatagram(target.host, target.port, buf[:n])
		if err != nil {
			continue
		}
		a.clientMu.RLock()
		client := cloneUDPAddr(a.clientAddr)
		a.clientMu.RUnlock()
		if client == nil {
			continue
		}
		_, _ = a.conn.WriteToUDP(packet, client)
		a.mu.Lock()
		if current := a.targets[target.key]; current == target {
			current.lastUsed = time.Now()
		}
		a.mu.Unlock()
	}
}

func (a *udpAssociation) removeTarget(target *udpTarget) {
	a.mu.Lock()
	if current := a.targets[target.key]; current == target {
		delete(a.targets, target.key)
	}
	a.mu.Unlock()
}

func (a *udpAssociation) reapIdleTargets() {
	defer a.workers.Done()
	ticker := time.NewTicker(udpTargetSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-ticker.C:
			var stale []net.PacketConn
			a.mu.Lock()
			if a.closed {
				a.mu.Unlock()
				return
			}
			for key, target := range a.targets {
				if now.Sub(target.lastUsed) >= udpTargetIdleTimeout {
					delete(a.targets, key)
					stale = append(stale, target.conn)
				}
			}
			a.mu.Unlock()
			for _, conn := range stale {
				_ = conn.Close()
			}
		}
	}
}

func (a *udpAssociation) close() {
	a.cancel()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	conns := make([]net.PacketConn, 0, len(a.targets))
	for _, target := range a.targets {
		conns = append(conns, target.conn)
	}
	a.targets = nil
	a.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	a.workers.Wait()
}

func decodeUDPDatagram(packet []byte) (string, uint16, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return "", 0, nil, errors.New("unsupported SOCKS5 UDP fragmentation or header")
	}
	reader := strings.NewReader(string(packet[4:]))
	host, err := readAddress(reader, packet[3])
	if err != nil {
		return "", 0, nil, err
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(reader, portBytes[:]); err != nil {
		return "", 0, nil, err
	}
	consumed := len(packet) - reader.Len()
	return host, binary.BigEndian.Uint16(portBytes[:]), packet[consumed:], nil
}

func readAddress(reader io.Reader, atyp byte) (string, error) {
	switch atyp {
	case AtypIPv4:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return "", err
		}
		return net.IP(ip).String(), nil
	case AtypIPv6:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, ip); err != nil {
			return "", err
		}
		return net.IP(ip).String(), nil
	case AtypDomain:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return "", err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, name); err != nil {
			return "", err
		}
		if len(name) == 0 {
			return "", errors.New("empty SOCKS5 UDP destination")
		}
		return string(name), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 UDP address type: %d", atyp)
	}
}

func encodeUDPDatagram(host string, port uint16, payload []byte) ([]byte, error) {
	packet := []byte{0, 0, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			packet = append(packet, AtypIPv4)
			packet = append(packet, ip4...)
		} else {
			packet = append(packet, AtypIPv6)
			packet = append(packet, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errors.New("SOCKS5 UDP destination name is too long")
		}
		packet = append(packet, AtypDomain, byte(len(host)))
		packet = append(packet, host...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], port)
	packet = append(packet, portBytes[:]...)
	packet = append(packet, payload...)
	return packet, nil
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), addr.IP...), Port: addr.Port, Zone: addr.Zone}
}

func sameUDPAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.Zone == b.Zone && a.IP.Equal(b.IP)
}

func (s *Server) pipe(c1, c2 net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	copyOne := func(dst, src net.Conn) {
		defer wg.Done()
		bufp := proxyCopyBufferPool.Get().(*[proxyCopyBufferSize]byte)
		_, _ = io.CopyBuffer(dst, src, bufp[:])
		proxyCopyBufferPool.Put(bufp)

		// Attempt half-close if supported
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		} else if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
	}

	go copyOne(c1, c2)
	go copyOne(c2, c1)

	wg.Wait()
}

func SplitHostPort(target string) (string, uint16, error) {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, err
	}
	return host, uint16(port), nil
}

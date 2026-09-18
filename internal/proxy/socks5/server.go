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

const proxyCopyBufferSize = 32 * 1024

var proxyCopyBufferPool = sync.Pool{
	New: func() any { return new([proxyCopyBufferSize]byte) },
}

const (
	Version5 = 0x05

	AuthMethodNone         = 0x00
	AuthMethodNoAcceptable = 0xFF

	CmdConnect = 0x01

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
}

type Server struct {
	cfg       ServerConfig
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	listener  net.Listener
	starting  bool
	closed    bool
	conns     map[net.Conn]struct{}
	workers   sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:1080"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{cfg: cfg, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
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

	// We support AuthMethodNone
	hasNoAuth := false
	for _, m := range methods {
		if m == AuthMethodNone {
			hasNoAuth = true
			break
		}
	}

	if !hasNoAuth {
		_, _ = conn.Write([]byte{Version5, AuthMethodNoAcceptable})
		return errors.New("no acceptable auth methods")
	}

	// Reply with AuthMethodNone
	if _, err := conn.Write([]byte{Version5, AuthMethodNone}); err != nil {
		return err
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
	cmd := reqHeader[1]
	atyp := reqHeader[3]

	if cmd != CmdConnect {
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

func (s *Server) sendReply(conn net.Conn, rep byte, bndAddr string, bndPort uint16) error {
	ip := net.ParseIP(bndAddr)
	if ip == nil {
		ip = net.IPv4zero
	}
	ip4 := ip.To4()

	resp := make([]byte, 0, 10)
	resp = append(resp, Version5, rep, 0x00, AtypIPv4)
	if ip4 != nil {
		resp = append(resp, ip4...)
	} else {
		resp = append(resp, net.IPv4zero...)
	}
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], bndPort)
	resp = append(resp, portBytes[:]...)

	_, err := conn.Write(resp)
	return err
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

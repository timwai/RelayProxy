package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/internal/proxy"
)

type ServerConfig struct {
	ListenAddr    string // e.g. "127.0.0.1:8080"
	GetExitNodeID func() string
	Dialer        proxy.TunnelDialer
}

const proxyCopyBufferSize = 32 * 1024

var proxyCopyBufferPool = sync.Pool{
	New: func() any { return new([proxyCopyBufferSize]byte) },
}

type Server struct {
	cfg    ServerConfig
	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	listener  net.Listener
	starting  bool
	closed    bool
	conns     map[net.Conn]struct{}
	closeDone chan struct{}
	closeErr  error

	// Includes listener setup and the accept loop so Close cannot finish while
	// an accepted connection is still waiting to become a handler.
	activeConn sync.WaitGroup
}

func NewServer(cfg ServerConfig) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:       cfg,
		ctx:       ctx,
		cancel:    cancel,
		conns:     make(map[net.Conn]struct{}),
		closeDone: make(chan struct{}),
	}
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("httpproxy server is closed")
	}
	if s.starting || s.listener != nil {
		s.mu.Unlock()
		return errors.New("httpproxy server already started")
	}
	s.starting = true
	s.activeConn.Add(1)
	s.mu.Unlock()

	l, err := (&net.ListenConfig{}).Listen(s.ctx, "tcp", s.cfg.ListenAddr)
	s.mu.Lock()
	s.starting = false
	if err != nil {
		s.mu.Unlock()
		s.activeConn.Done()
		return fmt.Errorf("httpproxy listen failed: %w", err)
	}
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		s.activeConn.Done()
		return errors.New("httpproxy server is closed")
	}
	s.listener = l
	s.mu.Unlock()
	log.Printf("[HTTPProxy] Server listening on %s", l.Addr().String())

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

// Close cancels pending dials, closes active I/O, and waits for all handlers.
// Concurrent calls wait for the same shutdown to finish.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return s.closeErr
	}
	s.closed = true
	listener := s.listener
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	s.cancel()
	var err error
	if listener != nil {
		err = listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	s.activeConn.Wait()

	s.mu.Lock()
	s.closeErr = err
	close(s.closeDone)
	s.mu.Unlock()
	return err
}

func (s *Server) serve(listener net.Listener) {
	defer s.activeConn.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("[HTTPProxy] Accept error: %v", err)
			continue
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.activeConn.Add(1)
		s.mu.Unlock()
		go func(c net.Conn) {
			defer s.activeConn.Done()
			defer s.releaseConn(c)
			_ = s.handleConn(c)
		}(conn)
	}
}

// dialTCP registers the upstream before it can perform I/O. If shutdown won
// the race with a completed dial, close that connection here instead.
func (s *Server) dialTCP(client net.Conn, exitID, host string, port uint16) (net.Conn, error) {
	ctx := proxy.WithClientConn(s.ctx, "http", client)
	conn, err := s.cfg.Dialer.DialTCP(ctx, exitID, host, port)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	return conn, nil
}

func (s *Server) releaseConn(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *Server) handleConn(conn net.Conn) error {
	// Set initial read deadline to protect against slowloris
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return err
	}

	exitID := ""
	if s.cfg.GetExitNodeID != nil {
		exitID = s.cfg.GetExitNodeID()
	}

	if s.cfg.Dialer == nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\nTunnel dialer unavailable"))
		return errors.New("tunnel dialer not configured")
	}

	if req.Method == http.MethodConnect {
		return s.handleConnect(conn, reader, req, exitID)
	}

	return s.handleHTTP(conn, reader, req, exitID)
}

// handleConnect handles HTTPS CONNECT method tunneling
func (s *Server) handleConnect(clientConn net.Conn, clientReader *bufio.Reader, req *http.Request, exitID string) error {
	host, portStr, err := net.SplitHostPort(req.Host)
	if err != nil {
		host = req.Host
		portStr = "443"
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		_, _ = clientConn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\nInvalid Port"))
		return err
	}

	targetConn, err := s.dialTCP(clientConn, exitID, host, uint16(port))
	if err != nil {
		log.Printf("[HTTPProxy] Connect DialTCP failed for %s:%d: %v", host, port, err)
		_, _ = clientConn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\n\r\n"))
		return err
	}
	defer s.releaseConn(targetConn)

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return err
	}

	// Clear deadline for raw tunnel piping
	_ = clientConn.SetDeadline(time.Time{})

	// ReadRequest may already have buffered the first bytes of the tunnel.
	s.pipeWithReader(clientConn, clientReader, targetConn)
	return nil
}

// handleHTTP forwards regular HTTP requests
func (s *Server) handleHTTP(clientConn net.Conn, clientReader *bufio.Reader, req *http.Request, exitID string) error {
	var host string
	var port uint16 = 80

	if req.URL.IsAbs() {
		host = req.URL.Hostname()
		if p := req.URL.Port(); p != "" {
			val, _ := strconv.ParseUint(p, 10, 16)
			port = uint16(val)
		}
	} else if req.Host != "" {
		h, p, err := net.SplitHostPort(req.Host)
		if err == nil {
			host = h
			val, _ := strconv.ParseUint(p, 10, 16)
			port = uint16(val)
		} else {
			host = req.Host
		}
	}

	if host == "" {
		_, _ = clientConn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\nMissing Host header"))
		return errors.New("missing host")
	}

	targetConn, err := s.dialTCP(clientConn, exitID, host, port)
	if err != nil {
		log.Printf("[HTTPProxy] HTTP DialTCP failed for %s:%d: %v", host, port, err)
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return err
	}
	defer s.releaseConn(targetConn)

	// Clean request headers for proxying
	removeHopHeaders(req.Header)
	req.Header.Set("Connection", "close")

	// Rewrite RequestURI to path if it's absolute
	if req.URL.IsAbs() {
		req.RequestURI = req.URL.RequestURI()
	}

	// Write request to target
	if err := req.Write(targetConn); err != nil {
		return err
	}

	// Clear deadline for response streaming
	_ = clientConn.SetDeadline(time.Time{})

	// Stream response back
	s.pipeWithReader(clientConn, clientReader, targetConn)
	return nil
}

func (s *Server) pipeWithReader(clientConn net.Conn, clientReader io.Reader, targetConn net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		bufp := proxyCopyBufferPool.Get().(*[proxyCopyBufferSize]byte)
		_, _ = io.CopyBuffer(targetConn, clientReader, bufp[:])
		proxyCopyBufferPool.Put(bufp)
		if cw, ok := targetConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = targetConn.Close()
		}
	}()

	go func() {
		defer wg.Done()
		bufp := proxyCopyBufferPool.Get().(*[proxyCopyBufferSize]byte)
		_, _ = io.CopyBuffer(clientConn, targetConn, bufp[:])
		proxyCopyBufferPool.Put(bufp)
		if cw, ok := clientConn.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = clientConn.Close()
		}
	}()

	wg.Wait()
}

func removeHopHeaders(h http.Header) {
	// RFC 2616 Hop-by-hop headers
	hopHeaders := []string{
		"Proxy-Connection",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Connection",
		"Keep-Alive",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, header := range hopHeaders {
		h.Del(header)
	}
}

func ParseURL(raw string) (*url.URL, error) {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

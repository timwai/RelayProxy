package exit

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	UpstreamDirect = "direct"
	UpstreamSOCKS5 = "socks5"
	UpstreamHTTP   = "http"
	UpstreamHTTPS  = "https"
)

// UpstreamConfig controls how this exit reaches final targets.
type UpstreamConfig struct {
	Mode     string
	Address  string
	Username string
	Password string
}

func (c UpstreamConfig) normalized() UpstreamConfig {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = UpstreamDirect
	}
	c.Address = strings.TrimSpace(c.Address)
	return c
}

// NormalizeUpstreamConfig returns the canonical exit egress settings.
func NormalizeUpstreamConfig(c UpstreamConfig) UpstreamConfig {
	return c.normalized()
}

// ValidateUpstreamConfig validates exit egress proxy settings for programmatic callers.
func ValidateUpstreamConfig(c UpstreamConfig) error {
	return c.validate()
}

func (c UpstreamConfig) validate() error {
	c = c.normalized()
	switch c.Mode {
	case UpstreamDirect:
		return nil
	case UpstreamSOCKS5, UpstreamHTTP, UpstreamHTTPS:
	default:
		return fmt.Errorf("unsupported exit upstream mode %q", c.Mode)
	}
	host, port, err := net.SplitHostPort(c.Address)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("exit upstream address must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("exit upstream port must be 1-65535")
	}
	if len(c.Username) > 255 || len(c.Password) > 255 {
		return fmt.Errorf("SOCKS5 username/password must not exceed 255 bytes")
	}
	return nil
}

func (h *Handler) dialTCP(ctx context.Context, ips []net.IP, port uint16) (net.Conn, string, error) {
	upstream := h.cfg.Upstream.normalized()
	if upstream.Mode == UpstreamDirect {
		conn, err := dialTCPIPs(ctx, ips, port)
		if err != nil {
			return nil, "", err
		}
		return conn, conn.RemoteAddr().String(), nil
	}
	ordered := interleaveIPFamilies(ips)
	if len(ordered) == 0 {
		return nil, "", errors.New("no IP addresses to dial")
	}
	var errs []error
	for _, ip := range ordered {
		target := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
		var conn net.Conn
		var err error
		switch upstream.Mode {
		case UpstreamSOCKS5:
			conn, err = dialSOCKS5(ctx, upstream, 0x01, target)
		case UpstreamHTTP, UpstreamHTTPS:
			conn, err = dialHTTPConnect(ctx, upstream, target)
		default:
			err = fmt.Errorf("unsupported exit upstream mode %q", upstream.Mode)
		}
		if err == nil {
			return conn, target, nil
		}
		errs = append(errs, err)
	}
	return nil, "", errors.Join(errs...)
}

func (h *Handler) dialUDP(ctx context.Context, target netip.AddrPort) (net.Conn, error) {
	upstream := h.cfg.Upstream.normalized()
	if upstream.Mode == UpstreamDirect {
		var d net.Dialer
		return d.DialContext(ctx, "udp", target.String())
	}
	if upstream.Mode != UpstreamSOCKS5 {
		return nil, fmt.Errorf("exit upstream %s does not support UDP; use SOCKS5 or DIRECT", upstream.Mode)
	}
	return dialSOCKS5UDP(ctx, upstream, target)
}

func dialProxyTCP(ctx context.Context, cfg UpstreamConfig) (net.Conn, error) {
	var d net.Dialer
	if cfg.Mode != UpstreamHTTPS {
		return d.DialContext(ctx, "tcp", cfg.Address)
	}
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		return nil, err
	}
	td := tls.Dialer{
		NetDialer: &d,
		Config:    &tls.Config{MinVersion: tls.VersionTLS12, ServerName: strings.Trim(host, "[]")},
	}
	return td.DialContext(ctx, "tcp", cfg.Address)
}

func dialHTTPConnect(ctx context.Context, cfg UpstreamConfig, target string) (net.Conn, error) {
	conn, err := dialProxyTCP(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect exit upstream %s: %w", cfg.Address, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Connection: Keep-Alive\r\n", target, target)
	if cfg.Username != "" || cfg.Password != "" {
		token := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", token)
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, fmt.Errorf("write HTTP CONNECT: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP CONNECT rejected by upstream: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func dialSOCKS5(ctx context.Context, cfg UpstreamConfig, command byte, target string) (net.Conn, error) {
	conn, err := dialProxyTCP(ctx, UpstreamConfig{Mode: UpstreamHTTP, Address: cfg.Address})
	if err != nil {
		return nil, fmt.Errorf("connect SOCKS5 upstream %s: %w", cfg.Address, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	}
	if err := socks5Authenticate(conn, cfg); err != nil {
		return nil, err
	}
	if err := socks5Command(conn, command, target); err != nil {
		return nil, err
	}
	if command != 0x03 {
		_ = conn.SetDeadline(time.Time{})
		ok = true
		return conn, nil
	}
	ok = true
	return conn, nil
}

func socks5Authenticate(conn net.Conn, cfg UpstreamConfig) error {
	methods := []byte{0x00}
	if cfg.Username != "" || cfg.Password != "" {
		methods = []byte{0x02}
	}
	request := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	var response [2]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	if response[0] != 0x05 || response[1] == 0xff {
		return errors.New("SOCKS5 upstream rejected authentication methods")
	}
	wantsPassword := cfg.Username != "" || cfg.Password != ""
	if response[1] == 0x00 {
		if wantsPassword {
			return errors.New("SOCKS5 upstream selected no-auth despite configured credentials")
		}
		return nil
	}
	if response[1] != 0x02 || !wantsPassword {
		return fmt.Errorf("SOCKS5 upstream selected unoffered auth method 0x%02x", response[1])
	}
	if len(cfg.Username) > 255 || len(cfg.Password) > 255 {
		return errors.New("SOCKS5 username/password too long")
	}
	auth := []byte{0x01, byte(len(cfg.Username))}
	auth = append(auth, cfg.Username...)
	auth = append(auth, byte(len(cfg.Password)))
	auth = append(auth, cfg.Password...)
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	if response[0] != 0x01 || response[1] != 0x00 {
		return errors.New("SOCKS5 username/password authentication failed")
	}
	return nil
}

// socks5Command sends CONNECT or UDP ASSOCIATE and returns the proxy bound endpoint.
func socks5Command(conn net.Conn, command byte, target string) error {
	host, rawPort, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("invalid SOCKS5 target port")
	}
	addr, err := encodeSOCKS5Address(host, uint16(port))
	if err != nil {
		return err
	}
	request := []byte{0x05, command, 0x00}
	request = append(request, addr...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	_, err = readSOCKS5Reply(conn)
	return err
}

func readSOCKS5Reply(conn net.Conn) (netip.AddrPort, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return netip.AddrPort{}, err
	}
	if head[0] != 0x05 || head[2] != 0x00 {
		return netip.AddrPort{}, errors.New("invalid SOCKS5 reply")
	}
	if head[1] != 0x00 {
		return netip.AddrPort{}, fmt.Errorf("SOCKS5 upstream command failed: reply=0x%02x", head[1])
	}
	addr, err := readSOCKS5Address(conn, head[3])
	if err != nil {
		return netip.AddrPort{}, err
	}
	return addr, nil
}

func encodeSOCKS5Address(host string, port uint16) ([]byte, error) {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = ip.Unmap()
		if ip.Is4() {
			b := []byte{0x01}
			a := ip.As4()
			b = append(b, a[:]...)
			return append(b, byte(port>>8), byte(port)), nil
		}
		b := []byte{0x04}
		a := ip.As16()
		b = append(b, a[:]...)
		return append(b, byte(port>>8), byte(port)), nil
	}
	if len(host) == 0 || len(host) > 255 {
		return nil, errors.New("invalid SOCKS5 hostname")
	}
	b := []byte{0x03, byte(len(host))}
	b = append(b, host...)
	return append(b, byte(port>>8), byte(port)), nil
}

func readSOCKS5Address(r io.Reader, atyp byte) (netip.AddrPort, error) {
	var raw []byte
	switch atyp {
	case 0x01:
		raw = make([]byte, 4)
	case 0x04:
		raw = make([]byte, 16)
	case 0x03:
		var n [1]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return netip.AddrPort{}, err
		}
		if n[0] == 0 {
			return netip.AddrPort{}, errors.New("empty SOCKS5 bound hostname")
		}
		// CONNECT does not use BND.ADDR. Consume a domain reply without
		// resolving it locally; even an untrusted proxy can choose this name.
		// UDP ASSOCIATE rejects the invalid (non-IP) endpoint below.
		nameAndPort := make([]byte, int(n[0])+2)
		if _, err := io.ReadFull(r, nameAndPort); err != nil {
			return netip.AddrPort{}, err
		}
		return netip.AddrPort{}, nil
	default:
		return netip.AddrPort{}, fmt.Errorf("unsupported SOCKS5 address type 0x%02x", atyp)
	}
	if _, err := io.ReadFull(r, raw); err != nil {
		return netip.AddrPort{}, err
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return netip.AddrPort{}, err
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, errors.New("invalid SOCKS5 IP address")
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port[0])<<8|uint16(port[1])), nil
}

func dialSOCKS5UDP(ctx context.Context, cfg UpstreamConfig, target netip.AddrPort) (net.Conn, error) {
	return dialSOCKS5UDPTo(ctx, cfg, target.Addr().String(), target.Port())
}

// dialSOCKS5UDPTo keeps the original destination name in the UDP header: the
// SOCKS5 server resolves ATYP=0x03 names, not the local Agent.
func dialSOCKS5UDPTo(ctx context.Context, cfg UpstreamConfig, host string, port uint16) (net.Conn, error) {
	address, err := encodeSOCKS5Address(host, port)
	if err != nil {
		return nil, err
	}
	var remoteAddr net.Addr = socks5UDPTargetAddr{host: host, port: port}
	control, err := dialProxyTCP(ctx, UpstreamConfig{Mode: UpstreamHTTP, Address: cfg.Address})
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = control.Close()
		}
	}()
	if deadline, has := ctx.Deadline(); has {
		_ = control.SetDeadline(deadline)
	}
	if err := socks5Authenticate(control, cfg); err != nil {
		return nil, err
	}
	request := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := control.Write(request); err != nil {
		return nil, err
	}
	relay, err := readSOCKS5Reply(control)
	if err != nil {
		return nil, err
	}
	if !relay.IsValid() {
		return nil, errors.New("SOCKS5 UDP relay returned a domain BND.ADDR; refusing local DNS resolution")
	}
	if relay.Addr().IsUnspecified() {
		host, _, splitErr := net.SplitHostPort(control.RemoteAddr().String())
		if splitErr != nil {
			return nil, splitErr
		}
		ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]"))
		if parseErr != nil {
			ips, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if lookupErr != nil {
				return nil, fmt.Errorf("resolve SOCKS5 UDP relay host: %w", lookupErr)
			}
			if len(ips) == 0 {
				return nil, errors.New("resolve SOCKS5 UDP relay host: no addresses")
			}
			ip = ips[0]
		}
		relay = netip.AddrPortFrom(ip.Unmap(), relay.Port())
	}
	udp, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(relay))
	if err != nil {
		return nil, err
	}
	_ = control.SetDeadline(time.Time{})
	ok = true
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		remoteAddr = net.UDPAddrFromAddrPort(netip.AddrPortFrom(ip.Unmap(), port))
	}
	return &socks5UDPConn{control: control, udp: udp, targetAddress: address, remoteAddr: remoteAddr}, nil
}

// socks5UDPTargetAddr describes a domain-based UDP peer without local DNS.
type socks5UDPTargetAddr struct {
	host string
	port uint16
}

func (a socks5UDPTargetAddr) Network() string { return "udp" }
func (a socks5UDPTargetAddr) String() string {
	return net.JoinHostPort(a.host, strconv.Itoa(int(a.port)))
}

type socks5UDPConn struct {
	control       net.Conn
	udp           *net.UDPConn
	targetAddress []byte // SOCKS5 ATYP+ADDR+PORT (including domain ATYP=0x03)
	remoteAddr    net.Addr
	once          sync.Once
}

func (c *socks5UDPConn) Read(p []byte) (int, error) {
	buf := make([]byte, 65535)
	n, err := c.udp.Read(buf)
	if err != nil {
		return 0, err
	}
	if n < 4 || buf[0] != 0 || buf[1] != 0 || buf[2] != 0 {
		return 0, errors.New("invalid SOCKS5 UDP response")
	}
	offset, err := skipSOCKS5UDPAddress(buf[:n], 3)
	if err != nil {
		return 0, err
	}
	payload := buf[offset:n]
	if len(payload) > len(p) {
		return 0, io.ErrShortBuffer
	}
	return copy(p, payload), nil
}

func (c *socks5UDPConn) Write(p []byte) (int, error) {
	packet := make([]byte, 0, 3+len(c.targetAddress)+len(p))
	packet = append(packet, 0, 0, 0)
	packet = append(packet, c.targetAddress...)
	packet = append(packet, p...)
	n, err := c.udp.Write(packet)
	if err != nil {
		return 0, err
	}
	if n != len(packet) {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

// skipSOCKS5UDPAddress parses a UDP reply's ATYP and skips its source
// address. The source can be a domain: resolving it locally here would leak
// DNS queries and could break responses from otherwise working SOCKS5 servers.
func skipSOCKS5UDPAddress(packet []byte, offset int) (int, error) {
	if offset >= len(packet) {
		return 0, io.ErrUnexpectedEOF
	}
	atyp := packet[offset]
	offset++
	var size int
	switch atyp {
	case 0x01:
		size = 4
	case 0x04:
		size = 16
	case 0x03:
		if offset >= len(packet) {
			return 0, io.ErrUnexpectedEOF
		}
		size = int(packet[offset])
		offset++
	default:
		return 0, fmt.Errorf("unsupported SOCKS5 UDP address type 0x%02x", atyp)
	}
	if offset+size+2 > len(packet) {
		return 0, io.ErrUnexpectedEOF
	}
	return offset + size + 2, nil
}

func parseSOCKS5UDPAddress(packet []byte, offset int) (int, netip.AddrPort, error) {
	if offset >= len(packet) {
		return 0, netip.AddrPort{}, io.ErrUnexpectedEOF
	}
	atyp := packet[offset]
	offset++
	var raw []byte
	switch atyp {
	case 0x01:
		if offset+4+2 > len(packet) {
			return 0, netip.AddrPort{}, io.ErrUnexpectedEOF
		}
		raw = packet[offset : offset+4]
		offset += 4
	case 0x04:
		if offset+16+2 > len(packet) {
			return 0, netip.AddrPort{}, io.ErrUnexpectedEOF
		}
		raw = packet[offset : offset+16]
		offset += 16
	case 0x03:
		if offset >= len(packet) {
			return 0, netip.AddrPort{}, io.ErrUnexpectedEOF
		}
		n := int(packet[offset])
		offset++
		if offset+n+2 > len(packet) {
			return 0, netip.AddrPort{}, io.ErrUnexpectedEOF
		}
		host := string(packet[offset : offset+n])
		offset += n
		port := uint16(packet[offset])<<8 | uint16(packet[offset+1])
		offset += 2
		// The UDP reply hostname is supplied by the upstream, not the
		// application. Resolving it locally would leak an arbitrary DNS name.
		return 0, netip.AddrPort{}, fmt.Errorf("SOCKS5 UDP domain reply %q:%d requires remote DNS; use skipSOCKS5UDPAddress for opaque replies", host, port)
	default:
		return 0, netip.AddrPort{}, fmt.Errorf("unsupported SOCKS5 UDP address type 0x%02x", atyp)
	}
	port := uint16(packet[offset])<<8 | uint16(packet[offset+1])
	offset += 2
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return 0, netip.AddrPort{}, errors.New("invalid SOCKS5 UDP address")
	}
	return offset, netip.AddrPortFrom(addr.Unmap(), port), nil
}

func (c *socks5UDPConn) Close() error {
	var err error
	c.once.Do(func() { err = errors.Join(c.udp.Close(), c.control.Close()) })
	return err
}
func (c *socks5UDPConn) LocalAddr() net.Addr                { return c.udp.LocalAddr() }
func (c *socks5UDPConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *socks5UDPConn) SetDeadline(t time.Time) error      { return c.udp.SetDeadline(t) }
func (c *socks5UDPConn) SetReadDeadline(t time.Time) error  { return c.udp.SetReadDeadline(t) }
func (c *socks5UDPConn) SetWriteDeadline(t time.Time) error { return c.udp.SetWriteDeadline(t) }

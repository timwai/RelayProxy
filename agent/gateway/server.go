package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"relayproxy/internal/proxy"
)

type Options struct {
	Config        Config
	Dialer        proxy.TunnelDialer
	DefaultExitID func() string
	DNS           func(context.Context, string, []byte, bool) []byte
}
type Status struct {
	Supported bool   `json:"supported"`
	Running   bool   `json:"running"`
	Interface string `json:"interface"`
	Active    int64  `json:"active"`
	TCP       uint64 `json:"tcp"`
	UDP       uint64 `json:"udp"`
	DNS       uint64 `json:"dns"`
	Rejected  uint64 `json:"rejected"`
	LastError string `json:"lastError,omitempty"`
}
type association struct {
	name    string
	expires time.Time
}
type udpFlow struct {
	queue  chan []byte
	cancel context.CancelFunc
}
type flowKey struct{ source, dest netip.AddrPort }
type Server struct {
	queuedBytes atomic.Int64

	stopOnce                               sync.Once
	stopErr                                error
	opts                                   Options
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	mu                                     sync.Mutex
	tcp                                    net.Listener
	udp                                    *net.UDPConn
	lock                                   io.Closer
	conns                                  map[net.Conn]struct{}
	flows                                  map[flowKey]*udpFlow
	names                                  map[[2]netip.Addr][]association
	wg                                     sync.WaitGroup
	slots                                  chan struct{}
	running                                atomic.Bool
	active                                 atomic.Int64
	tcpCount, udpCount, dnsCount, rejected atomic.Uint64
	lastError                              string
}

func New(o Options) (*Server, error) {
	if err := Validate(o.Config); err != nil {
		return nil, err
	}
	if o.Dialer == nil {
		return nil, errors.New("gateway: dialer required")
	}
	o.Config = Clone(o.Config)
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{opts: o, ctx: ctx, cancel: cancel, slots: make(chan struct{}, o.Config.Limit()), conns: make(map[net.Conn]struct{}), flows: make(map[flowKey]*udpFlow), names: make(map[[2]netip.Addr][]association)}, nil
}
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Supported: Supported(), Running: s.running.Load(), Interface: s.opts.Config.Interface, Active: s.active.Load(), TCP: s.tcpCount.Load(), UDP: s.udpCount.Load(), DNS: s.dnsCount.Load(), Rejected: s.rejected.Load(), LastError: s.lastError}
}
func (s *Server) fail(err error) { s.mu.Lock(); s.lastError = err.Error(); s.mu.Unlock() }
func (s *Server) allowed(source netip.Addr) bool {
	for _, raw := range s.opts.Config.SourceCIDRs {
		p, _ := netip.ParsePrefix(raw)
		if p.Contains(source.Unmap()) {
			return true
		}
	}
	return false
}
func (s *Server) take() bool {
	select {
	case s.slots <- struct{}{}:
		s.active.Add(1)
		return true
	default:
		s.rejected.Add(1)
		return false
	}
}
func (s *Server) release() { <-s.slots; s.active.Add(-1) }
func (s *Server) exitID() string {
	if s.opts.Config.ExitID != "" {
		return s.opts.Config.ExitID
	}
	if s.opts.DefaultExitID != nil {
		return s.opts.DefaultExitID()
	}
	return ""
}
func (s *Server) flowContext(ctx context.Context, k flowKey) context.Context {
	return proxy.WithGatewayFlow(ctx, k.source, k.dest, s.opts.Config.Interface)
}
func (s *Server) host(k flowKey) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]netip.Addr{k.source.Addr(), k.dest.Addr()}
	list := s.names[key]
	name := ""
	now := time.Now()
	live := list[:0]
	for _, a := range list {
		if now.Before(a.expires) {
			live = append(live, a)
			if name != "" && name != a.name {
				return k.dest.Addr().String()
			}
			name = a.name
		}
	}
	if len(live) == 0 {
		delete(s.names, key)
	} else {
		s.names[key] = live
	}
	if name != "" {
		return name
	}
	return k.dest.Addr().String()
}
func (s *Server) remember(source netip.Addr, reply []byte) {
	var m dnsmessage.Message
	if m.Unpack(reply) != nil || m.RCode != dnsmessage.RCodeSuccess || len(m.Questions) != 1 {
		return
	}
	name := m.Questions[0].Name.String()
	if len(name) > 0 {
		name = name[:len(name)-1]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Accept only A owners reachable from the question through its CNAME chain.
	reachable := map[dnsmessage.Name]bool{m.Questions[0].Name: true}
	minTTL := uint32(600)
	for pass := 0; pass < 16; pass++ {
		changed := false
		for _, rr := range m.Answers {
			if reachable[rr.Header.Name] {
				if cn, ok := rr.Body.(*dnsmessage.CNAMEResource); ok && !reachable[cn.CNAME] {
					reachable[cn.CNAME] = true
					minTTL = min(minTTL, rr.Header.TTL)
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	for _, rr := range m.Answers {
		a, ok := rr.Body.(*dnsmessage.AResource)
		if !ok || rr.Header.TTL == 0 || !reachable[rr.Header.Name] || minTTL == 0 {
			continue
		}
		ip := netip.AddrFrom4(a.A)
		key := [2]netip.Addr{source, ip}
		if len(s.names) >= 8192 {
			for k, list := range s.names {
				alive := false
				for _, item := range list {
					if now.Before(item.expires) {
						alive = true
						break
					}
				}
				if !alive {
					delete(s.names, k)
				}
			}
			if len(s.names) >= 8192 {
				return
			}
		}
		list := s.names[key]
		found := false
		expiry := now.Add(time.Duration(min(rr.Header.TTL, minTTL)) * time.Second)
		live := list[:0]
		for _, item := range list {
			if now.Before(item.expires) {
				if item.name == name {
					item.expires = expiry
					found = true
				}
				live = append(live, item)
			}
		}
		if !found && len(live) < 16 {
			live = append(live, association{name, expiry})
		}
		s.names[key] = live
	}
}
func (s *Server) dns(ctx context.Context, k flowKey, q []byte, udp bool) []byte {
	s.dnsCount.Add(1)
	if s.opts.DNS == nil {
		return nil
	}
	reply := s.opts.DNS(s.flowContext(ctx, k), s.exitID(), q, udp)
	s.remember(k.source.Addr(), reply)
	return reply
}
func (s *Server) serveTCP(c net.Conn) {
	defer s.wg.Done()
	defer s.release()
	defer c.Close()
	stopClient := context.AfterFunc(s.ctx, func() { _ = c.Close() })
	defer stopClient()
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	source, e1 := netip.ParseAddrPort(c.RemoteAddr().String())
	dest, e2 := netip.ParseAddrPort(c.LocalAddr().String())
	if e1 != nil || e2 != nil || s.selfDestination(dest) || !s.allowed(source.Addr()) {
		s.rejected.Add(1)
		return
	}
	k := flowKey{source, dest}
	if dest.Port() == 53 {
		for {
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			var size [2]byte
			if _, err := io.ReadFull(c, size[:]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(size[:]))
			if n < 12 {
				return
			}
			q := make([]byte, n)
			if _, err := io.ReadFull(c, q); err != nil {
				return
			}
			answer := s.dns(s.ctx, k, q, false)
			if len(answer) == 0 {
				return
			}
			binary.BigEndian.PutUint16(size[:], uint16(len(answer)))
			if _, err := c.Write(append(size[:], answer...)); err != nil {
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(s.flowContext(s.ctx, k), 15*time.Second)
	up, err := s.opts.Dialer.DialTCP(ctx, s.exitID(), s.host(k), dest.Port())
	cancel()
	if err != nil {
		s.fail(err)
		return
	}
	defer up.Close()
	stopUpstream := context.AfterFunc(s.ctx, func() { _ = up.Close() })
	defer stopUpstream()
	s.tcpCount.Add(1)
	// Stop closes accepted sockets, which also releases both copy goroutines.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(idleConn{up}, idleConn{c})
		if w, ok := up.(interface{ CloseWrite() error }); ok {
			_ = w.CloseWrite()
		} else {
			_ = up.Close()
		}
	}()
	_, _ = io.Copy(idleConn{c}, idleConn{up})
	_ = c.Close()
	_ = up.Close()
	<-done
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Minute))
	return c.Conn.Read(p)
}
func (c idleConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.Conn.Write(p)
}
func (s *Server) receiveUDP(k flowKey, p []byte) {
	if !s.running.Load() || !s.allowed(k.source.Addr()) || s.selfDestination(k.dest) {
		s.rejected.Add(1)
		return
	}
	if s.queuedBytes.Add(int64(len(p))) > 16*1024*1024 {
		s.queuedBytes.Add(-int64(len(p)))
		s.rejected.Add(1)
		return
	}
	s.mu.Lock()
	f := s.flows[k]
	if f == nil {
		if !s.take() {
			s.queuedBytes.Add(-int64(len(p)))
			s.mu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(s.ctx)
		f = &udpFlow{queue: make(chan []byte, 8), cancel: cancel}
		s.flows[k] = f
		s.wg.Add(1)
		go s.serveUDP(ctx, k, f)
	}
	select {
	case f.queue <- append([]byte(nil), p...):
	default:
		s.queuedBytes.Add(-int64(len(p)))
		s.rejected.Add(1)
	}
	s.mu.Unlock()
}
func (s *Server) serveUDP(ctx context.Context, k flowKey, f *udpFlow) {
	defer s.wg.Done()
	defer s.release()
	defer f.cancel()
	defer func() {
		s.mu.Lock()
		delete(s.flows, k)
		for {
			select {
			case p := <-f.queue:
				s.queuedBytes.Add(-int64(len(p)))
			default:
				s.mu.Unlock()
				return
			}
		}
	}()
	reply, err := transparentReply(k.dest, s.opts.Config.Interface)
	if err != nil {
		s.fail(err)
		return
	}
	defer reply.Close()
	target := net.UDPAddrFromAddrPort(k.dest)
	client := net.UDPAddrFromAddrPort(k.source)
	if k.dest.Port() == 53 {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				return
			case q := <-f.queue:
				s.queuedBytes.Add(-int64(len(q)))
				answer := s.dns(ctx, k, q, true)
				if len(answer) > 0 {
					_ = reply.SetWriteDeadline(time.Now().Add(5 * time.Second))
					_, _ = reply.WriteToUDP(answer, client)
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(5 * time.Second)
			}
		}
	}

	dialCtx, cancel := context.WithTimeout(s.flowContext(ctx, k), 15*time.Second)
	up, err := s.opts.Dialer.DialUDP(dialCtx, s.exitID(), s.host(k), k.dest.Port())
	cancel()
	if err != nil {
		s.fail(err)
		return
	}
	defer up.Close()
	s.udpCount.Add(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			_ = up.SetReadDeadline(time.Now().Add(60 * time.Second))
			n, _, e := up.ReadFrom(buf)
			if e != nil {
				f.cancel()
				return
			}
			_ = reply.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, e = reply.WriteToUDP(buf[:n], client); e != nil {
				f.cancel()
				return
			}
		}
	}()
	defer func() { _ = up.Close(); <-done }()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case p := <-f.queue:
			s.queuedBytes.Add(-int64(len(p)))
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(60 * time.Second)
			_ = up.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := up.WriteTo(p, target); err != nil {
				return
			}
		}
	}
}

// Stop retains the kernel guard so a process restart cannot bypass policy.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		s.running.Store(false)
		s.cancel()
		s.mu.Lock()
		if s.tcp != nil {
			_ = s.tcp.Close()
		}
		if s.udp != nil {
			_ = s.udp.Close()
		}
		for c := range s.conns {
			_ = c.Close()
		}
		for _, f := range s.flows {
			f.cancel()
		}
		s.mu.Unlock()
		s.wg.Wait()
		if s.lock != nil {
			s.stopErr = s.lock.Close()
		}
	})
	return s.stopErr
}

func portAddress(c Config) string { return net.JoinHostPort("0.0.0.0", strconv.Itoa(c.ListenPort())) }
func (s *Server) String() string  { return fmt.Sprintf("gateway(%s)", s.opts.Config.Interface) }

func (s *Server) selfDestination(dest netip.AddrPort) bool {
	if dest.Port() != uint16(s.opts.Config.ListenPort()) {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	for _, a := range addrs {
		p, e := netip.ParsePrefix(a.String())
		if e == nil && p.Addr().Unmap() == dest.Addr().Unmap() {
			return true
		}
	}
	return false
}

package rdp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

const (
	maxUDPAssociations        = 256
	maxUDPOpeningAssociations = 64
	maxPendingUDPPackets      = 8
	maxTCPConnections         = 1024
	associationOpenTimeout    = 10 * time.Second
)

var (
	// ErrIngressDisabled is returned when an administrative mutation attempts
	// to reconcile a manager whose global public-ingress switch is off.
	ErrIngressDisabled = errors.New("RDP public ingress is disabled")
	// ErrIngressClosed is returned after the manager has started shutting down.
	ErrIngressClosed = errors.New("RDP public ingress is closed")
)

type IngressConfig struct {
	Enabled     bool
	Listen      string
	RateLimit   int
	SourceCIDRs []string
	PortStart   int
	PortEnd     int
	Audit       func(*repository.ConnectionAudit)
}

type IngressManager struct {
	db        *repository.DB
	sessions  *session.Manager
	cfg       IngressConfig
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	reloadMu  sync.Mutex
	endpoints map[string]*ingressEndpoint
	closed    atomic.Bool
}

// EndpointStatus reports the live listener state for one persisted ingress
// allocation. The allocation may remain enabled while a listener is being
// reconciled, so callers must not infer socket readiness from database status.
type EndpointStatus struct {
	TCPListening bool
	UDPListening bool
	ActiveUDP    int
}

type ingressEndpoint struct {
	item         *repository.RDPIngress
	tcp          net.Listener
	udp          *net.UDPConn
	cancel       context.CancelFunc
	mu           sync.Mutex
	assocs       map[netip.AddrPort]*udpAssociation
	opening      map[netip.AddrPort]*udpOpening
	sourceCIDRs  []netip.Prefix
	limiter      *sourceLimiter
	auditLimiter *sourceLimiter
	tcpSem       chan struct{}
}

type udpAssociation struct {
	remote netip.AddrPort
	conn   net.PacketConn
	stream tunnel.TunnelStream
	cancel context.CancelFunc
}

type udpOpening struct {
	remote  netip.AddrPort
	packets [][]byte
}

func NewIngressManager(parent context.Context, db *repository.DB, sessions *session.Manager, cfg IngressConfig) *IngressManager {
	ctx, cancel := context.WithCancel(parent)
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 120
	}
	return &IngressManager{db: db, sessions: sessions, cfg: cfg, ctx: ctx, cancel: cancel, endpoints: make(map[string]*ingressEndpoint)}
}

func (m *IngressManager) Start() error {
	if m == nil || !m.cfg.Enabled {
		return nil
	}
	if m.closed.Load() {
		return ErrIngressClosed
	}
	if m.db == nil || m.sessions == nil {
		return errors.New("RDP ingress requires database and session manager")
	}
	if err := m.reload(); err != nil {
		m.cancel()
		return err
	}
	go m.reloadLoop()
	return nil
}

// Enabled reports whether the manager can currently own public listeners.
// The configuration is immutable for the lifetime of a process; changing the
// global switch therefore still requires a service restart.
func (m *IngressManager) Enabled() bool {
	return m != nil && m.cfg.Enabled && !m.closed.Load()
}

// EndpointStatus returns a best-effort snapshot of one endpoint. A missing
// endpoint is represented by the zero value; the next reconciliation may
// still be opening its listeners.
func (m *IngressManager) EndpointStatus(id string) EndpointStatus {
	if m == nil || id == "" || m.closed.Load() {
		return EndpointStatus{}
	}
	m.mu.Lock()
	ep := m.endpoints[id]
	m.mu.Unlock()
	if ep == nil {
		return EndpointStatus{}
	}
	ep.mu.Lock()
	activeUDP := len(ep.assocs)
	ep.mu.Unlock()
	return EndpointStatus{TCPListening: ep.tcp != nil, UDPListening: ep.udp != nil, ActiveUDP: activeUDP}
}

// Reload reconciles the in-memory listeners with the allocation table. It is
// safe to call after an administrative create/enable/disable/delete mutation.
func (m *IngressManager) Reload() error {
	if m == nil || !m.cfg.Enabled {
		return ErrIngressDisabled
	}
	if m.closed.Load() {
		return ErrIngressClosed
	}
	if m.db == nil || m.sessions == nil {
		return errors.New("RDP ingress requires database and session manager")
	}
	return m.reload()
}

func (m *IngressManager) reloadLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.reload()
		}
	}
}

func (m *IngressManager) reload() error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if m.closed.Load() {
		return ErrIngressClosed
	}
	items, err := m.db.ListRDPIngress("")
	if err != nil {
		return err
	}
	wanted := make(map[string]*repository.RDPIngress)
	for _, item := range items {
		if item.Status == "enabled" && (item.ExpiresAt == nil || time.Now().Before(*item.ExpiresAt)) {
			wanted[item.ID] = item
		}
	}
	m.mu.Lock()
	var stale []*ingressEndpoint
	for id, endpoint := range m.endpoints {
		if _, ok := wanted[id]; !ok {
			stale = append(stale, endpoint)
			delete(m.endpoints, id)
		}
	}
	active := make(map[string]bool, len(m.endpoints))
	for id := range m.endpoints {
		active[id] = true
	}
	m.mu.Unlock()
	for _, endpoint := range stale {
		endpoint.close()
	}
	var firstOpenErr error
	for id, item := range wanted {
		if m.closed.Load() {
			return ErrIngressClosed
		}
		if !active[id] {
			if err := m.open(item); err != nil {
				log.Printf("[RDP] public ingress %s unavailable: %v", item.ID, err)
				if firstOpenErr == nil {
					firstOpenErr = fmt.Errorf("RDP ingress %s unavailable: %w", item.ID, err)
				}
			}
		}
	}
	return firstOpenErr
}

func (m *IngressManager) open(item *repository.RDPIngress) error {
	if item == nil || item.ListenPort < 1 || item.ListenPort > 65535 {
		return errors.New("invalid public RDP port allocation")
	}
	if len(item.SourceCIDRs) == 0 && len(m.cfg.SourceCIDRs) > 0 {
		item.SourceCIDRs = append([]string(nil), m.cfg.SourceCIDRs...)
	}
	host := "0.0.0.0"
	if strings.TrimSpace(m.cfg.Listen) != "" {
		configuredHost, _, err := net.SplitHostPort(m.cfg.Listen)
		if err != nil {
			return err
		}
		if configuredHost != "" {
			host = configuredHost
		}
	}
	tcp, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(item.ListenPort)))
	if err != nil {
		return err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, fmt.Sprint(item.ListenPort)))
	if err != nil {
		_ = tcp.Close()
		return err
	}
	udp, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		_ = tcp.Close()
		return err
	}
	tunnel.TuneUDPConn(udp)
	prefixes := make([]netip.Prefix, 0, len(item.SourceCIDRs))
	for _, raw := range item.SourceCIDRs {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			_ = tcp.Close()
			_ = udp.Close()
			return fmt.Errorf("invalid RDP ingress source CIDR %q: %w", raw, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	if len(prefixes) == 0 && len(m.cfg.SourceCIDRs) > 0 {
		for _, raw := range m.cfg.SourceCIDRs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil {
				_ = tcp.Close()
				_ = udp.Close()
				return fmt.Errorf("invalid RDP ingress source CIDR %q: %w", raw, err)
			}
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	ctx, cancel := context.WithCancel(m.ctx)
	ep := &ingressEndpoint{
		item: item, tcp: tcp, udp: udp, cancel: cancel,
		assocs: make(map[netip.AddrPort]*udpAssociation), opening: make(map[netip.AddrPort]*udpOpening),
		sourceCIDRs: prefixes, limiter: newSourceLimiter(item.RateLimitPerMin),
		auditLimiter: newSourceLimiter(4),
		tcpSem:       make(chan struct{}, maxTCPConnections),
	}
	m.mu.Lock()
	m.endpoints[item.ID] = ep
	m.mu.Unlock()
	go m.serveTCP(ctx, ep)
	go m.serveUDP(ctx, ep)
	return nil
}

func (m *IngressManager) Close() error {
	if m == nil {
		return nil
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if m.closed.Swap(true) {
		return nil
	}
	m.cancel()
	m.mu.Lock()
	endpoints := make([]*ingressEndpoint, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		endpoints = append(endpoints, ep)
	}
	m.endpoints = make(map[string]*ingressEndpoint)
	m.mu.Unlock()
	for _, ep := range endpoints {
		ep.close()
	}
	return nil
}

func (m *IngressManager) CloseDevice(deviceID string) {
	if m == nil {
		return
	}
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.mu.Lock()
	var endpoints []*ingressEndpoint
	for id, ep := range m.endpoints {
		if ep.item.TargetDeviceID == deviceID {
			endpoints = append(endpoints, ep)
			delete(m.endpoints, id)
		}
	}
	m.mu.Unlock()
	for _, ep := range endpoints {
		ep.close()
		if m.db != nil {
			_ = m.db.SetRDPIngressStatus(ep.item.ID, "", false)
		}
	}
}

func (ep *ingressEndpoint) close() {
	if ep.cancel != nil {
		ep.cancel()
	}
	if ep.tcp != nil {
		_ = ep.tcp.Close()
	}
	if ep.udp != nil {
		_ = ep.udp.Close()
	}
	ep.mu.Lock()
	assocs := make([]*udpAssociation, 0, len(ep.assocs))
	for key, assoc := range ep.assocs {
		assocs = append(assocs, assoc)
		delete(ep.assocs, key)
	}
	for _, opening := range ep.opening {
		opening.packets = nil
	}
	ep.opening = make(map[netip.AddrPort]*udpOpening)
	ep.mu.Unlock()
	for _, assoc := range assocs {
		assoc.cancel()
		_ = assoc.conn.Close()
		_ = assoc.stream.Close()
	}
}

func (m *IngressManager) serveTCP(ctx context.Context, ep *ingressEndpoint) {
	for {
		conn, err := ep.tcp.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		tunnel.TuneTCPConn(conn)
		select {
		case ep.tcpSem <- struct{}{}:
			go func() {
				defer func() { <-ep.tcpSem }()
				m.handleTCP(ctx, ep, conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (m *IngressManager) handleTCP(ctx context.Context, ep *ingressEndpoint, conn net.Conn) {
	defer conn.Close()
	openDeadline := time.Now().Add(associationOpenTimeout)
	_ = conn.SetDeadline(openDeadline)
	remoteIP := sourceIP(conn.RemoteAddr())
	if !ep.allow(remoteIP) {
		m.auditReject(ep, remoteIP, "SOURCE_OR_RATE_LIMIT")
		return
	}
	target, ok := m.sessions.Get(ep.item.TargetDeviceID)
	if !ok || target == nil || !contains(target.Grants, protocol.CapabilityRDPHost) || !contains(target.Grants, protocol.CapabilityRDPPublic) {
		m.auditReject(ep, remoteIP, "TARGET_OFFLINE")
		return
	}
	openCtx, cancel := context.WithTimeout(ctx, associationOpenTimeout)
	defer cancel()
	stream, err := target.Tunnel.OpenStream(openCtx)
	if err != nil {
		m.auditReject(ep, remoteIP, "STREAM_OPEN_FAILED")
		return
	}
	defer stream.Close()
	if deadline, ok := openCtx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	requestID := "ingress_" + uuid.NewString()[:8]
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenRDP, RequestID: requestID, ExitDeviceID: target.DeviceID}); err != nil {
		return
	}
	if err := protocol.WriteJSON(stream, protocol.OpenRDPRequest{RequestID: requestID, TimeoutMs: 10000}); err != nil {
		return
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &response); err != nil || !response.Success {
		m.auditReject(ep, remoteIP, "TARGET_UNAVAILABLE")
		return
	}
	_ = stream.SetDeadline(time.Time{})
	_ = conn.SetDeadline(time.Time{})
	up, down := tunnel.Pipe(ctx, conn, stream, 30*time.Minute, nil)
	m.audit(ep.item, remoteIP, "SUCCESS", "", up, down)
}

func (m *IngressManager) serveUDP(ctx context.Context, ep *ingressEndpoint) {
	buffer := make([]byte, 64*1024)
	for {
		n, remote, err := ep.udp.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			return
		}
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if !ep.allowSourceAddr(remote.Addr()) {
			m.auditReject(ep, remote.Addr().String(), "SOURCE_NOT_ALLOWED")
			continue
		}
		key := remote
		ep.mu.Lock()
		assoc := ep.assocs[key]
		opening := ep.opening[key]
		startOpening := false
		if assoc == nil && opening == nil {
			if len(ep.opening) >= maxUDPOpeningAssociations {
				ep.mu.Unlock()
				continue
			}
			if !ep.allow(remote.Addr().String()) {
				ep.mu.Unlock()
				m.auditReject(ep, remote.Addr().String(), "RATE_LIMIT")
				continue
			}
			opening = &udpOpening{remote: remote}
			ep.opening[key] = opening
			startOpening = true
		}
		if assoc == nil && opening != nil {
			if len(opening.packets) < maxPendingUDPPackets {
				packet := make([]byte, n)
				copy(packet, buffer[:n])
				opening.packets = append(opening.packets, packet)
			}
		}
		ep.mu.Unlock()
		if startOpening {
			go m.openUDPAssociationAsync(ctx, ep, key, opening)
		}
		if assoc == nil {
			continue
		}
		if _, err := assoc.conn.WriteTo(buffer[:n], nil); err != nil {
			m.removeUDPAssociation(ep, key, assoc)
		}
	}
}

func (m *IngressManager) openUDPAssociationAsync(ctx context.Context, ep *ingressEndpoint, key netip.AddrPort, opening *udpOpening) {
	openCtx, cancel := context.WithTimeout(ctx, associationOpenTimeout)
	assoc := m.openUDPAssociation(openCtx, ep, opening.remote)
	cancel()
	ep.mu.Lock()
	if ep.opening[key] == opening {
		delete(ep.opening, key)
	}
	if assoc == nil {
		ep.mu.Unlock()
		return
	}
	if ctx.Err() != nil || len(ep.assocs) >= maxUDPAssociations {
		ep.mu.Unlock()
		assoc.cancel()
		_ = assoc.conn.Close()
		_ = assoc.stream.Close()
		m.auditReject(ep, key.Addr().String(), "ASSOCIATION_LIMIT")
		return
	}
	ep.assocs[key] = assoc
	packets := opening.packets
	ep.mu.Unlock()
	go m.readUDPAssociation(ctx, ep, key, assoc)
	for _, packet := range packets {
		if _, err := assoc.conn.WriteTo(packet, nil); err != nil {
			m.removeUDPAssociation(ep, key, assoc)
			return
		}
	}
}

func (m *IngressManager) openUDPAssociation(ctx context.Context, ep *ingressEndpoint, remote netip.AddrPort) *udpAssociation {
	remoteText := remote.Addr().String()
	target, ok := m.sessions.Get(ep.item.TargetDeviceID)
	if !ok || target == nil || !contains(target.Grants, protocol.CapabilityRDPHost) || !contains(target.Grants, protocol.CapabilityRDPPublic) || !tunnel.PeerSupportsDatagrams(target.Tunnel) {
		m.auditReject(ep, remoteText, "DATAGRAM_UNAVAILABLE")
		return nil
	}
	channel, err := tunnel.OpenDatagramChannel(target.Tunnel, 0)
	if err != nil {
		return nil
	}
	stream, err := target.Tunnel.OpenStream(ctx)
	if err != nil {
		_ = channel.Close()
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	requestID := "ingress_udp_" + uuid.NewString()[:8]
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenRDPUDP, RequestID: requestID, ExitDeviceID: target.DeviceID}); err != nil {
		_ = channel.Close()
		_ = stream.Close()
		return nil
	}
	if err := protocol.WriteJSON(stream, protocol.OpenRDPRequest{RequestID: requestID, TimeoutMs: 10000, Mode: protocol.UDPModeDatagram, AssociationID: channel.ID, DatagramRequired: true}); err != nil {
		_ = channel.Close()
		_ = stream.Close()
		return nil
	}
	var response protocol.OpenUDPResponse
	if err := protocol.ReadJSON(stream, &response); err != nil || !response.Success ||
		response.Mode != protocol.UDPModeDatagram || response.AssociationID != channel.ID {
		_ = channel.Close()
		_ = stream.Close()
		return nil
	}
	_ = stream.SetDeadline(time.Time{})
	remoteAddr := &net.UDPAddr{IP: remote.Addr().AsSlice(), Port: int(remote.Port())}
	return &udpAssociation{remote: remote, conn: tunnel.NewUDPDatagramConn(channel, stream, remoteAddr), stream: stream, cancel: func() { _ = channel.Close() }}
}

func (m *IngressManager) readUDPAssociation(ctx context.Context, ep *ingressEndpoint, key netip.AddrPort, assoc *udpAssociation) {
	buffer := make([]byte, 64*1024)
	for {
		n, _, err := assoc.conn.ReadFrom(buffer)
		if err != nil {
			m.removeUDPAssociation(ep, key, assoc)
			return
		}
		if _, err := ep.udp.WriteToUDPAddrPort(buffer[:n], assoc.remote); err != nil {
			m.removeUDPAssociation(ep, key, assoc)
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (m *IngressManager) removeUDPAssociation(ep *ingressEndpoint, key netip.AddrPort, assoc *udpAssociation) {
	ep.mu.Lock()
	if ep.assocs[key] == assoc {
		delete(ep.assocs, key)
	}
	ep.mu.Unlock()
	assoc.cancel()
	_ = assoc.conn.Close()
	_ = assoc.stream.Close()
}

func (ep *ingressEndpoint) allow(ip string) bool {
	if !ep.allowSource(ip) {
		return false
	}
	return ep.limiter.allow(ip)
}

func (ep *ingressEndpoint) allowSource(ip string) bool {
	parsed, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return ep.allowSourceAddr(parsed)
}

func (ep *ingressEndpoint) allowSourceAddr(parsed netip.Addr) bool {
	if !parsed.IsValid() {
		return false
	}
	if ep.item.ExpiresAt != nil && !time.Now().Before(*ep.item.ExpiresAt) {
		return false
	}
	prefixes := ep.sourceCIDRs
	if len(prefixes) == 0 && len(ep.item.SourceCIDRs) > 0 {
		// Endpoints created directly in focused tests may not have gone through
		// open(), so retain a compatible slow-path for those values.
		prefixes = make([]netip.Prefix, 0, len(ep.item.SourceCIDRs))
		for _, raw := range ep.item.SourceCIDRs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err == nil {
				prefixes = append(prefixes, prefix.Masked())
			}
		}
	}
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if prefix.Contains(parsed) {
			return true
		}
	}
	return false
}

func (m *IngressManager) audit(item *repository.RDPIngress, ip, result, code string, up, down int64) {
	if m.cfg.Audit == nil || item == nil {
		return
	}
	now := time.Now()
	m.cfg.Audit(&repository.ConnectionAudit{ClientDeviceID: "public:" + ip, ExitDeviceID: item.TargetDeviceID, Protocol: "rdp_pub", TargetHost: "127.0.0.1", TargetPort: 3389, StartedAt: now, EndedAt: now, BytesUp: up, BytesDown: down, Result: result, ErrorCode: code})
}

func (m *IngressManager) auditReject(ep *ingressEndpoint, ip, code string) {
	if ep == nil {
		return
	}
	if ep.auditLimiter == nil || ep.auditLimiter.allow(ip+"\x00"+code) {
		m.audit(ep.item, ip, "REJECTED", code, 0, 0)
	}
}

func sourceIP(address net.Addr) string {
	if address == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String()
	}
	return host
}

type sourceLimiter struct {
	mu      sync.Mutex
	limit   int
	started time.Time
	counts  map[string]int
}

func newSourceLimiter(limit int) *sourceLimiter {
	if limit <= 0 {
		limit = 120
	}
	return &sourceLimiter{limit: limit, started: time.Now(), counts: make(map[string]int)}
}

func (l *sourceLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.started) >= time.Minute {
		l.started = now
		l.counts = make(map[string]int)
	}
	if l.counts[ip] >= l.limit {
		return false
	}
	l.counts[ip]++
	if len(l.counts) > 8192 {
		return false
	}
	return true
}

var _ io.Closer = (*IngressManager)(nil)

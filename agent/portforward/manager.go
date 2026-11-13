// Package portforward owns local TCP/UDP listeners for authenticated remote
// service mappings. Transport implementations are supplied by the Agent and
// are responsible for P2P/Relay selection and remote-side authorization.
package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/tunnel"
)

type MappingProtocol string

const (
	ProtocolTCP  MappingProtocol = "tcp"
	ProtocolUDP  MappingProtocol = "udp"
	ProtocolBoth MappingProtocol = "tcp_udp"
)

type Path string

const (
	PathUnknown   Path = "unknown"
	PathP2PQUIC   Path = "p2p_quic"
	PathRelayQUIC Path = "relay_quic"
	PathRelayTLS  Path = "relay_tls"
)

type State string

const (
	StateStopped   State = "STOPPED"
	StateListening State = "LISTENING"
)

var (
	ErrManagerClosed       = errors.New("port forward manager is closed")
	ErrMappingExists       = errors.New("port forward mapping already exists")
	ErrMappingNotFound     = errors.New("port forward mapping not found")
	ErrUDPAssociationLimit = errors.New("UDP association capacity reached")
)

const maxUDPPayload = 65535

// Mapping describes one local-to-remote service mapping. ListenAddress must
// be an IP literal. Non-loopback listeners require AllowRemoteListen; wildcard
// listeners are never accepted. Target ACLs are enforced again by the target
// Agent and are not delegated to this local configuration.
type Mapping struct {
	ID                string
	Name              string
	RemoteDeviceID    string
	Protocol          MappingProtocol
	ListenAddress     string
	ListenPort        uint16
	TargetAddress     string
	TargetPort        uint16
	Enabled           bool
	AllowRemoteListen bool
}

// Transport is the data-plane boundary. Implementations should prefer an
// authenticated P2P path and fall back to an authorized Relay path. TCP and
// UDP are selected independently. OpenUDP must use unreliable datagrams.
type Transport interface {
	DialTCP(context.Context, Mapping) (net.Conn, Path, error)
	OpenUDP(context.Context, Mapping) (DatagramSession, Path, error)
}

// DatagramSession is one remote UDP association. Receive returns a payload
// owned by the caller. Close must unblock a pending Receive.
type DatagramSession interface {
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
}

type Options struct {
	OpenTimeout        time.Duration
	UDPIdleTimeout     time.Duration
	MaxTCPConnections  int
	MaxUDPAssociations int
	UDPQueueDepth      int
}

type Snapshot struct {
	Mapping
	State           State
	ListenAddr      string
	TCPPath         Path
	UDPPath         Path
	ActiveTCP       int64
	ActiveUDP       int64
	UDPAssociations int64
	UDPDrops        uint64
	BytesUp         uint64
	BytesDown       uint64
	LastError       string
}

type managerEntry struct {
	mapping Mapping
	runtime *mappingRuntime
}

// Manager owns configured mappings. Add starts enabled mappings immediately;
// StartMapping and StopMapping can toggle a mapping without deleting its
// configuration. Remove closes every listener and association for the mapping.
type Manager struct {
	ctx       context.Context
	transport Transport
	options   Options

	mu      sync.RWMutex
	entries map[string]*managerEntry
	closed  bool
}

func NewManager(parent context.Context, transport Transport, options Options) *Manager {
	if parent == nil {
		parent = context.Background()
	}
	if options.OpenTimeout <= 0 {
		options.OpenTimeout = 10 * time.Second
	}
	if options.UDPIdleTimeout <= 0 {
		options.UDPIdleTimeout = 2 * time.Minute
	}
	if options.MaxTCPConnections <= 0 {
		options.MaxTCPConnections = 128
	}
	if options.MaxUDPAssociations <= 0 {
		options.MaxUDPAssociations = 64
	}
	if options.UDPQueueDepth <= 0 {
		options.UDPQueueDepth = 16
	}
	return &Manager{
		ctx: parent, transport: transport, options: options,
		entries: make(map[string]*managerEntry),
	}
}

func (m *Manager) Add(mapping Mapping) error {
	if m == nil {
		return errors.New("nil port forward manager")
	}
	normalized, err := normalizeMapping(mapping)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return ErrManagerClosed
	}
	if _, exists := m.entries[normalized.ID]; exists {
		return ErrMappingExists
	}
	entry := &managerEntry{mapping: normalized}
	if normalized.Enabled {
		entry.runtime, err = newMappingRuntime(m.ctx, m.transport, m.options, normalized)
		if err != nil {
			return fmt.Errorf("start mapping %s: %w", normalized.ID, err)
		}
		entry.mapping = entry.runtime.mapping
	}
	m.entries[normalized.ID] = entry
	return nil
}

func (m *Manager) StartMapping(id string) error {
	if m == nil {
		return ErrManagerClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return ErrManagerClosed
	}
	entry := m.entries[id]
	if entry == nil {
		return ErrMappingNotFound
	}
	if entry.runtime != nil {
		entry.mapping.Enabled = true
		return nil
	}
	if m.transport == nil {
		return errors.New("port forward transport is not configured")
	}
	entry.mapping.Enabled = true
	runtime, err := newMappingRuntime(m.ctx, m.transport, m.options, entry.mapping)
	if err != nil {
		entry.mapping.Enabled = false
		return fmt.Errorf("start mapping %s: %w", id, err)
	}
	entry.runtime = runtime
	entry.mapping = runtime.mapping
	return nil
}

func (m *Manager) StopMapping(id string) error {
	if m == nil {
		return ErrManagerClosed
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	entry := m.entries[id]
	if entry == nil {
		m.mu.Unlock()
		return ErrMappingNotFound
	}
	runtime := entry.runtime
	entry.runtime = nil
	entry.mapping.Enabled = false
	m.mu.Unlock()
	if runtime != nil {
		return runtime.Close()
	}
	return nil
}

func (m *Manager) Remove(id string) error {
	if m == nil {
		return ErrManagerClosed
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	entry := m.entries[id]
	if entry == nil {
		m.mu.Unlock()
		return ErrMappingNotFound
	}
	delete(m.entries, id)
	m.mu.Unlock()
	if entry.runtime != nil {
		return entry.runtime.Close()
	}
	return nil
}

func (m *Manager) Get(id string) (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	m.mu.RLock()
	entry := m.entries[id]
	if entry == nil {
		m.mu.RUnlock()
		return Snapshot{}, false
	}
	mapping, runtime := entry.mapping, entry.runtime
	m.mu.RUnlock()
	if runtime != nil {
		return runtime.snapshot(), true
	}
	return Snapshot{
		Mapping: mapping, State: StateStopped,
		ListenAddr: net.JoinHostPort(mapping.ListenAddress, strconv.Itoa(int(mapping.ListenPort))),
	}, true
}

func (m *Manager) List() []Snapshot {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	out := make([]Snapshot, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry.runtime != nil {
			out = append(out, entry.runtime.snapshot())
			continue
		}
		mapping := entry.mapping
		out = append(out, Snapshot{
			Mapping: mapping, State: StateStopped,
			ListenAddr: net.JoinHostPort(mapping.ListenAddress, strconv.Itoa(int(mapping.ListenPort))),
		})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	runtimes := make([]*mappingRuntime, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry.runtime != nil {
			runtimes = append(runtimes, entry.runtime)
		}
	}
	m.entries = make(map[string]*managerEntry)
	m.mu.Unlock()
	var closeErrors []error
	for _, runtime := range runtimes {
		if err := runtime.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func normalizeMapping(mapping Mapping) (Mapping, error) {
	mapping.ID = strings.TrimSpace(mapping.ID)
	mapping.Name = strings.TrimSpace(mapping.Name)
	mapping.RemoteDeviceID = strings.TrimSpace(mapping.RemoteDeviceID)
	mapping.ListenAddress = strings.TrimSpace(mapping.ListenAddress)
	mapping.TargetAddress = strings.TrimSpace(mapping.TargetAddress)
	if mapping.ID == "" || mapping.RemoteDeviceID == "" {
		return Mapping{}, errors.New("mapping id and remote device id are required")
	}
	if mapping.Protocol != ProtocolTCP && mapping.Protocol != ProtocolUDP && mapping.Protocol != ProtocolBoth {
		return Mapping{}, fmt.Errorf("unsupported mapping protocol %q", mapping.Protocol)
	}
	if mapping.ListenAddress == "" {
		mapping.ListenAddress = "127.0.0.1"
	}
	listenIP := net.ParseIP(mapping.ListenAddress)
	if listenIP == nil || listenIP.IsUnspecified() || listenIP.IsMulticast() {
		return Mapping{}, errors.New("listen address must be a specific IP address")
	}
	if !mapping.AllowRemoteListen && !listenIP.IsLoopback() {
		return Mapping{}, errors.New("non-loopback listen address requires explicit permission")
	}
	if mapping.TargetAddress == "" || strings.ContainsAny(mapping.TargetAddress, "\x00\r\n/") {
		return Mapping{}, errors.New("valid target address is required")
	}
	if mapping.TargetPort == 0 {
		return Mapping{}, errors.New("target port must be between 1 and 65535")
	}
	if ip := net.ParseIP(mapping.TargetAddress); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return Mapping{}, errors.New("unspecified and multicast target addresses are not allowed")
		}
	} else if !validHostname(mapping.TargetAddress) {
		return Mapping{}, errors.New("target address must be an IP literal or valid hostname")
	}
	return mapping, nil
}

func validHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}

type mappingRuntime struct {
	ctx       context.Context
	cancel    context.CancelFunc
	transport Transport
	options   Options
	mapping   Mapping

	tcpListener net.Listener
	udpConn     net.PacketConn
	tcpSem      chan struct{}

	closed    atomic.Bool
	closeOnce sync.Once
	wg        sync.WaitGroup

	assocMu         sync.Mutex
	associations    map[string]*udpAssociation
	activeTCP       atomic.Int64
	activeUDP       atomic.Int64
	udpAssociations atomic.Int64
	udpDrops        atomic.Uint64
	bytesUp         atomic.Uint64
	bytesDown       atomic.Uint64
	tcpPath         atomic.Value
	udpPath         atomic.Value

	errorMu   sync.RWMutex
	lastError string
}

func newMappingRuntime(parent context.Context, transport Transport, options Options, mapping Mapping) (*mappingRuntime, error) {
	if transport == nil {
		return nil, errors.New("port forward transport is not configured")
	}
	ctx, cancel := context.WithCancel(parent)
	port := int(mapping.ListenPort)
	listenAddr := net.JoinHostPort(mapping.ListenAddress, strconv.Itoa(port))
	var tcpListener net.Listener
	var udpConn net.PacketConn
	var err error

	if mapping.Protocol == ProtocolTCP || mapping.Protocol == ProtocolBoth {
		tcpListener, err = net.Listen("tcp", listenAddr)
		if err != nil {
			cancel()
			return nil, err
		}
		port = tcpListener.Addr().(*net.TCPAddr).Port
	}
	if mapping.Protocol == ProtocolUDP || mapping.Protocol == ProtocolBoth {
		udpAddr := net.JoinHostPort(mapping.ListenAddress, strconv.Itoa(port))
		udpConn, err = net.ListenPacket("udp", udpAddr)
		if err != nil {
			if tcpListener != nil {
				_ = tcpListener.Close()
			}
			cancel()
			return nil, err
		}
		if port == 0 {
			port = udpConn.LocalAddr().(*net.UDPAddr).Port
		}
	}

	mapping.ListenPort = uint16(port)
	runtime := &mappingRuntime{
		ctx: ctx, cancel: cancel, transport: transport, options: options, mapping: mapping,
		tcpListener: tcpListener, udpConn: udpConn,
		tcpSem:       make(chan struct{}, options.MaxTCPConnections),
		associations: make(map[string]*udpAssociation),
	}
	context.AfterFunc(ctx, runtime.shutdown)
	if tcpListener != nil {
		runtime.wg.Add(1)
		go runtime.acceptTCP()
	}
	if udpConn != nil {
		runtime.wg.Add(1)
		go runtime.serveUDP()
	}
	return runtime, nil
}

func (r *mappingRuntime) shutdown() {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		r.cancel()
		if r.tcpListener != nil {
			_ = r.tcpListener.Close()
		}
		if r.udpConn != nil {
			_ = r.udpConn.Close()
		}
		r.assocMu.Lock()
		associations := make([]*udpAssociation, 0, len(r.associations))
		for _, association := range r.associations {
			associations = append(associations, association)
		}
		r.associations = make(map[string]*udpAssociation)
		r.udpAssociations.Store(0)
		r.assocMu.Unlock()
		for _, association := range associations {
			association.stop()
		}
	})
}

func (r *mappingRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.shutdown()
	r.wg.Wait()
	return nil
}

func (r *mappingRuntime) acceptTCP() {
	defer r.wg.Done()
	for {
		conn, err := r.tcpListener.Accept()
		if err != nil {
			if r.ctx.Err() != nil || r.closed.Load() {
				return
			}
			r.setError(fmt.Errorf("accept local TCP: %w", err))
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-r.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		select {
		case r.tcpSem <- struct{}{}:
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer func() { <-r.tcpSem }()
				r.handleTCP(conn)
			}()
		default:
			_ = conn.Close()
		}
	}
}

func (r *mappingRuntime) handleTCP(local net.Conn) {
	defer local.Close()
	openCtx, cancel := context.WithTimeout(r.ctx, r.options.OpenTimeout)
	remote, path, err := r.transport.DialTCP(openCtx, r.mapping)
	cancel()
	if err != nil {
		if r.ctx.Err() == nil {
			r.setError(fmt.Errorf("open remote TCP path: %w", err))
		}
		return
	}
	if remote == nil {
		r.setError(errors.New("transport returned a nil TCP connection"))
		return
	}
	if path == "" {
		path = PathUnknown
	}
	r.tcpPath.Store(path)
	r.setError(nil)
	r.activeTCP.Add(1)
	defer r.activeTCP.Add(-1)
	up, down := tunnel.Pipe(r.ctx, local, remote, 0, nil)
	if up > 0 {
		r.bytesUp.Add(uint64(up))
	}
	if down > 0 {
		r.bytesDown.Add(uint64(down))
	}
}

func (r *mappingRuntime) serveUDP() {
	defer r.wg.Done()
	buffer := make([]byte, maxUDPPayload)
	for {
		n, source, err := r.udpConn.ReadFrom(buffer)
		if err != nil {
			if r.ctx.Err() != nil || r.closed.Load() {
				return
			}
			r.setError(fmt.Errorf("read local UDP: %w", err))
			return
		}
		if source == nil || n > maxUDPPayload {
			continue
		}
		association, err := r.getAssociation(source)
		if err != nil {
			r.udpDrops.Add(1)
			r.setError(err)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if !association.enqueue(payload) {
			r.udpDrops.Add(1)
		}
	}
}

func (r *mappingRuntime) getAssociation(source net.Addr) (*udpAssociation, error) {
	key := source.String()
	r.assocMu.Lock()
	defer r.assocMu.Unlock()
	if r.closed.Load() || r.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	if existing := r.associations[key]; existing != nil {
		if !existing.closed.Load() {
			existing.touch()
			return existing, nil
		}
		delete(r.associations, key)
		r.udpAssociations.Add(-1)
	}
	if len(r.associations) >= r.options.MaxUDPAssociations {
		return nil, ErrUDPAssociationLimit
	}
	ctx, cancel := context.WithCancel(r.ctx)
	association := &udpAssociation{
		runtime: r, key: key, source: source, ctx: ctx, cancel: cancel,
		send: make(chan []byte, r.options.UDPQueueDepth),
	}
	association.touch()
	r.associations[key] = association
	r.udpAssociations.Add(1)
	r.wg.Add(1)
	go association.run()
	return association, nil
}

func (r *mappingRuntime) removeAssociation(association *udpAssociation) {
	r.assocMu.Lock()
	if r.associations[association.key] == association {
		delete(r.associations, association.key)
		r.udpAssociations.Add(-1)
	}
	r.assocMu.Unlock()
}

func (r *mappingRuntime) setError(err error) {
	r.errorMu.Lock()
	if err == nil {
		r.lastError = ""
	} else {
		r.lastError = err.Error()
	}
	r.errorMu.Unlock()
}

func (r *mappingRuntime) snapshot() Snapshot {
	snapshot := Snapshot{
		Mapping: r.mapping, State: StateListening,
		ListenAddr: net.JoinHostPort(r.mapping.ListenAddress, strconv.Itoa(int(r.mapping.ListenPort))),
		ActiveTCP:  r.activeTCP.Load(), ActiveUDP: r.activeUDP.Load(),
		UDPAssociations: r.udpAssociations.Load(), UDPDrops: r.udpDrops.Load(),
		BytesUp: r.bytesUp.Load(), BytesDown: r.bytesDown.Load(),
	}
	if r.closed.Load() {
		snapshot.State = StateStopped
	}
	if value := r.tcpPath.Load(); value != nil {
		snapshot.TCPPath = value.(Path)
	}
	if value := r.udpPath.Load(); value != nil {
		snapshot.UDPPath = value.(Path)
	}
	r.errorMu.RLock()
	snapshot.LastError = r.lastError
	r.errorMu.RUnlock()
	return snapshot
}

package direct

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	defaultClientAttemptTimeout = 2 * time.Second
	defaultClientCooldown       = 2 * time.Second
	maxClientCooldown           = 30 * time.Second
)

type ClientDialFunc func(context.Context, DialConfig) (tunnel.TunnelSession, error)
type ClientRaceDialFunc func(context.Context, []DialConfig) (tunnel.TunnelSession, string, error)

type ClientManagerOptions struct {
	AttemptTimeout time.Duration
	Cooldown       time.Duration
	MaxCooldown    time.Duration
	Dial           ClientDialFunc
	RaceDial       ClientRaceDialFunc
	Now            func() time.Time
	// EndpointUsable checks local address-family reachability before dialing.
	// An incompatible endpoint must not consume a one-time Direct ticket.
	EndpointUsable func(address string) bool
}

type ClientPathStatus struct {
	ExitDeviceID  string
	State         string
	Endpoint      string
	Error         string
	CooldownUntil time.Time
	FallbackCount uint64
}

type clientEntry struct {
	public        protocol.ProxyPublicDirectPath
	ticketHash    [sha256.Size]byte
	attempted     bool
	starting      bool
	session       tunnel.TunnelSession
	failureCount  int
	cooldownUntil time.Time
	lastError     string
	lastEndpoint  string
	fallbackCount uint64
}

type ClientManager struct {
	ctx            context.Context
	cancel         context.CancelFunc
	clientID       func() string
	raceDial       ClientRaceDialFunc
	endpointUsable func(address string) bool
	now            func() time.Time

	attemptTimeout time.Duration
	cooldown       time.Duration
	maxCooldown    time.Duration

	mu       sync.Mutex
	entries  map[string]*clientEntry
	fallback func(string, string)
	wg       sync.WaitGroup
	closed   atomic.Bool
}

func NewClientManager(parent context.Context, clientID func() string, options ClientManagerOptions) *ClientManager {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	attemptTimeout := options.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = defaultClientAttemptTimeout
	}
	cooldown := options.Cooldown
	if cooldown <= 0 {
		cooldown = defaultClientCooldown
	}
	maxCooldown := options.MaxCooldown
	if maxCooldown <= 0 {
		maxCooldown = maxClientCooldown
	}
	if maxCooldown < cooldown {
		maxCooldown = cooldown
	}
	raceDial := options.RaceDial
	if raceDial == nil {
		if options.Dial != nil {
			raceDial = func(ctx context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
				if len(configs) == 0 {
					return nil, "", errors.New("public direct dial requires at least one endpoint")
				}
				session, err := options.Dial(ctx, configs[0])
				return session, configs[0].Address, err
			}
		} else {
			raceDial = DialAny
		}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	endpointUsable := options.EndpointUsable
	if endpointUsable == nil {
		endpointUsable = localEndpointReachable
	}
	return &ClientManager{
		ctx: ctx, cancel: cancel, clientID: clientID, raceDial: raceDial, endpointUsable: endpointUsable, now: now,
		attemptTimeout: attemptTimeout, cooldown: cooldown, maxCooldown: maxCooldown,
		entries: make(map[string]*clientEntry),
	}
}

func (m *ClientManager) SetFallback(fn func(exitDeviceID, reason string)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.fallback = fn
	m.mu.Unlock()
}

func (m *ClientManager) Close() error {
	if m == nil || m.closed.Swap(true) {
		return nil
	}
	m.cancel()
	m.mu.Lock()
	sessions := make([]tunnel.TunnelSession, 0, len(m.entries))
	for _, entry := range m.entries {
		if entry.session != nil {
			sessions = append(sessions, entry.session)
		}
	}
	m.entries = make(map[string]*clientEntry)
	m.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
	m.wg.Wait()
	return nil
}

// UpdateInventory replaces the Server-authorized Public Direct snapshot.
// Removing Public Direct metadata closes an existing direct session immediately;
// a ticket expiry alone does not disconnect an already authenticated session.
func (m *ClientManager) UpdateInventory(exits []protocol.ProxyExit) {
	if m == nil || m.closed.Load() {
		return
	}
	now := m.now().UTC()
	next := make(map[string]protocol.ProxyPublicDirectPath)
	for _, item := range exits {
		exitID := strings.TrimSpace(item.DeviceID)
		if exitID == "" || exitID == protocol.ServerExitDeviceID || item.Direct == nil || item.Direct.Public == nil {
			continue
		}
		public := clonePublicDirectPath(*item.Direct.Public)
		if !public.Available || !strings.EqualFold(strings.TrimSpace(public.Transport), "quic") {
			continue
		}
		next[exitID] = public
	}

	var closeSessions []tunnel.TunnelSession
	m.mu.Lock()
	for exitID, entry := range m.entries {
		if _, ok := next[exitID]; ok {
			continue
		}
		if entry.session != nil {
			closeSessions = append(closeSessions, entry.session)
		}
		delete(m.entries, exitID)
	}
	for exitID, public := range next {
		hash := sha256.Sum256(public.Ticket)
		entry := m.entries[exitID]
		if entry == nil {
			entry = &clientEntry{}
			m.entries[exitID] = entry
		}
		if entry.ticketHash != hash {
			entry.ticketHash = hash
			entry.attempted = false
		}
		entry.public = public
		if entry.session == nil {
			switch {
			case !publicPathUsable(public, now):
				entry.lastError = "public direct credentials unavailable"
			case len(m.reachablePublicEndpoints(public)) == 0:
				entry.lastError = "public direct unavailable: no endpoint matches the local network IP family"
			case !entry.attempted:
				entry.lastError = ""
			}
		}
	}
	m.mu.Unlock()
	for _, session := range closeSessions {
		_ = session.Close()
	}
}

func (m *ClientManager) ReadyForExit(exitDeviceID string) (tunnel.TunnelSession, bool) {
	if m == nil || strings.TrimSpace(exitDeviceID) == "" || m.closed.Load() {
		return nil, false
	}
	m.mu.Lock()
	entry := m.entries[exitDeviceID]
	if entry == nil || entry.session == nil {
		m.mu.Unlock()
		return nil, false
	}
	session := entry.session
	if sessionDone(session) {
		entry.session = nil
		m.mu.Unlock()
		return nil, false
	}
	m.mu.Unlock()
	return session, true
}

// PreferForExit reports whether Public Direct should suppress P2P for this Exit.
// It is true while Public Direct is READY, actively connecting, or has one fresh
// unconsumed ticket that can be attempted now.
func (m *ClientManager) PreferForExit(exitDeviceID string) bool {
	if m == nil || strings.TrimSpace(exitDeviceID) == "" || m.closed.Load() {
		return false
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[exitDeviceID]
	if entry == nil {
		return false
	}
	if entry.session != nil {
		if !sessionDone(entry.session) {
			return true
		}
		entry.session = nil
	}
	if entry.starting {
		return true
	}
	if entry.attempted || now.Before(entry.cooldownUntil) {
		return false
	}
	return publicPathUsable(entry.public, now) && len(m.reachablePublicEndpoints(entry.public)) > 0
}

// EnsureClient starts at most one bounded attempt. A ticket is marked consumed
// for retry purposes before dialing: after an ambiguous network failure the same
// nonce is never sent again.
func (m *ClientManager) EnsureClient(exitDeviceID string) bool {
	if m == nil || m.closed.Load() {
		return false
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if exitDeviceID == "" || exitDeviceID == protocol.ServerExitDeviceID {
		return false
	}
	clientID := ""
	if m.clientID != nil {
		clientID = strings.TrimSpace(m.clientID())
	}
	if clientID == "" {
		return false
	}
	now := m.now().UTC()

	m.mu.Lock()
	entry := m.entries[exitDeviceID]
	if entry == nil || entry.starting || entry.attempted || now.Before(entry.cooldownUntil) ||
		!publicPathUsable(entry.public, now) {
		m.mu.Unlock()
		return false
	}
	if entry.session != nil {
		if !sessionDone(entry.session) {
			m.mu.Unlock()
			return true
		}
		entry.session = nil
	}
	endpoints := m.reachablePublicEndpoints(entry.public)
	if len(endpoints) == 0 {
		entry.lastEndpoint = ""
		entry.lastError = "public direct unavailable: no endpoint matches the local network IP family"
		m.mu.Unlock()
		return false
	}
	entry.starting = true
	entry.attempted = true
	entry.lastEndpoint = publicEndpointDialAddress(endpoints[0])
	public := clonePublicDirectPath(entry.public)
	ticketHash := entry.ticketHash
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		m.connect(exitDeviceID, clientID, endpoints, public, ticketHash)
	}()
	return true
}

func (m *ClientManager) connect(exitDeviceID, clientID string, endpoints []protocol.PublicDirectEndpoint, public protocol.ProxyPublicDirectPath, ticketHash [sha256.Size]byte) {
	ctx, cancel := context.WithTimeout(m.ctx, m.attemptTimeout)
	defer cancel()

	_, stillValid := ticketLifetime(public, m.now().UTC())
	if !stillValid {
		m.finishFailure(exitDeviceID, ticketHash, errors.New("public direct ticket expired before connection"))
		return
	}
	configs := make([]DialConfig, 0, len(endpoints))
	for _, endpoint := range endpoints {
		tlsConfig, err := PinnedTLSConfig(endpoint.CertFingerprint)
		if err != nil {
			continue
		}
		configs = append(configs, DialConfig{
			Address: publicEndpointDialAddress(endpoint), TLSConfig: tlsConfig,
			ClientDeviceID: clientID, ExitDeviceID: exitDeviceID,
			Ticket:      append([]byte(nil), public.Ticket...),
			AuthTimeout: m.attemptTimeout,
		})
	}
	if len(configs) == 0 {
		m.finishFailure(exitDeviceID, ticketHash, errors.New("public direct has no endpoint with a valid certificate fingerprint"))
		return
	}
	session, selectedAddress, err := m.raceDial(ctx, configs)
	if err != nil {
		m.finishFailure(exitDeviceID, ticketHash, err)
		return
	}

	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		_ = session.Close()
		return
	}
	entry := m.entries[exitDeviceID]
	if entry == nil {
		m.mu.Unlock()
		_ = session.Close()
		return
	}
	if entry.ticketHash != ticketHash {
		entry.starting = false
		m.mu.Unlock()
		_ = session.Close()
		return
	}
	entry.starting = false
	if entry.session != nil && !sessionDone(entry.session) {
		m.mu.Unlock()
		_ = session.Close()
		return
	}
	entry.session = session
	if strings.TrimSpace(selectedAddress) != "" {
		entry.lastEndpoint = strings.TrimSpace(selectedAddress)
	}
	entry.failureCount = 0
	entry.cooldownUntil = time.Time{}
	entry.lastError = ""
	m.mu.Unlock()

	m.wg.Add(1)
	go m.watchSession(exitDeviceID, session)
}

func (m *ClientManager) watchSession(exitDeviceID string, session tunnel.TunnelSession) {
	defer m.wg.Done()
	select {
	case <-m.ctx.Done():
		return
	case <-session.Done():
	}
	// Public Direct owns its client UDP socket. Explicitly close a remotely
	// terminated session so the socket is released even though Done is already
	// closed. Close is required to be idempotent by TunnelSession implementations.
	_ = session.Close()
	if m.closed.Load() {
		return
	}
	var fallback func(string, string)
	m.mu.Lock()
	entry := m.entries[exitDeviceID]
	if entry == nil || entry.session != session {
		m.mu.Unlock()
		return
	}
	entry.session = nil
	m.recordFailureLocked(entry, "public direct session closed")
	fallback = m.fallback
	m.mu.Unlock()
	if fallback != nil {
		fallback(exitDeviceID, "public direct session closed")
	}
}

func (m *ClientManager) FailReadyForExit(exitDeviceID, reason string) {
	if m == nil || m.closed.Load() {
		return
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if reason == "" {
		reason = "public direct path failed"
	}
	var session tunnel.TunnelSession
	var fallback func(string, string)
	m.mu.Lock()
	entry := m.entries[exitDeviceID]
	if entry != nil {
		session = entry.session
		entry.session = nil
		entry.starting = false
		m.recordFailureLocked(entry, reason)
		fallback = m.fallback
	}
	m.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	if fallback != nil {
		fallback(exitDeviceID, reason)
	}
}

func (m *ClientManager) NoteFallback(exitDeviceID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if entry := m.entries[strings.TrimSpace(exitDeviceID)]; entry != nil {
		entry.fallbackCount++
	}
	m.mu.Unlock()
}

func (m *ClientManager) PathStatus(exitDeviceID string) (ClientPathStatus, bool) {
	if m == nil {
		return ClientPathStatus{}, false
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[exitDeviceID]
	if entry == nil {
		return ClientPathStatus{}, false
	}
	state := "AVAILABLE"
	if entry.session != nil && !sessionDone(entry.session) {
		state = "READY"
	} else if entry.starting {
		state = "CONNECTING"
	} else if m.now().UTC().Before(entry.cooldownUntil) {
		state = "COOLDOWN"
	} else if entry.attempted {
		state = "FAILED"
	} else if !publicPathUsable(entry.public, m.now().UTC()) ||
		len(m.reachablePublicEndpoints(entry.public)) == 0 {
		state = "UNAVAILABLE"
	}
	reason := entry.lastError
	endpoint := entry.lastEndpoint
	if state == "UNAVAILABLE" && publicPathUsable(entry.public, m.now().UTC()) &&
		len(m.reachablePublicEndpoints(entry.public)) == 0 {
		reason = "public direct unavailable: no endpoint matches the local network IP family"
		endpoint = ""
	}
	return ClientPathStatus{
		ExitDeviceID: exitDeviceID, State: state, Endpoint: endpoint,
		Error: reason, CooldownUntil: entry.cooldownUntil,
		FallbackCount: entry.fallbackCount,
	}, true
}

func (m *ClientManager) finishFailure(exitDeviceID string, ticketHash [sha256.Size]byte, err error) {
	reason := "public direct connection failed"
	if err != nil {
		reason = err.Error()
	}
	var fallback func(string, string)
	m.mu.Lock()
	entry := m.entries[exitDeviceID]
	if entry != nil {
		if entry.ticketHash != ticketHash {
			entry.starting = false
		} else {
			entry.starting = false
			m.recordFailureLocked(entry, reason)
			fallback = m.fallback
		}
	}
	m.mu.Unlock()
	if fallback != nil {
		fallback(exitDeviceID, reason)
	}
}

func (m *ClientManager) recordFailureLocked(entry *clientEntry, reason string) {
	entry.failureCount++
	entry.lastError = reason
	delay := m.cooldown
	for i := 1; i < entry.failureCount && delay < m.maxCooldown; i++ {
		delay *= 2
		if delay >= m.maxCooldown {
			delay = m.maxCooldown
			break
		}
	}
	entry.cooldownUntil = m.now().UTC().Add(delay)
}

func clonePublicDirectPath(value protocol.ProxyPublicDirectPath) protocol.ProxyPublicDirectPath {
	value.Ticket = append([]byte(nil), value.Ticket...)
	value.Endpoints = append([]protocol.PublicDirectEndpoint(nil), value.Endpoints...)
	return value
}

func publicPathUsable(value protocol.ProxyPublicDirectPath, now time.Time) bool {
	if !value.Available || !strings.EqualFold(strings.TrimSpace(value.Transport), "quic") ||
		len(value.Ticket) == 0 {
		return false
	}
	if _, ok := ticketLifetime(value, now); !ok {
		return false
	}
	_, ok := selectPublicEndpoint(value)
	return ok
}

func ticketLifetime(value protocol.ProxyPublicDirectPath, now time.Time) (time.Duration, bool) {
	if value.TicketExpiresAt <= 0 {
		return 0, false
	}
	remaining := time.Unix(value.TicketExpiresAt, 0).Sub(now)
	return remaining, remaining > 0
}

func selectPublicEndpoint(value protocol.ProxyPublicDirectPath) (protocol.PublicDirectEndpoint, bool) {
	items := selectPublicEndpoints(value)
	if len(items) == 0 {
		return protocol.PublicDirectEndpoint{}, false
	}
	return items[0], true
}

func selectPublicEndpoints(value protocol.ProxyPublicDirectPath) []protocol.PublicDirectEndpoint {
	items := make([]protocol.PublicDirectEndpoint, 0, len(value.Endpoints))
	for _, endpoint := range value.Endpoints {
		if !endpoint.Verified ||
			endpoint.Protocol != protocol.PublicDirectEndpointProtocolUDP ||
			strings.TrimSpace(endpoint.Address) == "" ||
			strings.TrimSpace(endpoint.CertFingerprint) == "" {
			continue
		}
		items = append(items, endpoint)
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := clientEndpointPriority(items[i].Source), clientEndpointPriority(items[j].Source)
		if left != right {
			return left > right
		}
		return items[i].Address < items[j].Address
	})
	return items
}

// reachablePublicEndpoints preserves Server verification and source priority
// while excluding network families the local OS cannot route. This check is
// repeated for every attempt, so switching Wi-Fi/VPN can restore IPv6 paths.
func (m *ClientManager) reachablePublicEndpoints(value protocol.ProxyPublicDirectPath) []protocol.PublicDirectEndpoint {
	endpoints := selectPublicEndpoints(value)
	if m.endpointUsable == nil {
		return endpoints
	}
	usable := endpoints[:0]
	for _, endpoint := range endpoints {
		if m.endpointUsable(publicEndpointDialAddress(endpoint)) {
			usable = append(usable, endpoint)
		}
	}
	return usable
}

func publicEndpointDialAddress(endpoint protocol.PublicDirectEndpoint) string {
	if address := strings.TrimSpace(endpoint.DialAddress); address != "" {
		return address
	}
	return strings.TrimSpace(endpoint.Address)
}

func clientEndpointPriority(source string) int {
	switch source {
	case protocol.PublicDirectEndpointManual:
		return 3
	case protocol.PublicDirectEndpointObserved:
		return 2
	case protocol.PublicDirectEndpointIPv6:
		return 1
	default:
		return 0
	}
}

func sessionDone(session tunnel.TunnelSession) bool {
	if session == nil {
		return true
	}
	select {
	case <-session.Done():
		return true
	default:
		return false
	}
}

func (m *ClientManager) String() string {
	if m == nil {
		return "public-direct-client(nil)"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("public-direct-client(exits=%d)", len(m.entries))
}

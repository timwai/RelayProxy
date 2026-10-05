package direct

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

type VerificationState string

const (
	StateUnknown   VerificationState = "unknown"
	StateVerifying VerificationState = "verifying"
	StateVerified  VerificationState = "verified"
	StateFailed    VerificationState = "failed"
	StateExpired   VerificationState = "expired"
)

type EndpointStatus struct {
	Endpoint protocol.PublicDirectEndpoint
	State    VerificationState
	Error    string
}

type RegistrationSnapshot struct {
	DeviceID       string
	RegistrationID string
	ExpiresAt      time.Time
	Endpoints      []EndpointStatus
}

type VerificationTarget struct {
	DeviceID       string
	RegistrationID string
	Generation     uint64
	Endpoint       protocol.PublicDirectEndpoint
	Secret         []byte
}

type endpointRecord struct {
	endpoint protocol.PublicDirectEndpoint
	state    VerificationState
	err      string
}

type registration struct {
	owner           *session.DeviceSession
	id              string
	generation      uint64
	secret          []byte
	registeredAt    time.Time
	registrationTTL time.Duration
	endpoints       []endpointRecord
}

type Registry struct {
	mu         sync.RWMutex
	ttl        time.Duration
	generation uint64
	byDevice   map[string]*registration
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Registry{ttl: ttl, byDevice: make(map[string]*registration)}
}

func (r *Registry) Register(
	owner *session.DeviceSession,
	registrationID string,
	secret []byte,
	listenerPort int,
	candidates []protocol.PublicDirectEndpoint,
) ([]VerificationTarget, error) {
	if r == nil {
		return nil, errors.New("Public Direct registry is unavailable")
	}
	if owner == nil || owner.DeviceID == "" || !owner.IsExit() {
		return nil, errors.New("Public Direct registration requires an authenticated Exit session")
	}
	if registrationID == "" {
		return nil, errors.New("Public Direct registration id is required")
	}
	if len(secret) != directtransport.VerificationSecretSize {
		return nil, errors.New("Public Direct verification secret has invalid size")
	}
	if listenerPort < 1 || listenerPort > 65535 {
		return nil, errors.New("Public Direct listener port is invalid")
	}
	if len(candidates) > 16 {
		return nil, errors.New("too many Public Direct endpoint candidates")
	}

	normalized := make([]protocol.PublicDirectEndpoint, 0, len(candidates)+1)
	seen := make(map[string]struct{}, len(candidates)+1)
	if observed, ok := observedEndpoint(owner, listenerPort); ok {
		normalized = appendUniqueEndpoint(normalized, seen, observed)
	}
	for _, candidate := range candidates {
		endpoint, err := normalizeReportedEndpoint(candidate, listenerPort)
		if err != nil {
			return nil, err
		}
		normalized = appendUniqueEndpoint(normalized, seen, endpoint)
	}
	if len(normalized) == 0 {
		return nil, errors.New("Public Direct registration contains no verifiable public endpoints")
	}

	now := time.Now()
	r.mu.Lock()
	r.generation++
	reg := &registration{
		owner:           owner,
		id:              registrationID,
		generation:      r.generation,
		secret:          directtransport.CloneSecret(secret),
		registeredAt:    now,
		registrationTTL: r.ttl,
		endpoints:       make([]endpointRecord, len(normalized)),
	}
	targets := make([]VerificationTarget, 0, len(normalized))
	for i, endpoint := range normalized {
		reg.endpoints[i] = endpointRecord{endpoint: endpoint, state: StateUnknown}
		targets = append(targets, VerificationTarget{
			DeviceID:       owner.DeviceID,
			RegistrationID: registrationID,
			Generation:     reg.generation,
			Endpoint:       endpoint,
			Secret:         directtransport.CloneSecret(secret),
		})
	}
	r.byDevice[owner.DeviceID] = reg
	r.mu.Unlock()
	return targets, nil
}

func (r *Registry) MarkVerifying(target VerificationTarget) bool {
	return r.updateTarget(target, func(record *endpointRecord, _ time.Time) {
		record.state = StateVerifying
		record.err = ""
	})
}

func (r *Registry) MarkVerified(target VerificationTarget) bool {
	return r.updateTarget(target, func(record *endpointRecord, now time.Time) {
		record.state = StateVerified
		record.err = ""
		record.endpoint.Verified = true
		record.endpoint.VerifiedAt = now.Unix()
		record.endpoint.ExpiresAt = now.Add(r.ttl).Unix()
	})
}

func (r *Registry) MarkFailed(target VerificationTarget, err error) bool {
	return r.updateTarget(target, func(record *endpointRecord, _ time.Time) {
		record.state = StateFailed
		record.endpoint.Verified = false
		record.endpoint.VerifiedAt = 0
		record.endpoint.ExpiresAt = 0
		if err != nil {
			record.err = err.Error()
		} else {
			record.err = "verification failed"
		}
	})
}

func (r *Registry) updateTarget(target VerificationTarget, update func(*endpointRecord, time.Time)) bool {
	if r == nil || update == nil {
		return false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.byDevice[target.DeviceID]
	if reg == nil || reg.id != target.RegistrationID || reg.generation != target.Generation {
		return false
	}
	for i := range reg.endpoints {
		if sameEndpoint(reg.endpoints[i].endpoint, target.Endpoint) {
			update(&reg.endpoints[i], now)
			return true
		}
	}
	return false
}

func (r *Registry) VerifiedEndpoints(deviceID string) []protocol.PublicDirectEndpoint {
	if r == nil || deviceID == "" {
		return nil
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.byDevice[deviceID]
	if reg == nil {
		return nil
	}
	out := make([]protocol.PublicDirectEndpoint, 0, len(reg.endpoints))
	for i := range reg.endpoints {
		record := &reg.endpoints[i]
		if record.state == StateVerified && record.endpoint.ExpiresAt > now.Unix() {
			out = append(out, record.endpoint)
			continue
		}
		if record.state == StateVerified {
			record.state = StateExpired
			record.endpoint.Verified = false
		}
	}
	return out
}

func (r *Registry) Snapshot(deviceID string) (RegistrationSnapshot, bool) {
	if r == nil {
		return RegistrationSnapshot{}, false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.byDevice[deviceID]
	if reg == nil {
		return RegistrationSnapshot{}, false
	}
	snapshot := RegistrationSnapshot{
		DeviceID:       deviceID,
		RegistrationID: reg.id,
		ExpiresAt:      reg.registeredAt.Add(reg.registrationTTL),
		Endpoints:      make([]EndpointStatus, len(reg.endpoints)),
	}
	for i := range reg.endpoints {
		record := &reg.endpoints[i]
		if record.state == StateVerified && record.endpoint.ExpiresAt <= now.Unix() {
			record.state = StateExpired
			record.endpoint.Verified = false
		}
		snapshot.Endpoints[i] = EndpointStatus{Endpoint: record.endpoint, State: record.state, Error: record.err}
	}
	return snapshot, true
}

func (r *Registry) InvalidateSession(owner *session.DeviceSession) bool {
	if r == nil || owner == nil || owner.DeviceID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	reg := r.byDevice[owner.DeviceID]
	if reg == nil || reg.owner != owner {
		return false
	}
	delete(r.byDevice, owner.DeviceID)
	return true
}

func (r *Registry) RevokeDevice(deviceID string) bool {
	if r == nil || deviceID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byDevice[deviceID] == nil {
		return false
	}
	delete(r.byDevice, deviceID)
	return true
}

func observedEndpoint(owner *session.DeviceSession, listenerPort int) (protocol.PublicDirectEndpoint, bool) {
	if owner == nil || owner.Tunnel == nil || owner.Tunnel.RemoteAddr() == nil {
		return protocol.PublicDirectEndpoint{}, false
	}
	host, _, err := net.SplitHostPort(owner.Tunnel.RemoteAddr().String())
	if err != nil {
		return protocol.PublicDirectEndpoint{}, false
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !isPublicIP(addr) || !addr.Is4() {
		return protocol.PublicDirectEndpoint{}, false
	}
	return protocol.PublicDirectEndpoint{
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  net.JoinHostPort(addr.String(), strconv.Itoa(listenerPort)),
		Source:   protocol.PublicDirectEndpointSourceObserved,
	}, true
}

func normalizeReportedEndpoint(candidate protocol.PublicDirectEndpoint, listenerPort int) (protocol.PublicDirectEndpoint, error) {
	if candidate.Protocol != protocol.PublicDirectEndpointProtocolUDP {
		return protocol.PublicDirectEndpoint{}, errors.New("Public Direct endpoint protocol must be udp")
	}
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(candidate.Address))
	if err != nil || strings.TrimSpace(host) == "" {
		return protocol.PublicDirectEndpoint{}, errors.New("Public Direct endpoint address must be host:port")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return protocol.PublicDirectEndpoint{}, errors.New("Public Direct endpoint port is invalid")
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	switch candidate.Source {
	case protocol.PublicDirectEndpointSourceIPv6:
		addr, err := netip.ParseAddr(host)
		if err != nil || !addr.Is6() || !isPublicIP(addr) {
			return protocol.PublicDirectEndpoint{}, errors.New("Public Direct IPv6 endpoint must be a global public IPv6 address")
		}
		if port != listenerPort {
			return protocol.PublicDirectEndpoint{}, errors.New("Public Direct IPv6 endpoint must use the listener port")
		}
		host = addr.String()
	case protocol.PublicDirectEndpointSourceManual:
		if strings.ContainsAny(host, " /\\\t\r\n") {
			return protocol.PublicDirectEndpoint{}, errors.New("Public Direct manual endpoint host is invalid")
		}
		if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
			if !isPublicIP(addr) {
				return protocol.PublicDirectEndpoint{}, errors.New("Public Direct manual IP endpoint must be public")
			}
			host = addr.String()
		}
	default:
		return protocol.PublicDirectEndpoint{}, errors.New("Public Direct endpoint source must be ipv6 or manual")
	}
	return protocol.PublicDirectEndpoint{
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  net.JoinHostPort(host, strconv.Itoa(port)),
		Source:   candidate.Source,
	}, nil
}

func isPublicIP(addr netip.Addr) bool {
	return addr.IsValid() &&
		addr.IsGlobalUnicast() &&
		!addr.IsPrivate() &&
		!addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() &&
		!addr.IsMulticast() &&
		!addr.IsUnspecified()
}

func appendUniqueEndpoint(
	out []protocol.PublicDirectEndpoint,
	seen map[string]struct{},
	endpoint protocol.PublicDirectEndpoint,
) []protocol.PublicDirectEndpoint {
	key := endpoint.Protocol + "\x00" + endpoint.Address
	if _, exists := seen[key]; exists {
		return out
	}
	seen[key] = struct{}{}
	return append(out, endpoint)
}

func sameEndpoint(a, b protocol.PublicDirectEndpoint) bool {
	return a.Protocol == b.Protocol && a.Address == b.Address && a.Source == b.Source
}

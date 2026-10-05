package direct

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"relayproxy/internal/protocol"
)

type VerificationState string

const (
	StateUnknown   VerificationState = "unknown"
	StateVerifying VerificationState = "verifying"
	StateVerified  VerificationState = "verified"
	StateFailed    VerificationState = "failed"
	StateExpired   VerificationState = "expired"
)

type EndpointRecord struct {
	DeviceID        string
	SessionID       string
	NetworkEpoch    uint64
	CertFingerprint string
	Endpoint        protocol.PublicDirectEndpoint
	State           VerificationState
	RegisteredAt    time.Time
	VerifiedAt      time.Time
	ExpiresAt       time.Time
	LastError       string
}

const maxEndpointCandidates = 16

type Registry struct {
	mu      sync.Mutex
	records map[string]map[string]EndpointRecord
	now     func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{records: make(map[string]map[string]EndpointRecord), now: time.Now}
}

func (r *Registry) Register(deviceID, sessionID string, observedIP netip.Addr, request protocol.PublicDirectRegistrationRequest) ([]EndpointRecord, error) {
	deviceID, sessionID = strings.TrimSpace(deviceID), strings.TrimSpace(sessionID)
	if deviceID == "" || sessionID == "" {
		return nil, errors.New("public direct registration requires authenticated device and session ids")
	}
	fingerprint, err := normalizeFingerprint(request.CertFingerprint)
	if err != nil {
		return nil, err
	}
	if len(request.Candidates) > maxEndpointCandidates {
		return nil, fmt.Errorf("public direct registration has too many endpoint candidates: %d", len(request.Candidates))
	}
	candidates := make([]protocol.PublicDirectEndpointCandidate, 0, len(request.Candidates)+1)
	if request.ListenerPort != 0 && observedIP.IsValid() && observedIP.Is4() && isPublicIP(observedIP) {
		candidates = append(candidates, protocol.PublicDirectEndpointCandidate{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  net.JoinHostPort(observedIP.String(), strconv.Itoa(int(request.ListenerPort))),
			Source:   protocol.PublicDirectEndpointObserved,
		})
	}
	for _, candidate := range request.Candidates {
		normalized, err := normalizeCandidate(candidate)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, normalized)
	}
	if len(candidates) == 0 {
		return nil, errors.New("public direct registration has no verifiable endpoint candidates")
	}

	now := r.now()
	next := make(map[string]EndpointRecord, len(candidates))
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := r.records[deviceID]
	var currentEpoch uint64
	for _, old := range previous {
		if old.SessionID == sessionID && old.NetworkEpoch > currentEpoch {
			currentEpoch = old.NetworkEpoch
		}
	}
	if request.NetworkEpoch < currentEpoch {
		return nil, fmt.Errorf("stale public direct network epoch %d; current epoch is %d", request.NetworkEpoch, currentEpoch)
	}
	for _, candidate := range candidates {
		if _, exists := next[candidate.Address]; exists {
			continue
		}
		record := EndpointRecord{
			DeviceID: deviceID, SessionID: sessionID, NetworkEpoch: request.NetworkEpoch,
			CertFingerprint: fingerprint,
			Endpoint: protocol.PublicDirectEndpoint{
				Protocol: candidate.Protocol, Address: candidate.Address, Source: candidate.Source,
				CertFingerprint: fingerprint,
			},
			State: StateUnknown, RegisteredAt: now,
		}
		if old, ok := previous[candidate.Address]; ok &&
			old.SessionID == sessionID && old.NetworkEpoch == request.NetworkEpoch &&
			old.CertFingerprint == fingerprint && old.Endpoint.Protocol == candidate.Protocol &&
			old.Endpoint.Source == candidate.Source {
			record.State = old.State
			record.RegisteredAt = old.RegisteredAt
			record.VerifiedAt = old.VerifiedAt
			record.ExpiresAt = old.ExpiresAt
			record.LastError = old.LastError
			record.Endpoint.Verified = old.Endpoint.Verified && old.State == StateVerified && old.ExpiresAt.After(now)
			if record.Endpoint.Verified {
				record.Endpoint.DialAddress = old.Endpoint.DialAddress
			}
			if old.State == StateVerified && !old.ExpiresAt.After(now) {
				record.State = StateExpired
				record.Endpoint.Verified = false
				record.Endpoint.DialAddress = ""
			}
		}
		next[candidate.Address] = record
	}
	r.records[deviceID] = next
	return cloneRecords(next), nil
}

func (r *Registry) Lookup(deviceID, sessionID, address string) (EndpointRecord, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[strings.TrimSpace(deviceID)][strings.TrimSpace(address)]
	if !ok || record.SessionID != strings.TrimSpace(sessionID) {
		return EndpointRecord{}, false
	}
	record = r.expireLocked(record)
	if bucket := r.records[record.DeviceID]; bucket != nil {
		bucket[record.Endpoint.Address] = record
	}
	return record, true
}

func sameRegistration(current, expected EndpointRecord) bool {
	return current.DeviceID == expected.DeviceID &&
		current.SessionID == expected.SessionID &&
		current.NetworkEpoch == expected.NetworkEpoch &&
		current.CertFingerprint == expected.CertFingerprint &&
		current.Endpoint.Protocol == expected.Endpoint.Protocol &&
		current.Endpoint.Address == expected.Endpoint.Address &&
		current.Endpoint.Source == expected.Endpoint.Source
}

func (r *Registry) updateRegistration(expected EndpointRecord, fn func(*EndpointRecord, time.Time)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.records[expected.DeviceID]
	current, ok := bucket[expected.Endpoint.Address]
	if !ok || !sameRegistration(current, expected) {
		return false
	}
	fn(&current, r.now())
	bucket[expected.Endpoint.Address] = current
	return true
}

func (r *Registry) markVerifyingRegistration(expected EndpointRecord) bool {
	return r.updateRegistration(expected, func(record *EndpointRecord, now time.Time) {
		record.State = StateVerifying
		record.Endpoint.Verified = false
		record.Endpoint.DialAddress = ""
		record.LastError = ""
		record.VerifiedAt = time.Time{}
		record.ExpiresAt = time.Time{}
	})
}

func (r *Registry) markVerifiedRegistration(expected EndpointRecord, dialAddress string, ttl time.Duration) bool {
	dialAddress = strings.TrimSpace(dialAddress)
	if ttl <= 0 || dialAddress == "" {
		return false
	}
	return r.updateRegistration(expected, func(record *EndpointRecord, now time.Time) {
		record.State = StateVerified
		record.Endpoint.Verified = true
		record.Endpoint.DialAddress = dialAddress
		record.VerifiedAt = now
		record.ExpiresAt = now.Add(ttl)
		record.LastError = ""
	})
}

func (r *Registry) markFailedRegistration(expected EndpointRecord, err error) bool {
	return r.updateRegistration(expected, func(record *EndpointRecord, now time.Time) {
		record.State = StateFailed
		record.Endpoint.Verified = false
		record.Endpoint.DialAddress = ""
		record.VerifiedAt = time.Time{}
		record.ExpiresAt = time.Time{}
		if err != nil {
			record.LastError = err.Error()
		} else {
			record.LastError = "verification failed"
		}
	})
}

func (r *Registry) MarkVerifying(deviceID, sessionID, address string) bool {
	return r.update(deviceID, sessionID, address, func(record *EndpointRecord, now time.Time) {
		record.State = StateVerifying
		record.Endpoint.Verified = false
		record.LastError = ""
		record.VerifiedAt = time.Time{}
		record.ExpiresAt = time.Time{}
	})
}

func (r *Registry) MarkVerified(deviceID, sessionID, address string, ttl time.Duration) bool {
	return r.MarkVerifiedAddress(deviceID, sessionID, address, address, ttl)
}

func (r *Registry) MarkVerifiedAddress(deviceID, sessionID, address, dialAddress string, ttl time.Duration) bool {
	dialAddress = strings.TrimSpace(dialAddress)
	if ttl <= 0 || dialAddress == "" {
		return false
	}
	return r.update(deviceID, sessionID, address, func(record *EndpointRecord, now time.Time) {
		record.State = StateVerified
		record.Endpoint.Verified = true
		record.Endpoint.DialAddress = dialAddress
		record.VerifiedAt = now
		record.ExpiresAt = now.Add(ttl)
		record.LastError = ""
	})
}

func (r *Registry) MarkFailed(deviceID, sessionID, address string, err error) bool {
	return r.update(deviceID, sessionID, address, func(record *EndpointRecord, now time.Time) {
		record.State = StateFailed
		record.Endpoint.Verified = false
		record.VerifiedAt = time.Time{}
		record.ExpiresAt = time.Time{}
		if err != nil {
			record.LastError = err.Error()
		} else {
			record.LastError = "verification failed"
		}
	})
}

func (r *Registry) VerifiedEndpoints(deviceID string) []protocol.PublicDirectEndpoint {
	deviceID = strings.TrimSpace(deviceID)
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.records[deviceID]
	out := make([]protocol.PublicDirectEndpoint, 0, len(bucket))
	for address, record := range bucket {
		record = r.expireLocked(record)
		bucket[address] = record
		if record.State == StateVerified && record.Endpoint.Verified {
			out = append(out, record.Endpoint)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := publicEndpointPriority(out[i].Source), publicEndpointPriority(out[j].Source)
		if left != right {
			return left > right
		}
		return out[i].Address < out[j].Address
	})
	return out
}

func (r *Registry) Snapshot(deviceID string) []EndpointRecord {
	deviceID = strings.TrimSpace(deviceID)
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.records[deviceID]
	for address, record := range bucket {
		bucket[address] = r.expireLocked(record)
	}
	out := cloneRecords(bucket)
	sort.Slice(out, func(i, j int) bool {
		left, right := publicEndpointPriority(out[i].Endpoint.Source), publicEndpointPriority(out[j].Endpoint.Source)
		if left != right {
			return left > right
		}
		return out[i].Endpoint.Address < out[j].Endpoint.Address
	})
	return out
}

func (r *Registry) InvalidateSession(deviceID, sessionID string) {
	deviceID, sessionID = strings.TrimSpace(deviceID), strings.TrimSpace(sessionID)
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.records[deviceID]
	for _, record := range bucket {
		if record.SessionID == sessionID {
			delete(r.records, deviceID)
			return
		}
	}
}

func (r *Registry) InvalidateDevice(deviceID string) {
	r.mu.Lock()
	delete(r.records, strings.TrimSpace(deviceID))
	r.mu.Unlock()
}

func (r *Registry) update(deviceID, sessionID, address string, fn func(*EndpointRecord, time.Time)) bool {
	deviceID, sessionID, address = strings.TrimSpace(deviceID), strings.TrimSpace(sessionID), strings.TrimSpace(address)
	r.mu.Lock()
	defer r.mu.Unlock()
	bucket := r.records[deviceID]
	record, ok := bucket[address]
	if !ok || record.SessionID != sessionID {
		return false
	}
	fn(&record, r.now())
	bucket[address] = record
	return true
}

func (r *Registry) expireLocked(record EndpointRecord) EndpointRecord {
	if record.State == StateVerified && !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(r.now()) {
		record.State = StateExpired
		record.Endpoint.Verified = false
		record.Endpoint.DialAddress = ""
	}
	return record
}

func cloneRecords(bucket map[string]EndpointRecord) []EndpointRecord {
	out := make([]EndpointRecord, 0, len(bucket))
	for _, record := range bucket {
		out = append(out, record)
	}
	return out
}

func normalizeFingerprint(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "sha256:") {
		return "", errors.New("public direct certificate fingerprint must use sha256")
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if len(raw) != 64 {
		return "", errors.New("public direct certificate fingerprint has invalid length")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return "", errors.New("public direct certificate fingerprint is invalid")
	}
	return value, nil
}

func normalizeCandidate(candidate protocol.PublicDirectEndpointCandidate) (protocol.PublicDirectEndpointCandidate, error) {
	candidate.Protocol = strings.ToLower(strings.TrimSpace(candidate.Protocol))
	candidate.Source = strings.ToLower(strings.TrimSpace(candidate.Source))
	candidate.Address = strings.TrimSpace(candidate.Address)
	if candidate.Protocol != protocol.PublicDirectEndpointProtocolUDP {
		return candidate, errors.New("public direct V1 supports only UDP/QUIC endpoints")
	}
	if candidate.Source != protocol.PublicDirectEndpointIPv6 && candidate.Source != protocol.PublicDirectEndpointManual {
		return candidate, errors.New("agent may register only ipv6 or manual public direct candidates")
	}
	host, portText, err := net.SplitHostPort(candidate.Address)
	if err != nil {
		return candidate, fmt.Errorf("invalid public direct endpoint %q", candidate.Address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return candidate, fmt.Errorf("invalid public direct endpoint port %q", portText)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return candidate, errors.New("public direct endpoint host is empty")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isPublicIP(ip) {
			return candidate, fmt.Errorf("public direct endpoint %q is not globally routable", host)
		}
		if candidate.Source == protocol.PublicDirectEndpointIPv6 && !ip.Is6() {
			return candidate, errors.New("ipv6 public direct candidate is not IPv6")
		}
		host = ip.String()
	} else {
		if candidate.Source == protocol.PublicDirectEndpointIPv6 {
			return candidate, errors.New("ipv6 public direct candidate must use an IP literal")
		}
		if strings.EqualFold(host, "localhost") || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
			return candidate, errors.New("manual public direct hostname is invalid")
		}
	}
	candidate.Address = net.JoinHostPort(host, strconv.Itoa(port))
	return candidate, nil
}

func isPublicIP(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
		!ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func publicEndpointPriority(source string) int {
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

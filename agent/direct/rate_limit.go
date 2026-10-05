package direct

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthAttemptsPerMinute = 120
	maxRateLimitEntries          = 4096
)

type rateWindow struct {
	start time.Time
	count int
}

type handshakeLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]rateWindow
}

func newHandshakeLimiter(limit int) *handshakeLimiter {
	if limit <= 0 {
		limit = defaultAuthAttemptsPerMinute
	}
	return &handshakeLimiter{
		limit: limit, window: time.Minute,
		entries: make(map[string]rateWindow),
	}
}

func (l *handshakeLimiter) Allow(address net.Addr, now time.Time) bool {
	if l == nil || l.limit <= 0 {
		return false
	}
	key := directRemoteKey(address)
	if key == "" {
		return false
	}
	now = now.UTC()
	l.mu.Lock()
	defer l.mu.Unlock()

	for item, state := range l.entries {
		if now.Sub(state.start) >= l.window {
			delete(l.entries, item)
		}
	}
	state, exists := l.entries[key]
	if !exists {
		if len(l.entries) >= maxRateLimitEntries {
			return false
		}
		l.entries[key] = rateWindow{start: now, count: 1}
		return true
	}
	if now.Sub(state.start) >= l.window {
		l.entries[key] = rateWindow{start: now, count: 1}
		return true
	}
	if state.count >= l.limit {
		return false
	}
	state.count++
	l.entries[key] = state
	return true
}

func directRemoteKey(address net.Addr) string {
	if address == nil {
		return ""
	}
	switch value := address.(type) {
	case *net.UDPAddr:
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			return ip.Unmap().String()
		}
	case *net.TCPAddr:
		if ip, ok := netip.AddrFromSlice(value.IP); ok {
			return ip.Unmap().String()
		}
	}
	raw := strings.TrimSpace(address.String())
	host, _, err := net.SplitHostPort(raw)
	if err == nil {
		if ip, err := netip.ParseAddr(host); err == nil {
			return ip.Unmap().String()
		}
		return strings.ToLower(host)
	}
	return strings.ToLower(raw)
}

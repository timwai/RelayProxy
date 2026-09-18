package exit

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDNSCacheTTL     = 30 * time.Second
	defaultDNSCacheEntries = 2048
	happyEyeballsDelay     = 250 * time.Millisecond
)

type dnsCacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
}

type dnsLookup struct {
	done chan struct{}
	ips  []net.IP
	err  error
}

type dnsCache struct {
	mu         sync.RWMutex
	entries    map[string]dnsCacheEntry
	inflight   map[string]*dnsLookup
	lookupIP   func(context.Context, string, string) ([]net.IP, error)
	ttl        time.Duration
	maxEntries int
}

func newDNSCache(ttl time.Duration, maxEntries int) *dnsCache {
	if ttl <= 0 {
		ttl = defaultDNSCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultDNSCacheEntries
	}
	return &dnsCache{
		entries:    make(map[string]dnsCacheEntry),
		inflight:   make(map[string]*dnsLookup),
		lookupIP:   net.DefaultResolver.LookupIP,
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func cloneIPs(src []net.IP) []net.IP {
	dst := make([]net.IP, len(src))
	for i, ip := range src {
		dst[i] = append(net.IP(nil), ip...)
	}
	return dst
}

func dnsCacheKey(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func (c *dnsCache) lookup(ctx context.Context, host string) ([]net.IP, error) {
	key := dnsCacheKey(host)
	now := time.Now()

	c.mu.RLock()
	entry, ok := c.entries[key]
	flight := c.inflight[key]
	c.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		// Cache entries are immutable after publication. Callers in this package
		// treat the returned addresses as read-only, so a cache hit allocates
		// neither a slice nor per-IP backing bytes.
		return entry.ips, nil
	}
	if flight != nil {
		select {
		case <-flight.done:
			return flight.ips, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// Register one lookup per normalized hostname. Browser connection bursts
	// often create many simultaneous streams to the same host; coalescing a
	// cache miss prevents all of them from entering the resolver concurrently.
	c.mu.Lock()
	now = time.Now()
	if entry, ok = c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.ips, nil
	}
	if flight = c.inflight[key]; flight != nil {
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.ips, flight.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight = &dnsLookup{done: make(chan struct{})}
	c.inflight[key] = flight
	c.mu.Unlock()

	ips, err := c.lookupIP(ctx, "ip", host)
	if err == nil && len(ips) > 0 {
		ips = cloneIPs(ips)
	}

	c.mu.Lock()
	if err == nil && len(ips) > 0 {
		now = time.Now()
		if len(c.entries) >= c.maxEntries {
			for k, candidate := range c.entries {
				if !now.Before(candidate.expiresAt) {
					delete(c.entries, k)
				}
			}
			if len(c.entries) >= c.maxEntries {
				for k := range c.entries {
					delete(c.entries, k)
					break
				}
			}
		}
		c.entries[key] = dnsCacheEntry{ips: ips, expiresAt: now.Add(c.ttl)}
	}
	flight.ips, flight.err = ips, err
	delete(c.inflight, key)
	close(flight.done)
	c.mu.Unlock()

	return ips, err
}

func interleaveIPFamilies(ips []net.IP) []net.IP {
	if len(ips) < 2 {
		return ips
	}
	ordered := make([]net.IP, 0, len(ips))
	preferV6 := ips[0].To4() == nil
	v4Index, v6Index := 0, 0

	nextFamily := func(wantV4 bool, index *int) net.IP {
		for *index < len(ips) {
			ip := ips[*index]
			*index = *index + 1
			if (ip.To4() != nil) == wantV4 {
				return ip
			}
		}
		return nil
	}

	for len(ordered) < len(ips) {
		if preferV6 {
			if ip := nextFamily(false, &v6Index); ip != nil {
				ordered = append(ordered, ip)
			}
			if ip := nextFamily(true, &v4Index); ip != nil {
				ordered = append(ordered, ip)
			}
		} else {
			if ip := nextFamily(true, &v4Index); ip != nil {
				ordered = append(ordered, ip)
			}
			if ip := nextFamily(false, &v6Index); ip != nil {
				ordered = append(ordered, ip)
			}
		}
	}
	return ordered
}

// dialTCPIPs races already-resolved and ACL-approved IPs. It never resolves a
// hostname internally, so the socket cannot escape the checked destination set.
func dialTCPIPs(ctx context.Context, ips []net.IP, port uint16) (net.Conn, error) {
	ordered := interleaveIPFamilies(ips)
	if len(ordered) == 0 {
		return nil, errors.New("no IP addresses to dial")
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	winner := make(chan net.Conn, 1)
	errs := make(chan error, len(ordered))
	done := make(chan struct{})
	var wg sync.WaitGroup
	var won atomic.Bool

	for i, ip := range ordered {
		wg.Add(1)
		go func(index int, candidate net.IP) {
			defer wg.Done()
			if index > 0 {
				timer := time.NewTimer(time.Duration(index) * happyEyeballsDelay)
				select {
				case <-raceCtx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-timer.C:
				}
			}

			dialer := net.Dialer{KeepAlive: 30 * time.Second}
			addr := net.JoinHostPort(candidate.String(), strconv.Itoa(int(port)))
			conn, err := dialer.DialContext(raceCtx, "tcp", addr)
			if err != nil {
				errs <- err
				return
			}
			if won.CompareAndSwap(false, true) {
				winner <- conn
				cancel()
				return
			}
			_ = conn.Close()
		}(i, append(net.IP(nil), ip...))
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case conn := <-winner:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		var collected []error
		for {
			select {
			case err := <-errs:
				if err != nil {
					collected = append(collected, err)
				}
			default:
				if len(collected) == 0 {
					return nil, errors.New("all dial attempts failed")
				}
				return nil, errors.Join(collected...)
			}
		}
	}
}

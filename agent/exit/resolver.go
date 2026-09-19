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
// Candidates are started incrementally instead of allocating one goroutine and
// timer per DNS answer up front.
func dialTCPIPs(ctx context.Context, ips []net.IP, port uint16) (net.Conn, error) {
	ordered := interleaveIPFamilies(ips)
	if len(ordered) == 0 {
		return nil, errors.New("no IP addresses to dial")
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type dialResult struct {
		conn net.Conn
		err  error
	}
	results := make(chan dialResult)
	var won atomic.Bool

	startDial := func(ip net.IP) {
		candidate := append(net.IP(nil), ip...)
		go func() {
			dialer := net.Dialer{KeepAlive: 30 * time.Second}
			addr := net.JoinHostPort(candidate.String(), strconv.Itoa(int(port)))
			conn, err := dialer.DialContext(raceCtx, "tcp", addr)
			if err != nil {
				select {
				case results <- dialResult{err: err}:
				case <-raceCtx.Done():
				}
				return
			}
			if raceCtx.Err() != nil || !won.CompareAndSwap(false, true) {
				_ = conn.Close()
				select {
				case results <- dialResult{err: context.Canceled}:
				case <-raceCtx.Done():
				}
				return
			}
			select {
			case results <- dialResult{conn: conn}:
			case <-raceCtx.Done():
				_ = conn.Close()
			}
		}()
	}

	next := 0
	active := 0
	startNext := func() {
		startDial(ordered[next])
		next++
		active++
	}
	startNext()

	timer := time.NewTimer(happyEyeballsDelay)
	defer timer.Stop()
	timerArmed := next < len(ordered)
	if !timerArmed {
		if !timer.Stop() {
			<-timer.C
		}
	}

	var collected []error
	for active > 0 {
		var timerC <-chan time.Time
		if timerArmed {
			timerC = timer.C
		}
		select {
		case <-ctx.Done():
			won.Store(true)
			cancel()
			return nil, ctx.Err()
		case result := <-results:
			active--
			if result.conn != nil {
				cancel()
				return result.conn, nil
			}
			if result.err != nil && !errors.Is(result.err, context.Canceled) {
				collected = append(collected, result.err)
			}
			if active == 0 && next < len(ordered) {
				if timerArmed && !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				startNext()
				if next < len(ordered) {
					timer.Reset(happyEyeballsDelay)
					timerArmed = true
				} else {
					timerArmed = false
				}
			}
		case <-timerC:
			timerArmed = false
			if next < len(ordered) {
				startNext()
				if next < len(ordered) {
					timer.Reset(happyEyeballsDelay)
					timerArmed = true
				}
			}
		}
	}

	if len(collected) == 0 {
		return nil, errors.New("all dial attempts failed")
	}
	return nil, errors.Join(collected...)
}

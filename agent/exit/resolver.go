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
	defaultDNSCacheTTL      = 30 * time.Second
	negativeDNSCacheTTL     = 5 * time.Second
	defaultDNSCacheEntries  = 2048
	defaultDNSLookupTimeout = 15 * time.Second
	happyEyeballsDelay      = 250 * time.Millisecond
)

type dnsCacheEntry struct {
	ips       []net.IP
	err       error
	expiresAt time.Time
}

type dnsLookup struct {
	done    chan struct{}
	ips     []net.IP
	err     error
	cancel  context.CancelFunc
	waiters int
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
	c.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		// Cache entries are immutable after publication. Callers in this package
		// treat the returned addresses as read-only, so a cache hit allocates
		// neither a slice nor per-IP backing bytes.
		return entry.ips, entry.err
	}

	// Register one lookup per normalized hostname. Browser connection bursts
	// often create many simultaneous streams to the same host; coalescing a
	// cache miss prevents all of them from entering the resolver concurrently.
	c.mu.Lock()
	now = time.Now()
	if entry, ok = c.entries[key]; ok && now.Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.ips, entry.err
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	flight := c.inflight[key]
	if flight == nil {
		// A shared lookup belongs to its waiters, rather than the first caller.
		// Keep request values but give the resolver a bounded independent lifetime.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultDNSLookupTimeout)
		flight = &dnsLookup{done: make(chan struct{}), cancel: cancel}
		c.inflight[key] = flight
		go c.resolve(lookupCtx, key, host, flight)
	}
	flight.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 && c.inflight[key] == flight {
			delete(c.inflight, key)
			flight.cancel()
		}
		c.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.ips, flight.err
	}
}

func (c *dnsCache) resolve(ctx context.Context, key, host string, flight *dnsLookup) {
	defer flight.cancel()
	ips, err := c.lookupIP(ctx, "ip", host)
	if err == nil && len(ips) > 0 {
		ips = cloneIPs(ips)
	}
	ttl := c.ttl
	cacheable := err == nil && len(ips) > 0
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound && !dnsErr.IsTimeout && !dnsErr.IsTemporary {
		// Briefly share NXDOMAIN results across browser retries. Transient
		// failures and cancellation must remain immediately retryable.
		cacheable = true
		ttl = min(ttl, negativeDNSCacheTTL)
	}

	c.mu.Lock()
	if c.inflight[key] == flight && cacheable {
		now := time.Now()
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
		c.entries[key] = dnsCacheEntry{ips: ips, err: err, expiresAt: now.Add(ttl)}
	}
	flight.ips, flight.err = ips, err
	if c.inflight[key] == flight {
		delete(c.inflight, key)
	}
	close(flight.done)
	c.mu.Unlock()
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
	conn, _, err := dialTCPIPsWith(ctx, ips, func(ctx context.Context, ip net.IP) (net.Conn, error) {
		dialer := net.Dialer{KeepAlive: 30 * time.Second}
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(int(port)))
		return dialer.DialContext(ctx, "tcp", addr)
	})
	return conn, err
}

// dialTCPIPsWith also races proxy handshakes, so an unresponsive candidate
// cannot consume the deadline reserved for the remaining approved addresses.
func dialTCPIPsWith(ctx context.Context, ips []net.IP, dial func(context.Context, net.IP) (net.Conn, error)) (net.Conn, net.IP, error) {
	ordered := interleaveIPFamilies(ips)
	if len(ordered) == 0 {
		return nil, nil, errors.New("no IP addresses to dial")
	}

	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type dialResult struct {
		conn net.Conn
		ip   net.IP
		err  error
	}
	results := make(chan dialResult)
	var won atomic.Bool

	startDial := func(ip net.IP) {
		candidate := append(net.IP(nil), ip...)
		go func() {
			conn, err := dial(raceCtx, candidate)
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
			case results <- dialResult{conn: conn, ip: candidate}:
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
			return nil, nil, ctx.Err()
		case result := <-results:
			active--
			if result.conn != nil {
				cancel()
				return result.conn, result.ip, nil
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
		return nil, nil, errors.New("all dial attempts failed")
	}
	return nil, nil, errors.Join(collected...)
}

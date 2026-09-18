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

type dnsCache struct {
	mu         sync.RWMutex
	entries    map[string]dnsCacheEntry
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
	return &dnsCache{entries: make(map[string]dnsCacheEntry), ttl: ttl, maxEntries: maxEntries}
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
		return cloneIPs(entry.ips), nil
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, nil
	}
	ips = cloneIPs(ips)

	c.mu.Lock()
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
	c.entries[key] = dnsCacheEntry{ips: cloneIPs(ips), expiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return ips, nil
}

func interleaveIPFamilies(ips []net.IP) []net.IP {
	if len(ips) < 2 {
		return cloneIPs(ips)
	}
	v4 := make([]net.IP, 0, len(ips))
	v6 := make([]net.IP, 0, len(ips))
	preferV6 := ips[0].To4() == nil
	for _, ip := range ips {
		if ip.To4() != nil {
			v4 = append(v4, ip)
		} else {
			v6 = append(v6, ip)
		}
	}
	ordered := make([]net.IP, 0, len(ips))
	for len(v4) > 0 || len(v6) > 0 {
		if preferV6 {
			if len(v6) > 0 {
				ordered = append(ordered, v6[0])
				v6 = v6[1:]
			}
			if len(v4) > 0 {
				ordered = append(ordered, v4[0])
				v4 = v4[1:]
			}
		} else {
			if len(v4) > 0 {
				ordered = append(ordered, v4[0])
				v4 = v4[1:]
			}
			if len(v6) > 0 {
				ordered = append(ordered, v6[0])
				v6 = v6[1:]
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

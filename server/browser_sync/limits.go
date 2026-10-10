package browsersync

import (
	"sync"
	"time"
)

// Browser Sync is an optional service exposed through the admin listener.
// Keys contain only device identifiers, rule IDs or direct peer IPs, never site
// origins, Cookie names, credential material or ciphertext.
const maxBrowserLimiterEntries = 8192

type rateBucket struct {
	count   int
	expires time.Time
}

type browserLimiter struct {
	mu      sync.Mutex
	buckets map[string]rateBucket
}

func newBrowserLimiter() *browserLimiter {
	return &browserLimiter{buckets: make(map[string]rateBucket)}
}

func (l *browserLimiter) allow(key string, max int, interval time.Duration) bool {
	return l.allowAt(key, max, interval, time.Now())
}

func (l *browserLimiter) allowAt(key string, max int, interval time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, present := l.buckets[key]
	if !present || !now.Before(b.expires) {
		if !present && len(l.buckets) >= maxBrowserLimiterEntries {
			for k, old := range l.buckets {
				if !now.Before(old.expires) {
					delete(l.buckets, k)
				}
			}
			// An attack with unique device IDs cannot exhaust process memory.
			if len(l.buckets) >= maxBrowserLimiterEntries {
				return false
			}
		}
		b = rateBucket{expires: now.Add(interval)}
	}
	if b.count >= max {
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}

// Two independent windows prevent both long floods and short bursts.
// A sender may naturally issue cursor/list queries before each snapshot.
// Rule-specific budgets prevent a single policy from dominating the relay.
func (h *Handler) permitControl(deviceID string, msg ruleControlFrame) bool {
	if !h.limits.allow("device-minute:"+deviceID, 240, time.Minute) ||
		!h.limits.allow("device-burst:"+deviceID, 30, 10*time.Second) {
		return false
	}
	ruleID := msg.RuleID
	if msg.Type == "SESSION_SNAPSHOT" && msg.Envelope != nil {
		ruleID = msg.Envelope.RuleID
	}
	switch msg.Type {
	case "SESSION_SNAPSHOT", "SYNC_REQUEST":
		return h.limits.allow("transfer-minute:"+deviceID+":"+ruleID, 20, time.Minute) &&
			h.limits.allow("transfer-burst:"+deviceID+":"+ruleID, 5, 10*time.Second)
	case "RULE_OFFER":
		return h.limits.allow("offers:"+deviceID, 12, time.Hour)
	case "LIST_PEERS", "LIST_RULES":
		return h.limits.allow("list:"+deviceID, 60, time.Minute)
	case "DELIVERY_STATUS", "SEQUENCE_CURSOR":
		return h.limits.allow("read:"+deviceID, 120, time.Minute)
	}
	return true
}

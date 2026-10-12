package browsersync

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBrowserLimiterWindowAndExpiry(t *testing.T) {
	l := newBrowserLimiter()
	start := time.Unix(1000, 0)
	for i := 0; i < 3; i++ {
		if !l.allowAt("device", 3, time.Minute, start) {
			t.Fatal("premature limit")
		}
	}
	if l.allowAt("device", 3, time.Minute, start.Add(time.Second)) {
		t.Fatal("limit bypass")
	}
	if !l.allowAt("other", 3, time.Minute, start) {
		t.Fatal("unrelated key limited")
	}
	if !l.allowAt("device", 3, time.Minute, start.Add(time.Minute)) {
		t.Fatal("limit never recovered")
	}
}

func TestBrowserLimiterConcurrentAndBounded(t *testing.T) {
	l := newBrowserLimiter()
	now := time.Unix(1000, 0)
	var wg sync.WaitGroup
	results := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- l.allowAt("one", 10, time.Minute, now) }()
	}
	wg.Wait()
	close(results)
	permitted := 0
	for allowed := range results {
		if allowed {
			permitted++
		}
	}
	if permitted != 10 {
		t.Fatalf("concurrent quota gave %d grants, want 10", permitted)
	}
	for i := 0; i < maxBrowserLimiterEntries-1; i++ {
		if !l.allowAt(fmt.Sprintf("key-%d", i), 1, time.Hour, now) {
			t.Fatal("map full too early")
		}
	}
	if l.allowAt("overflow", 1, time.Hour, now) {
		t.Fatal("unbounded unique keys")
	}
	if !l.allowAt("overflow", 1, time.Hour, now.Add(2*time.Hour)) {
		t.Fatal("expired keys not released")
	}
}

func TestBrowserControlRateLimits(t *testing.T) {
	h := &Handler{limits: newBrowserLimiter()}
	for i := 0; i < 5; i++ {
		if !h.permitControl("browser_1", ruleControlFrame{Type: "SESSION_SNAPSHOT", RuleID: "r1"}) {
			t.Fatal("unexpected transfer limit")
		}
	}
	if h.permitControl("browser_1", ruleControlFrame{Type: "SESSION_SNAPSHOT", RuleID: "r1"}) {
		t.Fatal("transfer burst not limited")
	}
	if !h.permitControl("browser_2", ruleControlFrame{Type: "SESSION_SNAPSHOT", RuleID: "r1"}) {
		t.Fatal("other device affected")
	}
	if !h.permitControl("browser_1", ruleControlFrame{Type: "SESSION_SNAPSHOT", RuleID: "r2"}) {
		t.Fatal("other rule affected")
	}
}

func TestBrowserControlRestartReadBurstDoesNotStarveWrites(t *testing.T) {
	h := &Handler{limits: newBrowserLimiter()}
	// A receiver can restart while a source is re-sending and querying ACKs.
	// A sustained read burst must not consume the separate mutation budget.
	for i := 0; i < 60; i++ {
		if !h.permitControl("browser_restart", ruleControlFrame{Type: "LIST_RULES"}) {
			t.Fatalf("legitimate restart list request %d was throttled", i)
		}
	}
	if h.permitControl("browser_restart", ruleControlFrame{Type: "LIST_RULES"}) {
		t.Fatal("listing's minute cap must still reject the 61st request")
	}
	for i := 0; i < 30; i++ {
		if !h.permitControl("browser_restart", ruleControlFrame{Type: "PING"}) {
			t.Fatalf("mutation/control burst unexpectedly shared read budget at %d", i)
		}
	}
	if h.permitControl("browser_restart", ruleControlFrame{Type: "PING"}) {
		t.Fatal("mutable/general control burst exceeded its 30/10s limit")
	}
	if !h.permitControl("another_browser", ruleControlFrame{Type: "LIST_RULES"}) {
		t.Fatal("unrelated browser throttled")
	}
}

func TestBrowserControlReadBurstRetainsBoundedQuota(t *testing.T) {
	h := &Handler{limits: newBrowserLimiter()}
	for i := 0; i < 64; i++ {
		if !h.permitControl("browser_read", ruleControlFrame{Type: "SEQUENCE_CURSOR"}) {
			t.Fatalf("read burst failed early at %d", i)
		}
	}
	if h.permitControl("browser_read", ruleControlFrame{Type: "SEQUENCE_CURSOR"}) {
		t.Fatal("read-only controls exceeded 64 requests per 10 seconds")
	}
	// The shared minute window still applies across read and write classes,
	// even though they have separate short-burst windows.
	h.limits = newBrowserLimiter()
	for i := 0; i < 240; i++ {
		// Keep this test deterministic: exercise the shared minute bucket
		// directly, without waiting for shorter burst windows to expire.
		if !h.limits.allow("device-minute:browser_total", 240, time.Minute) {
			t.Fatalf("minute limit blocked early at %d", i)
		}
	}
	if h.limits.allow("device-minute:browser_total", 240, time.Minute) {
		t.Fatal("shared per-device minute limit was bypassed")
	}
}

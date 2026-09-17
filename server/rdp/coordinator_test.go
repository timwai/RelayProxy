package rdp

import (
	"testing"
	"time"
)

func TestCoordinatorLeaseAccounting(t *testing.T) {
	c := NewCoordinator(nil, nil, DefaultLease, "")
	c.mu.Lock()
	lease := &Lease{ID: 1, ControllerID: "controller", TargetID: "target"}
	c.leases[lease.ID] = lease
	c.leaseCounts[lease.ControllerID] = 1
	c.leaseCounts[lease.TargetID] = 1
	removed := c.removeLeaseLocked(lease.ID)
	c.mu.Unlock()

	if removed != lease {
		t.Fatal("removeLeaseLocked returned a different lease")
	}
	if len(c.leases) != 0 || len(c.leaseCounts) != 0 {
		t.Fatalf("lease accounting retained state: leases=%d counts=%v", len(c.leases), c.leaseCounts)
	}
}

func TestCoordinatorConnectRateWindow(t *testing.T) {
	c := NewCoordinator(nil, nil, DefaultLease, "")
	started := time.Unix(100, 0)
	c.mu.Lock()
	for i := 0; i < maxConnectsPerMinute; i++ {
		if !c.allowConnectLocked("controller", started.Add(time.Duration(i)*time.Millisecond)) {
			c.mu.Unlock()
			t.Fatalf("request %d was unexpectedly rate limited", i)
		}
	}
	if c.allowConnectLocked("controller", started.Add(time.Second)) {
		c.mu.Unlock()
		t.Fatal("request beyond the rate window limit was accepted")
	}
	if !c.allowConnectLocked("controller", started.Add(connectRateWindow)) {
		c.mu.Unlock()
		t.Fatal("new rate window was not accepted")
	}
	c.mu.Unlock()
}

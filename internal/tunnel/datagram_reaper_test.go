package tunnel

import (
	"net"
	"testing"
	"time"
)

func TestDatagramReaperResumesAfterAllAssociationsClose(t *testing.T) {
	budget := &datagramBudget{associationLimit: 8, queueLimit: 1 << 20, reassemblyLimit: 1 << 20}
	m := budgetMux(t, budget)
	// Run the production shared reaper without the legacy per-connection watch.
	m.session = &QUICSession{}
	m.reapWake = make(chan struct{}, 1)
	m.wg.Add(1)
	go m.reapLoop()
	t.Cleanup(func() { m.close(); m.wg.Wait() })
	for id := uint64(1); id <= 2; id++ {
		channel := budgetChannel(t, m, id)
		left, right := net.Pipe()
		t.Cleanup(func() { _ = right.Close() })
		conn := NewUDPDatagramConnWithIdleTimeout(channel, budgetStream{left}, &net.UDPAddr{}, 30*time.Millisecond)
		payload := make([]byte, 3000)
		frames := budgetFrames(t, uint32(id), payload)
		if _, complete := conn.assemble(budgetFragment(t, frames[0]), payload); complete {
			t.Fatal("incomplete fragment unexpectedly completed")
		}
		select {
		case <-conn.done:
		case <-time.After(time.Second):
			t.Fatal("shared reaper did not wake for the new association")
		}
		// Close synchronizes cleanup even if done was closed just before the
		// reaper completed releasing reassembly and association reservations.
		_ = conn.Close()
		if usage := budget.usage(); usage.Associations != 0 || usage.ReassemblyBytes != 0 {
			t.Fatalf("reaper retained expired association resources: %+v", usage)
		}
		if id == 1 {
			// Let the last scheduled tick observe the empty association map.
			time.Sleep(150 * time.Millisecond)
		}
	}
}

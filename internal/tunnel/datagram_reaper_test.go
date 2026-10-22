package tunnel

import (
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func sharedReaperMux(t *testing.T) (*datagramMux, *datagramBudget) {
	t.Helper()
	budget := &datagramBudget{associationLimit: 8, queueLimit: 1 << 20, reassemblyLimit: 1 << 20}
	m := budgetMux(t, budget)
	// Run the production shared reaper without the legacy per-connection watch.
	m.session = &QUICSession{}
	m.reapWake = make(chan struct{}, 1)
	m.wg.Add(1)
	go m.reapLoop()
	t.Cleanup(func() { m.close(); m.wg.Wait() })
	return m, budget
}

func sharedReaperConn(t *testing.T, m *datagramMux, id uint64, idle time.Duration) *UDPDatagramConn {
	t.Helper()
	channel := budgetChannel(t, m, id)
	left, right := net.Pipe()
	conn := NewUDPDatagramConnWithIdleTimeout(channel, budgetStream{left}, &net.UDPAddr{}, idle)
	t.Cleanup(func() { _ = conn.Close(); _ = right.Close() })
	return conn
}

func TestDatagramReaperResumesAfterAllAssociationsClose(t *testing.T) {
	m, budget := sharedReaperMux(t)
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

func TestLifetimeDatagramReaperWakesOnlyForPendingFragments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, budget := sharedReaperMux(t)
		conn := sharedReaperConn(t, m, 1, 0)
		payload := make([]byte, 3000)
		for id := uint32(1); id <= 2; id++ {
			time.Sleep(time.Hour)
			if next := conn.reap(time.Now()); !next.IsZero() {
				t.Fatalf("quiet lifetime association requires a timer at %v", next)
			}
			frames := budgetFrames(t, id, payload)
			conn.assemble(budgetFragment(t, frames[0]), payload)
			time.Sleep(reassemblyTimeout - time.Millisecond)
			synctest.Wait()
			if budget.usage().ReassemblyBytes == 0 {
				t.Fatal("fragment was released before its deadline")
			}
			time.Sleep(2 * time.Millisecond)
			synctest.Wait()
			if usage := budget.usage(); usage.ReassemblyBytes != 0 || usage.Associations != 1 {
				t.Fatalf("fragment expiry damaged lifetime association: %+v", usage)
			}
			if next := conn.reap(time.Now()); !next.IsZero() {
				t.Fatal("reaper retained a timer after the last fragment expired")
			}
		}
	})
}

func TestDatagramReaperInterruptsDistantIdleDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, budget := sharedReaperMux(t)
		long := sharedReaperConn(t, m, 1, time.Hour)
		time.Sleep(time.Second)
		payload := make([]byte, 3000)
		frames := budgetFrames(t, 1, payload)
		long.assemble(budgetFragment(t, frames[0]), payload)
		short := sharedReaperConn(t, m, 2, time.Second)
		time.Sleep(reassemblyTimeout + time.Millisecond)
		synctest.Wait()
		if budget.usage().ReassemblyBytes != 0 {
			t.Fatal("pending fragments waited for the hour-long idle deadline")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-short.done:
		default:
			t.Fatal("new short idle timeout waited for the existing long deadline")
		}
		select {
		case <-long.done:
			t.Fatal("short timeout also closed the long-lived association")
		default:
		}
	})
}

func TestDatagramIdleDeadlineTracksReadAndWriteActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, _ := sharedReaperMux(t)
		conn := sharedReaperConn(t, m, 1, time.Second)
		time.Sleep(900 * time.Millisecond)
		if _, err := conn.WriteTo([]byte("write"), nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		frame := budgetFrames(t, 1, []byte("read"))[0]
		m.deliver(budgetEnvelope(1, frame))
		if _, _, err := conn.ReadFrom(make([]byte, 16)); err != nil {
			t.Fatalf("write activity did not extend the first deadline: %v", err)
		}
		time.Sleep(999 * time.Millisecond)
		synctest.Wait()
		select {
		case <-conn.done:
			t.Fatal("read activity did not extend the idle deadline")
		default:
		}
		time.Sleep(2 * time.Millisecond)
		synctest.Wait()
		select {
		case <-conn.done:
		default:
			t.Fatal("inactivity did not close the association at its deadline")
		}
		conn.touch()
		if conn.lastActivityNanos.Load() != -1 {
			t.Fatal("late activity revived an expired association")
		}
	})
}

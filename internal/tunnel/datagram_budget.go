package tunnel

import "sync/atomic"

const (
	maxProcessDatagramAssociations = 4096
	maxProcessDatagramQueueBytes   = 64 << 20
	maxProcessReassemblyBytes      = 64 << 20
)

// Queue and reassembly reservations share process-wide limits across both
// sides of every association. The separate byte limits total 128 MiB.
var processDatagramBudget = &datagramBudget{
	associationLimit: maxProcessDatagramAssociations,
	queueLimit:       maxProcessDatagramQueueBytes,
	reassemblyLimit:  maxProcessReassemblyBytes,
}

type datagramBudget struct {
	associationLimit, queueLimit, reassemblyLimit   int64
	associations, queueBytes, reassemblyBytes       atomic.Int64
	associationRejects, queueDrops, reassemblyDrops atomic.Uint64
}

// DatagramUsage reports retained native UDP resources and overload drops.
// A frame transferred to the QUIC transport or to its caller is no longer
// queued here; QUIC and operating-system transport buffers are separate.
type DatagramUsage struct {
	Associations       int64
	QueueBytes         int64
	ReassemblyBytes    int64
	AssociationRejects uint64
	QueueDrops         uint64
	ReassemblyDrops    uint64
}

func NativeUDPUsage() DatagramUsage { return processDatagramBudget.usage() }

func (b *datagramBudget) usage() DatagramUsage {
	return DatagramUsage{
		Associations: b.associations.Load(), QueueBytes: b.queueBytes.Load(), ReassemblyBytes: b.reassemblyBytes.Load(),
		AssociationRejects: b.associationRejects.Load(), QueueDrops: b.queueDrops.Load(), ReassemblyDrops: b.reassemblyDrops.Load(),
	}
}

func reserveDatagramBytes(counter *atomic.Int64, n, limit int64) bool {
	for {
		old := counter.Load()
		if n < 0 || n > limit-old {
			return false
		}
		if counter.CompareAndSwap(old, old+n) {
			return true
		}
	}
}

func (b *datagramBudget) reserveAssociation() bool {
	if reserveDatagramBytes(&b.associations, 1, b.associationLimit) {
		return true
	}
	b.associationRejects.Add(1)
	return false
}

func (b *datagramBudget) reserveQueue(n int) bool {
	if reserveDatagramBytes(&b.queueBytes, int64(n), b.queueLimit) {
		return true
	}
	b.queueDrops.Add(1)
	return false
}

func (b *datagramBudget) releaseQueue(n int) { b.queueBytes.Add(-int64(n)) }

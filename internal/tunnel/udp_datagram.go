package tunnel

import (
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
)

// Incomplete fragments are stale quickly for interactive RDP. Retaining a
// large pending set lets one burst of losses block all subsequent frames.
const reassemblyTimeout = 200 * time.Millisecond
const maxPendingUDPPackets = 32

type pendingUDP struct {
	total    uint16
	count    uint8
	data     []byte
	seen     uint64
	received int
	bytes    int
	created  time.Time
}

// UDPDatagramConn carries unreliable datagrams while retaining a reliable
// stream solely as the authenticated association's lifetime signal.
type UDPDatagramConn struct {
	channel                     *DatagramChannel
	stream                      TunnelStream
	remote                      net.Addr
	idleTimeout                 time.Duration
	done                        chan struct{}
	once                        sync.Once
	mu                          sync.Mutex
	readDeadline, writeDeadline time.Time
	lastActivityNanos           atomic.Int64
	deadlineChanged             chan struct{}
	readMu, writeMu             sync.Mutex
	readTimer                   *time.Timer
	assemblyMu                  sync.Mutex
	pending                     map[uint32]*pendingUDP
	packetID                    atomic.Uint32
}

func NewUDPDatagramConn(channel *DatagramChannel, stream TunnelStream, remote net.Addr) *UDPDatagramConn {
	return NewUDPDatagramConnWithIdleTimeout(channel, stream, remote, UDPIdleTimeout)
}

// NewUDPDatagramConnWithIdleTimeout creates a native datagram association.
// A non-positive timeout keeps the association alive until its authenticated
// lifetime stream closes. Long-lived protocols such as RDP use that mode so a
// quiet desktop doesn't pay a new association handshake on its next packet.
func NewUDPDatagramConnWithIdleTimeout(channel *DatagramChannel, stream TunnelStream, remote net.Addr, idleTimeout time.Duration) *UDPDatagramConn {
	c := &UDPDatagramConn{channel: channel, stream: stream, remote: remote, idleTimeout: idleTimeout, done: make(chan struct{}), deadlineChanged: make(chan struct{}), pending: make(map[uint32]*pendingUDP)}
	c.lastActivityNanos.Store(time.Now().UnixNano())
	if !channel.setCleanup(c.clearPending) {
		_ = c.Close()
		return c
	}
	if !channel.setReaper(c) {
		_ = c.Close()
		return c
	}
	go func() { var b [1]byte; _, _ = stream.Read(b[:]); _ = c.Close() }()
	// Hand-built muxes used by focused unit tests do not run the session-wide
	// reaper. Keep their expiry semantics without adding a ticker to production
	// associations, which are reaped by datagramMux.reapLoop.
	if channel.mux.session == nil {
		go c.legacyWatch()
	}
	return c
}

func (c *UDPDatagramConn) legacyWatch() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-c.channel.done:
			_ = c.Close()
			return
		case <-c.channel.mux.ctx.Done():
			_ = c.Close()
			return
		case now := <-ticker.C:
			c.reap(now)
			select {
			case <-c.done:
				return
			default:
			}
		}
	}
}

func (c *UDPDatagramConn) reap(now time.Time) {
	select {
	case <-c.done:
		return
	case <-c.channel.done:
		_ = c.Close()
		return
	case <-c.channel.mux.ctx.Done():
		_ = c.Close()
		return
	default:
	}
	c.assemblyMu.Lock()
	for id, p := range c.pending {
		if now.Sub(p.created) >= reassemblyTimeout {
			c.channel.mux.budget.reassemblyDrops.Add(1)
			c.dropLocked(id, p)
		}
	}
	c.assemblyMu.Unlock()
	if c.idleTimeout > 0 && now.Sub(time.Unix(0, c.lastActivityNanos.Load())) >= c.idleTimeout {
		_ = c.Close()
	}
}

func (c *UDPDatagramConn) touch() { c.lastActivityNanos.Store(time.Now().UnixNano()) }
func (c *UDPDatagramConn) dropLocked(id uint32, p *pendingUDP) {
	delete(c.pending, id)
	c.channel.mux.releaseReassembly(p.bytes)
}

func (c *UDPDatagramConn) clearPending() {
	c.assemblyMu.Lock()
	defer c.assemblyMu.Unlock()
	for id, p := range c.pending {
		c.dropLocked(id, p)
	}
}

func (c *UDPDatagramConn) assemble(frame, dst []byte) (int, bool) {
	f, err := protocol.DecodeUDPFragment(frame)
	if err != nil {
		return 0, false
	}
	c.assemblyMu.Lock()
	defer c.assemblyMu.Unlock()
	select {
	case <-c.done:
		return 0, false
	case <-c.channel.done:
		return 0, false
	default:
	}
	if f.Count == 1 {
		return copy(dst, f.Payload), true
	}
	p := c.pending[f.PacketID]
	if p == nil {
		if len(c.pending) >= maxPendingUDPPackets {
			c.channel.mux.budget.reassemblyDrops.Add(1)
			return 0, false
		}
		// Reserve one contiguous complete-packet buffer before allocation. This
		// avoids a second unbudgeted copy when the final fragment arrives.
		if !c.channel.mux.reserve(int(f.Total)) {
			return 0, false
		}
		p = &pendingUDP{total: f.Total, count: f.Count, data: make([]byte, int(f.Total)), bytes: int(f.Total), created: time.Now()}
		c.pending[f.PacketID] = p
	}
	if p.total != f.Total || p.count != f.Count {
		c.channel.mux.budget.reassemblyDrops.Add(1)
		c.dropLocked(f.PacketID, p)
		return 0, false
	}
	mask := uint64(1) << f.Index
	if p.seen&mask == 0 {
		copy(p.data[int(f.Index)*protocol.UDPFragmentPayload:], f.Payload)
		p.seen |= mask
		p.received++
	}
	if p.received != int(p.count) {
		return 0, false
	}
	n := copy(dst, p.data)
	c.dropLocked(f.PacketID, p)
	return n, true
}

func (c *UDPDatagramConn) resetReadTimer(deadline time.Time) <-chan time.Time {
	if c.readTimer != nil {
		if !c.readTimer.Stop() {
			select {
			case <-c.readTimer.C:
			default:
			}
		}
	}
	if deadline.IsZero() {
		return nil
	}
	d := max(time.Duration(0), time.Until(deadline))
	if c.readTimer == nil {
		c.readTimer = time.NewTimer(d)
	} else {
		c.readTimer.Reset(d)
	}
	return c.readTimer.C
}

func (c *UDPDatagramConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		c.mu.Lock()
		deadline, changed := c.readDeadline, c.deadlineChanged
		c.mu.Unlock()
		select {
		case <-c.done:
			return 0, nil, net.ErrClosed
		default:
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, nil, os.ErrDeadlineExceeded
		}
		frame, available, err := c.channel.takeFrame()
		if err != nil {
			return 0, nil, err
		}
		if available {
			n, complete := c.assemble(frame, p)
			if !complete {
				continue
			}
			c.touch()
			return n, c.remote, nil
		}
		timeout := c.resetReadTimer(deadline)
		select {
		case <-c.done:
			return 0, nil, net.ErrClosed
		case <-c.channel.done:
			return 0, nil, net.ErrClosed
		case <-c.channel.mux.ctx.Done():
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-changed:
			continue
		case <-c.channel.framesReady:
			continue
		}
	}
}

func (c *UDPDatagramConn) send(frame []byte) error {
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	return c.channel.sendFrame(frame)
}

func (c *UDPDatagramConn) sendFragment(packetID uint32, total uint16, index, count uint8, payload []byte) error {
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return os.ErrDeadlineExceeded
	}
	return c.channel.sendFragment(packetID, total, index, count, payload)
}

func (c *UDPDatagramConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if len(p) > protocol.MaxUDPDatagramPayload {
		return 0, protocol.ErrUDPFragment
	}
	packetID := c.packetID.Add(1)
	count := max(1, (len(p)+protocol.UDPFragmentPayload-1)/protocol.UDPFragmentPayload)
	for i := 0; i < count; i++ {
		start := min(i*protocol.UDPFragmentPayload, len(p))
		end := min((i+1)*protocol.UDPFragmentPayload, len(p))
		if err := c.sendFragment(packetID, uint16(len(p)), byte(i), byte(count), p[start:end]); err != nil {
			return 0, err
		}
	}
	c.touch()
	return len(p), nil
}
func (c *UDPDatagramConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		_ = c.channel.Close()
		_ = c.stream.Close()
		c.clearPending()
	})
	return nil
}
func (c *UDPDatagramConn) LocalAddr() net.Addr { return c.channel.mux.session.LocalAddr() }
func (c *UDPDatagramConn) UDPMode() string     { return protocol.UDPModeDatagram }
func (c *UDPDatagramConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	c.readDeadline = t
	c.writeDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}
func (c *UDPDatagramConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	c.readDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}
func (c *UDPDatagramConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	c.writeDeadline = t
	close(c.deadlineChanged)
	c.deadlineChanged = make(chan struct{})
	return nil
}

var _ net.PacketConn = (*UDPDatagramConn)(nil)
var _ io.Closer = (*DatagramChannel)(nil)

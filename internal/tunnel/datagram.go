package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
)

const (
	datagramEnvelopeSize    = 11
	maxDatagramAssociations = 256
	// A window animation can produce dozens of fragments back-to-back. Keep
	// enough recent frames to absorb that burst while the QUIC writer drains its
	// own bounded queue. The process-wide byte budget remains the hard cap.
	datagramQueueSize         = 128
	datagramSendQueueSize     = 256
	maxSessionReassemblyBytes = 8 << 20
	maxPooledDatagramPacket   = 2048
)

var ErrDatagramsUnsupported = errors.New("native UDP datagrams are not supported by this session")
var ErrDatagramLimit = errors.New("UDP association capacity reached")

var datagramPacketPool sync.Pool

func acquireDatagramPacket(n int) []byte {
	if n <= maxPooledDatagramPacket {
		if value := datagramPacketPool.Get(); value != nil {
			if packet, ok := value.([]byte); ok && cap(packet) >= n {
				return packet[:n]
			}
		}
	}
	return make([]byte, n)
}

func releaseDatagramPacket(packet []byte) {
	if cap(packet) == 0 || cap(packet) > maxPooledDatagramPacket {
		return
	}
	datagramPacketPool.Put(packet[:0])
}

// SupportsDatagrams is a negotiated QUIC transport capability. Application
// support is checked separately so old peers always keep stream compatibility.
func SupportsDatagrams(sess TunnelSession) bool {
	s, ok := sess.(*QUICSession)
	if !ok || s == nil || s.conn == nil {
		return false
	}
	state := s.conn.ConnectionState()
	return state.SupportsDatagrams.Local && state.SupportsDatagrams.Remote
}

func SetPeerCapabilities(sess TunnelSession, caps []string) {
	if s, ok := sess.(*QUICSession); ok {
		enabled := false
		for _, cap := range caps {
			if cap == protocol.UDPModeDatagram {
				enabled = true
			}
		}
		s.peerDatagrams.Store(enabled)
	}
}

func PeerSupportsDatagrams(sess TunnelSession) bool {
	s, ok := sess.(*QUICSession)
	return ok && SupportsDatagrams(sess) && s.peerDatagrams.Load()
}

func StreamSession(stream TunnelStream) TunnelSession {
	if s, ok := stream.(*QUICStreamAdapter); ok {
		return s.session
	}
	return nil
}

type queuedDatagram struct {
	channel *DatagramChannel
	packet  []byte
}

type receivedDatagram struct {
	packet   []byte
	frame    []byte
	fragment protocol.UDPFragment
	bytes    int // Includes the retained association envelope's backing bytes.
}

// DatagramPacket is an opaque, validated native datagram owned by the caller.
// Relay forwarding can hand the same backing buffer to another channel, which
// avoids copying the fragment payload before quic-go performs its send copy.
type DatagramPacket struct {
	packet       []byte
	payloadBytes int
}

func (p *DatagramPacket) PayloadBytes() int {
	if p == nil {
		return 0
	}
	return p.payloadBytes
}

type datagramMux struct {
	session         *QUICSession
	ctx             context.Context
	budget          *datagramBudget
	mu              sync.RWMutex
	channels        map[uint64]*DatagramChannel
	closed          bool
	closeOnce       sync.Once
	done            chan struct{}
	send            chan queuedDatagram
	sendReady       chan struct{}
	wg              sync.WaitGroup
	reassemblyBytes atomic.Int64
}

func newDatagramMux(s *QUICSession) *datagramMux {
	m := &datagramMux{session: s, ctx: s.conn.Context(), budget: processDatagramBudget, channels: make(map[uint64]*DatagramChannel), done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize), sendReady: make(chan struct{}, 1)}
	// Drain even unregistered datagrams. A session must not retain an unread
	// native receive queue merely because it has no active associations.
	m.wg.Add(3)
	go m.receiveLoop()
	go m.sendLoop()
	go m.reapLoop()
	return m
}

// DatagramChannel carries bounded fragments for one association on one session
// generation. Relays forward these fragments without reassembly.
type DatagramChannel struct {
	ID        uint64
	mux       *datagramMux
	frames    chan receivedDatagram
	done      chan struct{}
	closeOnce sync.Once
	closed    bool // guarded by mux.mu
	onClose   func()
	reaper    *UDPDatagramConn // guarded by mux.mu
}

func OpenDatagramChannel(sess TunnelSession, id uint64) (*DatagramChannel, error) {
	if !PeerSupportsDatagrams(sess) {
		return nil, ErrDatagramsUnsupported
	}
	return sess.(*QUICSession).datagrams.openChannel(id)
}

func (m *datagramMux) openChannel(id uint64) (*DatagramChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return nil, net.ErrClosed
	}
	if len(m.channels) >= maxDatagramAssociations {
		m.budget.associationRejects.Add(1)
		return nil, ErrDatagramLimit
	}
	if id == 0 {
		for {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				return nil, err
			}
			id = binary.BigEndian.Uint64(b[:])
			if id != 0 && m.channels[id] == nil {
				break
			}
		}
	}
	if m.channels[id] != nil {
		return nil, errors.New("duplicate UDP association ID")
	}
	if !m.budget.reserveAssociation() {
		return nil, ErrDatagramLimit
	}
	c := &DatagramChannel{ID: id, mux: m, frames: make(chan receivedDatagram, datagramQueueSize), done: make(chan struct{})}
	m.channels[id] = c
	return c, nil
}

func (m *datagramMux) receiveLoop() {
	defer m.wg.Done()
	defer m.close()
	for {
		packet, err := m.session.conn.ReceiveDatagram(m.ctx)
		if err != nil {
			return
		}
		m.deliver(packet)
	}
}

func (m *datagramMux) deliver(packet []byte) {
	if len(packet) < datagramEnvelopeSize || packet[0] != 'R' || packet[1] != 'U' || packet[2] != 1 {
		return
	}
	frame := packet[datagramEnvelopeSize:]
	fragment, err := protocol.DecodeUDPFragment(frame)
	if err != nil {
		return
	}
	id := binary.BigEndian.Uint64(packet[3:11])
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.channels[id]
	if m.closed || c == nil || c.closed || !m.budget.reserveQueue(len(packet)) {
		return
	}
	queued := receivedDatagram{packet: packet, frame: frame, fragment: fragment, bytes: len(packet)}
	select {
	case c.frames <- queued:
	default:
		// Prefer the newest RDP frame when a burst outruns the consumer. Keeping
		// old fragments in front of fresh ones creates visible animation latency
		// and can prevent a complete current packet from ever being reassembled.
		select {
		case old := <-c.frames:
			m.budget.releaseQueue(old.bytes)
			m.budget.queueDrops.Add(1)
		default:
		}
		select {
		case c.frames <- queued:
		default:
			m.budget.releaseQueue(len(packet))
			m.budget.queueDrops.Add(1)
		}
	}
}

func (m *datagramMux) sendLoop() {
	defer m.wg.Done()
	defer m.close()
	for {
		if job, ok := m.takeSend(); ok {
			select {
			case <-job.channel.done:
				releaseDatagramPacket(job.packet)
				continue
			default:
			}
			// quic-go may block when its send queue is full. Exactly one worker per
			// session owns that call. Overloaded application queues drop and count
			// frames without blocking writers or changing association mode.
			if err := m.session.conn.SendDatagram(job.packet); err != nil {
				_ = job.channel.Close()
			}
			releaseDatagramPacket(job.packet)
			continue
		}
		select {
		case <-m.ctx.Done():
			return
		case <-m.done:
			return
		case <-m.sendReady:
		}
	}
}

// reapLoop is shared by every native UDP association on a QUIC session. A
// per-association ticker would multiply wakeups and timers under load.
func (m *datagramMux) reapLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	conns := make([]*UDPDatagramConn, 0, maxDatagramAssociations)
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.done:
			return
		case now := <-ticker.C:
			conns = conns[:0]
			m.mu.RLock()
			for _, channel := range m.channels {
				if channel.reaper != nil {
					conns = append(conns, channel.reaper)
				}
			}
			m.mu.RUnlock()
			for _, conn := range conns {
				conn.reap(now)
			}
		}
	}
}

func (m *datagramMux) takeSend() (queuedDatagram, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.ctx.Err() != nil {
		return queuedDatagram{}, false
	}
	select {
	case job := <-m.send:
		// Dequeue and release are one transaction with respect to Close.
		m.budget.releaseQueue(len(job.packet))
		return job, true
	default:
		return queuedDatagram{}, false
	}
}

func (c *DatagramChannel) sendFrame(frame []byte) error {
	if _, err := protocol.DecodeUDPFragment(frame); err != nil {
		return err
	}
	return c.enqueueFrame(frame)
}

func (c *DatagramChannel) sendFragment(packetID uint32, total uint16, index, count uint8, payload []byte) error {
	n := protocol.UDPFragmentHeaderSize + len(payload)
	return c.enqueueFrameParts(n, func(frame []byte) {
		binary.BigEndian.PutUint32(frame[:4], packetID)
		binary.BigEndian.PutUint16(frame[4:6], total)
		frame[6], frame[7] = index, count
		copy(frame[protocol.UDPFragmentHeaderSize:], payload)
	})
}

func (c *DatagramChannel) enqueueFrame(frame []byte) error {
	return c.enqueueFrameParts(len(frame), func(dst []byte) { copy(dst, frame) })
}

func (c *DatagramChannel) enqueueFrameParts(frameSize int, fill func([]byte)) error {
	n := datagramEnvelopeSize + frameSize
	if !c.mux.budget.reserveQueue(n) {
		return nil // UDP overload is loss, not a switch to reliable transport.
	}
	// Packet allocation and payload copy do not need the association map lock.
	// Close is serialized only around the actual queue transaction below.
	b := acquireDatagramPacket(n)
	b[0], b[1], b[2] = 'R', 'U', 1
	binary.BigEndian.PutUint64(b[3:11], c.ID)
	fill(b[datagramEnvelopeSize:])
	return c.queuePreparedPacket(b)
}

func (c *DatagramChannel) queuePreparedPacket(packet []byte) error {
	n := len(packet)
	c.mux.mu.RLock()
	defer c.mux.mu.RUnlock()
	if c.closed || c.mux.closed || c.mux.ctx.Err() != nil {
		c.mux.budget.releaseQueue(n)
		releaseDatagramPacket(packet)
		return net.ErrClosed
	}
	select {
	case c.mux.send <- queuedDatagram{channel: c, packet: packet}:
		select {
		case c.mux.sendReady <- struct{}{}:
		default:
		}
	default:
		// Datagram transport is deliberately lossy. Under animation bursts,
		// discard the oldest queued frame so the freshest screen update gets a
		// chance to reach QUIC instead of waiting behind stale pixels.
		select {
		case old := <-c.mux.send:
			c.mux.budget.releaseQueue(len(old.packet))
			c.mux.budget.queueDrops.Add(1)
			releaseDatagramPacket(old.packet)
		default:
		}
		select {
		case c.mux.send <- queuedDatagram{channel: c, packet: packet}:
			select {
			case c.mux.sendReady <- struct{}{}:
			default:
			}
		default:
			c.mux.budget.releaseQueue(n)
			c.mux.budget.queueDrops.Add(1)
			releaseDatagramPacket(packet)
		}
	}
	return nil
}

func (c *DatagramChannel) Send(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.sendFrame(frame)
}

// Forward queues a fragment that was returned by DatagramChannel.Receive.
// Receive only exposes frames that were already validated by the source mux,
// so relay-to-relay forwarding can skip a redundant DecodeUDPFragment pass.
func (c *DatagramChannel) Forward(ctx context.Context, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.enqueueFrame(frame)
}

func (c *DatagramChannel) receiveDatagram(ctx context.Context) (receivedDatagram, error) {
	// Preserve close-first semantics without taking mux.mu on every packet.
	select {
	case <-ctx.Done():
		return receivedDatagram{}, ctx.Err()
	case <-c.done:
		return receivedDatagram{}, net.ErrClosed
	case <-c.mux.ctx.Done():
		return receivedDatagram{}, net.ErrClosed
	default:
	}

	select {
	case <-ctx.Done():
		return receivedDatagram{}, ctx.Err()
	case <-c.done:
		return receivedDatagram{}, net.ErrClosed
	case <-c.mux.ctx.Done():
		return receivedDatagram{}, net.ErrClosed
	case f := <-c.frames:
		c.mux.budget.releaseQueue(f.bytes)
		select {
		case <-c.done:
			return receivedDatagram{}, net.ErrClosed
		case <-c.mux.ctx.Done():
			return receivedDatagram{}, net.ErrClosed
		default:
			return f, nil
		}
	}
}

func (c *DatagramChannel) Receive(ctx context.Context) ([]byte, error) {
	f, err := c.receiveDatagram(ctx)
	if err != nil {
		return nil, err
	}
	return f.frame, nil
}

// ReceivePacket transfers ownership of one validated native datagram to the
// caller. It is intended for Relay forwarding; endpoints should use Receive.
func (c *DatagramChannel) ReceivePacket(ctx context.Context) (*DatagramPacket, error) {
	f, err := c.receiveDatagram(ctx)
	if err != nil {
		return nil, err
	}
	return &DatagramPacket{packet: f.packet, payloadBytes: len(f.fragment.Payload)}, nil
}

// ForwardPacket consumes packet. The source ReceivePacket buffer is reused;
// only the association envelope changes before quic-go queues its own copy.
func (c *DatagramChannel) ForwardPacket(ctx context.Context, packet *DatagramPacket) error {
	if packet == nil || len(packet.packet) < datagramEnvelopeSize+protocol.UDPFragmentHeaderSize {
		return protocol.ErrUDPFragment
	}
	raw := packet.packet
	packet.packet = nil
	if err := ctx.Err(); err != nil {
		releaseDatagramPacket(raw)
		return err
	}
	if !c.mux.budget.reserveQueue(len(raw)) {
		releaseDatagramPacket(raw)
		return nil
	}
	raw[0], raw[1], raw[2] = 'R', 'U', 1
	binary.BigEndian.PutUint64(raw[3:11], c.ID)
	return c.queuePreparedPacket(raw)
}

func (c *DatagramChannel) takeDatagram() (receivedDatagram, bool, error) {
	select {
	case <-c.done:
		return receivedDatagram{}, false, net.ErrClosed
	case <-c.mux.ctx.Done():
		return receivedDatagram{}, false, net.ErrClosed
	default:
	}

	select {
	case f := <-c.frames:
		c.mux.budget.releaseQueue(f.bytes)
		select {
		case <-c.done:
			return receivedDatagram{}, false, net.ErrClosed
		case <-c.mux.ctx.Done():
			return receivedDatagram{}, false, net.ErrClosed
		default:
			return f, true, nil
		}
	default:
		return receivedDatagram{}, false, nil
	}
}

func (c *DatagramChannel) takeFrame() ([]byte, bool, error) {
	f, ok, err := c.takeDatagram()
	return f.frame, ok, err
}
func (c *DatagramChannel) Close() error {
	c.closeOnce.Do(func() {
		m := c.mux
		m.mu.Lock()
		c.closed = true
		close(c.done)
		c.drainLocked()
		cleanup := c.onClose
		m.mu.Unlock()
		if cleanup != nil {
			cleanup()
		}
		// Keep the channel discoverable until cleanup finishes so a concurrent
		// session close waits for the same complete resource release.
		m.mu.Lock()
		delete(m.channels, c.ID)
		m.budget.associations.Add(-1)
		m.mu.Unlock()
	})
	return nil
}

func (c *DatagramChannel) setCleanup(cleanup func()) bool {
	c.mux.mu.Lock()
	defer c.mux.mu.Unlock()
	if c.closed || c.mux.closed {
		return false
	}
	c.onClose = cleanup
	return true
}

func (c *DatagramChannel) setReaper(reaper *UDPDatagramConn) bool {
	c.mux.mu.Lock()
	defer c.mux.mu.Unlock()
	if c.closed || c.mux.closed {
		return false
	}
	c.reaper = reaper
	return true
}

func (c *DatagramChannel) drainLocked() {
receive:
	for {
		select {
		case f := <-c.frames:
			c.mux.budget.releaseQueue(f.bytes)
		default:
			break receive
		}
	}
	var keep [datagramSendQueueSize]queuedDatagram
	n := 0
send:
	for {
		select {
		case job := <-c.mux.send:
			if job.channel == c {
				c.mux.budget.releaseQueue(len(job.packet))
				releaseDatagramPacket(job.packet)
			} else {
				keep[n] = job
				n++
			}
		default:
			break send
		}
	}
	for _, job := range keep[:n] {
		c.mux.send <- job
	}
}

func (m *datagramMux) close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		close(m.done)
		channels := make([]*DatagramChannel, 0, len(m.channels))
		for _, c := range m.channels {
			channels = append(channels, c)
		}
		m.mu.Unlock()
		for _, c := range channels {
			_ = c.Close()
		}
	})
}

func (m *datagramMux) reserve(n int) bool {
	if !reserveDatagramBytes(&m.reassemblyBytes, int64(n), maxSessionReassemblyBytes) {
		m.budget.reassemblyDrops.Add(1)
		return false
	}
	if !reserveDatagramBytes(&m.budget.reassemblyBytes, int64(n), m.budget.reassemblyLimit) {
		m.reassemblyBytes.Add(-int64(n))
		m.budget.reassemblyDrops.Add(1)
		return false
	}
	return true
}

func (m *datagramMux) releaseReassembly(n int) {
	m.reassemblyBytes.Add(-int64(n))
	m.budget.reassemblyBytes.Add(-int64(n))
}

package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
)

const UDPIdleTimeout = 60 * time.Second

// UDPStreamConn retains datagram boundaries on the legacy reliable transport.
// Deadlines are caller-owned; idle expiry is shared by both directions.
type UDPStreamConn struct {
	stream                      DeadlineStream
	remote                      net.Addr
	readMu, writeMu             sync.Mutex
	mu                          sync.Mutex
	closed                      bool
	readDeadline, writeDeadline time.Time
	activityOrigin              time.Time
	// Elapsed monotonic time since activityOrigin; -1 prevents activity from
	// reviving an expired or explicitly closed stream.
	lastActivityNanos atomic.Int64
	idleTimer         *time.Timer
	header            [4]byte
	headerN, payloadN int
	payload           []byte
	writeBuf          []byte
}

func NewUDPStreamConn(stream DeadlineStream, remote net.Addr) *UDPStreamConn {
	c := &UDPStreamConn{stream: stream, remote: remote, activityOrigin: time.Now()}
	c.idleTimer = time.AfterFunc(UDPIdleTimeout, c.expire)
	c.applyDeadlinesLocked()
	return c
}

func (c *UDPStreamConn) applyDeadlinesLocked() {
	// idleTimer owns inactivity expiry and closes the stream at the exact
	// deadline. Only caller-owned deadlines belong on the hot I/O path; adding
	// the idle deadline here would require refreshing it for every datagram.
	_ = c.stream.SetReadDeadline(c.readDeadline)
	_ = c.stream.SetWriteDeadline(c.writeDeadline)
}

func (c *UDPStreamConn) expire() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	for {
		lastActivity := c.lastActivityNanos.Load()
		remaining := UDPIdleTimeout - (time.Since(c.activityOrigin) - time.Duration(lastActivity))
		if remaining > 0 {
			c.idleTimer.Reset(remaining)
			c.mu.Unlock()
			return
		}
		// Close only if no packet has refreshed activity since the check.
		// Unlike two loads, CAS also covers a refresh just before closing.
		if c.lastActivityNanos.CompareAndSwap(lastActivity, -1) {
			break
		}
	}
	c.closed = true
	_ = c.stream.SetDeadline(time.Now())
	c.mu.Unlock()
	_ = c.stream.Close()
}

func (c *UDPStreamConn) touch() {
	// Do not reset the timer on every packet. When it fires, expire checks this
	// timestamp and reschedules itself for the remaining idle interval. This
	// keeps the packet hot path free of mutexes and runtime timer-heap updates.
	now := time.Since(c.activityOrigin).Nanoseconds()
	for {
		previous := c.lastActivityNanos.Load()
		if previous < 0 || now <= previous {
			return
		}
		if c.lastActivityNanos.CompareAndSwap(previous, now) {
			return
		}
	}
}

func (c *UDPStreamConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, nil, net.ErrClosed
	}
	// Retain partial framing across caller timeouts so the next read cannot
	// mistake a payload suffix for a new length header.
	if c.headerN < 4 {
		n, err := io.ReadFull(c.stream, c.header[c.headerN:])
		c.headerN += n
		if err != nil {
			return 0, nil, err
		}
		length := binary.BigEndian.Uint32(c.header[:])
		if length > protocol.MaxUDPDatagramPayload {
			_ = c.Close()
			return 0, nil, fmt.Errorf("UDP payload exceeds limit")
		}
		if cap(c.payload) < int(length) {
			c.payload = make([]byte, int(length))
		} else {
			c.payload = c.payload[:int(length)]
		}
	}
	n, err := io.ReadFull(c.stream, c.payload[c.payloadN:])
	c.payloadN += n
	if err != nil {
		return 0, nil, err
	}
	n = copy(p, c.payload)
	c.headerN, c.payloadN = 0, 0
	c.payload = c.payload[:0]
	c.touch()
	return n, c.remote, nil
}

func (c *UDPStreamConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	if len(p) > protocol.MaxUDPDatagramPayload {
		return 0, fmt.Errorf("UDP payload exceeds limit")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if cap(c.writeBuf) < 4+len(p) {
		c.writeBuf = make([]byte, 4+len(p))
	} else {
		c.writeBuf = c.writeBuf[:4+len(p)]
	}
	frame := c.writeBuf
	binary.BigEndian.PutUint32(frame[:4], uint32(len(p)))
	copy(frame[4:], p)
	n, err := c.stream.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err != nil {
		// A partial reliable frame cannot be retried as a fresh datagram.
		if n > 0 {
			_ = c.Close()
		}
		return 0, err
	}
	c.touch()
	return len(p), nil
}

func (c *UDPStreamConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.lastActivityNanos.Store(-1)
	c.idleTimer.Stop()
	_ = c.stream.SetDeadline(time.Now())
	c.mu.Unlock()
	return c.stream.Close()
}
func (c *UDPStreamConn) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4zero} }
func (c *UDPStreamConn) UDPMode() string     { return protocol.UDPModeStream }
func (c *UDPStreamConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.readDeadline = t
	c.writeDeadline = t
	c.applyDeadlinesLocked()
	return nil
}
func (c *UDPStreamConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.readDeadline = t
	c.applyDeadlinesLocked()
	return nil
}
func (c *UDPStreamConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.writeDeadline = t
	c.applyDeadlinesLocked()
	return nil
}

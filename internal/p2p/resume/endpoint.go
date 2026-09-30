package resume

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Transport is one replaceable ordered byte stream carrying resumable Frames.
// tunnel.TunnelStream and net.Conn both satisfy this interface.
type Transport interface {
	io.Reader
	io.Writer
	io.Closer
}

type TransportLoss struct {
	Generation uint64
	Err        error
}

// Endpoint exposes one continuous application byte stream while its framed
// transport may be replaced with a newer generation.
type Endpoint struct {
	state *StreamState

	inbound *streamBuffer

	appWriteMu sync.Mutex
	writeMu    sync.Mutex

	mu         sync.Mutex
	transport  Transport
	generation uint64
	ready      bool
	closed     bool
	change     chan struct{}
	genDone    map[uint64]chan struct{}
	genClosed  map[uint64]bool

	losses   chan TransportLoss
	progress chan struct{}
}

func NewEndpoint(state *StreamState) (*Endpoint, error) {
	if state == nil || !state.Identity().Valid() {
		return nil, ErrBinding
	}
	return &Endpoint{
		state:     state,
		inbound:   newStreamBuffer(512 << 10),
		change:    make(chan struct{}),
		genDone:   make(map[uint64]chan struct{}),
		genClosed: make(map[uint64]bool),
		losses:    make(chan TransportLoss, 8),
		progress:  make(chan struct{}, 1),
	}, nil
}

func (e *Endpoint) State() *StreamState {
	if e == nil {
		return nil
	}
	return e.state
}

func (e *Endpoint) Losses() <-chan TransportLoss {
	if e == nil {
		return nil
	}
	return e.losses
}

func (e *Endpoint) Generation() uint64 {
	if e == nil {
		return 0
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.generation
}

func (e *Endpoint) GenerationDone(generation uint64) <-chan struct{} {
	if e == nil || generation == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ch, ok := e.genDone[generation]; ok {
		return ch
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (e *Endpoint) SetDeadline(time.Time) error      { return nil }
func (e *Endpoint) SetReadDeadline(time.Time) error  { return nil }
func (e *Endpoint) SetWriteDeadline(time.Time) error { return nil }

// Bind installs a strictly newer transport. Replay is written before ready is
// published, so fresh application bytes can never overtake unacknowledged data.
func (e *Endpoint) Bind(transport Transport, generation uint64) error {
	if e == nil || transport == nil || generation == 0 {
		return ErrBinding
	}

	e.writeMu.Lock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		e.writeMu.Unlock()
		_ = transport.Close()
		return net.ErrClosed
	}
	if generation <= e.generation {
		e.mu.Unlock()
		e.writeMu.Unlock()
		_ = transport.Close()
		return ErrBinding
	}
	old := e.transport
	oldGeneration := e.generation
	if old != nil && oldGeneration != 0 {
		e.closeGenerationLocked(oldGeneration)
	}
	e.transport = transport
	e.generation = generation
	e.ready = false
	e.genDone[generation] = make(chan struct{})
	e.genClosed[generation] = false
	for g := range e.genDone {
		if g+8 < generation && e.genClosed[g] {
			delete(e.genDone, g)
			delete(e.genClosed, g)
		}
	}
	e.signalLocked()
	e.mu.Unlock()
	e.writeMu.Unlock()

	if old != nil && old != transport {
		_ = old.Close()
	}

	// Start reading immediately so two peers can bind concurrently even when
	// both have replay data waiting to cross the new transport.
	go e.readLoop(transport, generation)
	go e.activateTransport(transport, generation)
	return nil
}

func (e *Endpoint) activateTransport(transport Transport, generation uint64) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()

	e.mu.Lock()
	current := !e.closed && e.transport == transport && e.generation == generation
	e.mu.Unlock()
	if !current {
		return
	}

	for _, frame := range e.state.ReplayFrames() {
		if err := WriteFrame(transport, frame); err != nil {
			e.loseTransport(transport, generation, err)
			return
		}
	}

	e.mu.Lock()
	if !e.closed && e.transport == transport && e.generation == generation {
		e.ready = true
		e.signalLocked()
	}
	e.mu.Unlock()
}

func (e *Endpoint) Read(p []byte) (int, error) {
	if e == nil {
		return 0, net.ErrClosed
	}
	return e.inbound.Read(p)
}

// Write accepts bytes into the logical stream. Once a chunk enters the replay
// buffer this call waits for a usable transport rather than returning a short
// error that could cause the application to resend already-accepted bytes.
func (e *Endpoint) Write(p []byte) (int, error) {
	if e == nil {
		return 0, net.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}

	e.appWriteMu.Lock()
	defer e.appWriteMu.Unlock()

	written := 0
	for written < len(p) {
		end := min(written+MaxPayload, len(p))
		chunk := p[written:end]

		var frame Frame
		for {
			var err error
			frame, err = e.state.Data(chunk)
			if err == nil {
				break
			}
			if !errors.Is(err, ErrReplayLimit) {
				return written, err
			}
			if err := e.waitProgress(); err != nil {
				return written, err
			}
		}
		if err := e.sendBuffered(frame); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

// CloseWrite sends a logical TCP FIN. FIN remains replayable until the peer
// acknowledges it, so losing the transport after half-close is recoverable.
func (e *Endpoint) CloseWrite() error {
	if e == nil {
		return net.ErrClosed
	}
	e.appWriteMu.Lock()
	defer e.appWriteMu.Unlock()
	frame, err := e.state.FIN()
	if err != nil {
		return err
	}
	return e.sendBuffered(frame)
}

func (e *Endpoint) Abort(err error) {
	if e == nil {
		return
	}
	e.fail(err)
}

func (e *Endpoint) Closed() bool {
	if e == nil {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	transport := e.transport
	e.transport = nil
	e.ready = false
	e.closeGenerationLocked(e.generation)
	e.signalLocked()
	e.mu.Unlock()

	if transport != nil {
		_ = transport.Close()
	}
	e.inbound.CloseWithError(net.ErrClosed)
	e.notifyProgress()
	return nil
}

func (e *Endpoint) sendBuffered(frame Frame) error {
	for {
		transport, generation, err := e.waitTransport()
		if err != nil {
			return err
		}

		e.writeMu.Lock()
		e.mu.Lock()
		current := !e.closed && e.ready && e.transport == transport && e.generation == generation
		e.mu.Unlock()
		if !current {
			e.writeMu.Unlock()
			continue
		}
		err = WriteFrame(transport, frame)
		e.writeMu.Unlock()
		if err == nil {
			return nil
		}
		e.loseTransport(transport, generation, err)
	}
}

func (e *Endpoint) sendControlCurrent(transport Transport, generation uint64, frame Frame) {
	e.writeMu.Lock()
	e.mu.Lock()
	current := !e.closed && e.transport == transport && e.generation == generation
	e.mu.Unlock()
	if !current {
		e.writeMu.Unlock()
		return
	}
	err := WriteFrame(transport, frame)
	e.writeMu.Unlock()
	if err != nil {
		e.loseTransport(transport, generation, err)
	}
}

func (e *Endpoint) readLoop(transport Transport, generation uint64) {
	for {
		frame, err := ReadFrame(transport)
		if err != nil {
			e.loseTransport(transport, generation, err)
			return
		}

		fresh, _, err := e.state.Handle(frame)
		if err != nil {
			e.fail(err)
			return
		}
		e.notifyProgress()

		switch frame.Type {
		case FrameData:
			ack, err := e.state.AckFrame()
			if err != nil {
				e.fail(err)
				return
			}
			e.sendControlCurrent(transport, generation, ack)
			if len(fresh) > 0 {
				if err := e.inbound.Write(fresh); err != nil {
					return
				}
			}
		case FrameFIN:
			ack, err := e.state.AckFrame()
			if err != nil {
				e.fail(err)
				return
			}
			e.sendControlCurrent(transport, generation, ack)
			e.inbound.CloseWithError(nil)
			return
		case FrameRST:
			e.fail(net.ErrClosed)
			return
		case FrameAck:
		}
	}
}

func (e *Endpoint) waitTransport() (Transport, uint64, error) {
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return nil, 0, net.ErrClosed
		}
		if e.ready && e.transport != nil {
			transport, generation := e.transport, e.generation
			e.mu.Unlock()
			return transport, generation, nil
		}
		change := e.change
		e.mu.Unlock()
		<-change
	}
}

func (e *Endpoint) waitProgress() error {
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return net.ErrClosed
		}
		e.mu.Unlock()
		select {
		case <-e.progress:
			return nil
		default:
		}

		e.mu.Lock()
		change := e.change
		e.mu.Unlock()
		select {
		case <-e.progress:
			return nil
		case <-change:
		}
	}
}

func (e *Endpoint) loseTransport(transport Transport, generation uint64, err error) {
	if e == nil || transport == nil {
		return
	}
	e.mu.Lock()
	if e.closed || e.transport != transport || e.generation != generation {
		e.mu.Unlock()
		return
	}
	e.transport = nil
	e.ready = false
	e.closeGenerationLocked(generation)
	e.signalLocked()
	e.mu.Unlock()
	_ = transport.Close()
	e.reportLoss(generation, err)
}

func (e *Endpoint) fail(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	transport := e.transport
	e.transport = nil
	e.ready = false
	e.closeGenerationLocked(e.generation)
	e.signalLocked()
	e.mu.Unlock()
	if transport != nil {
		_ = transport.Close()
	}
	e.inbound.CloseWithError(err)
	e.notifyProgress()
}

func (e *Endpoint) reportLoss(generation uint64, err error) {
	select {
	case e.losses <- TransportLoss{Generation: generation, Err: err}:
	default:
	}
}

func (e *Endpoint) notifyProgress() {
	select {
	case e.progress <- struct{}{}:
	default:
	}
}

func (e *Endpoint) signalLocked() {
	close(e.change)
	e.change = make(chan struct{})
}

func (e *Endpoint) closeGenerationLocked(generation uint64) {
	if generation == 0 || e.genClosed[generation] {
		return
	}
	ch, ok := e.genDone[generation]
	if !ok {
		ch = make(chan struct{})
		e.genDone[generation] = ch
	}
	close(ch)
	e.genClosed[generation] = true
}

type streamBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	limit  int
	closed bool
	err    error
}

func newStreamBuffer(limit int) *streamBuffer {
	if limit <= 0 {
		limit = 512 << 10
	}
	b := &streamBuffer{limit: limit}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *streamBuffer) Write(p []byte) error {
	if b == nil || len(p) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for !b.closed && b.buf.Len()+len(p) > b.limit {
		b.cond.Wait()
	}
	if b.closed {
		if b.err != nil {
			return b.err
		}
		return io.ErrClosedPipe
	}
	_, _ = b.buf.Write(p)
	b.cond.Broadcast()
	return nil
}

func (b *streamBuffer) Read(p []byte) (int, error) {
	if b == nil {
		return 0, net.ErrClosed
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.closed {
		b.cond.Wait()
	}
	if b.buf.Len() > 0 {
		n, _ := b.buf.Read(p)
		b.cond.Broadcast()
		return n, nil
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

func (b *streamBuffer) CloseWithError(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.err = err
	}
	b.cond.Broadcast()
	b.mu.Unlock()
}

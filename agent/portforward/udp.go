package portforward

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type udpAssociation struct {
	runtime *mappingRuntime
	key     string
	source  net.Addr
	ctx     context.Context
	cancel  context.CancelFunc
	send    chan []byte

	closed    atomic.Bool
	lastUsed  atomic.Int64
	stopOnce  sync.Once
	sessionMu sync.Mutex
	session   DatagramSession
}

func (a *udpAssociation) touch() {
	a.lastUsed.Store(time.Now().UnixNano())
}

func (a *udpAssociation) enqueue(payload []byte) bool {
	if a == nil || a.closed.Load() || a.ctx.Err() != nil {
		return false
	}
	select {
	case a.send <- payload:
		a.touch()
		return true
	default:
		return false
	}
}

func (a *udpAssociation) stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		a.closed.Store(true)
		a.cancel()
		a.sessionMu.Lock()
		session := a.session
		a.sessionMu.Unlock()
		if session != nil {
			_ = session.Close()
		}
	})
}

func (a *udpAssociation) run() {
	defer a.runtime.wg.Done()
	var receiveDone chan struct{}
	defer func() {
		a.stop()
		if receiveDone != nil {
			<-receiveDone
		}
		a.runtime.removeAssociation(a)
	}()

	openCtx, cancel := context.WithTimeout(a.ctx, a.runtime.options.OpenTimeout)
	session, path, err := a.runtime.transport.OpenUDP(openCtx, a.runtime.mapping)
	cancel()
	if err != nil {
		if a.ctx.Err() == nil {
			a.runtime.setError(err)
		}
		return
	}
	if session == nil {
		a.runtime.setError(errors.New("transport returned a nil UDP association"))
		return
	}
	if path == "" {
		path = PathUnknown
	}
	a.sessionMu.Lock()
	a.session = session
	a.sessionMu.Unlock()
	a.runtime.udpPath.Store(path)
	a.runtime.setError(nil)
	a.runtime.activeUDP.Add(1)
	defer a.runtime.activeUDP.Add(-1)

	receiveErrors := make(chan error, 1)
	receiveDone = make(chan struct{})
	go func() {
		defer close(receiveDone)
		for {
			payload, receiveErr := session.Receive(a.ctx)
			if receiveErr != nil {
				select {
				case receiveErrors <- receiveErr:
				default:
				}
				return
			}
			if len(payload) > maxUDPPayload {
				continue
			}
			n, writeErr := a.runtime.udpConn.WriteTo(payload, a.source)
			if writeErr != nil {
				select {
				case receiveErrors <- writeErr:
				default:
				}
				return
			}
			a.runtime.bytesDown.Add(uint64(n))
			a.touch()
		}
	}()

	interval := a.runtime.options.UDPIdleTimeout / 4
	if interval > 15*time.Second {
		interval = 15 * time.Second
	}
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case payload := <-a.send:
			if sendErr := session.Send(a.ctx, payload); sendErr != nil {
				if a.ctx.Err() == nil {
					a.runtime.setError(sendErr)
				}
				return
			}
			a.runtime.bytesUp.Add(uint64(len(payload)))
			a.touch()
		case receiveErr := <-receiveErrors:
			if receiveErr != nil && a.ctx.Err() == nil {
				a.runtime.setError(receiveErr)
			}
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, a.lastUsed.Load())) >= a.runtime.options.UDPIdleTimeout {
				return
			}
		case <-a.ctx.Done():
			return
		}
	}
}

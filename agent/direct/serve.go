package direct

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	defaultMaxSessions = 128
	defaultMaxStreams  = 1024
)

// ServeExit accepts authenticated Public Direct sessions and feeds their proxy
// streams into the existing Exit handler. No Public Direct-specific TCP or UDP
// business logic exists here.
func ServeExit(ctx context.Context, listener *PublicListener, handler *exit.Handler, maxSessions, maxStreams int) error {
	if listener == nil || handler == nil {
		return errors.New("public direct listener and exit handler are required")
	}
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	if maxStreams <= 0 {
		maxStreams = defaultMaxStreams
	}
	sessionSlots := make(chan struct{}, maxSessions)
	var sessions sync.WaitGroup
	defer sessions.Wait()

	for {
		accepted, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			if errors.Is(err, ErrUnauthorized) {
				continue
			}
			continue
		}
		if accepted == nil || accepted.Tunnel == nil {
			continue
		}
		select {
		case sessionSlots <- struct{}{}:
		default:
			_ = accepted.Tunnel.Close()
			continue
		}
		sessions.Add(1)
		go func(session tunnel.TunnelSession) {
			defer sessions.Done()
			defer func() { <-sessionSlots }()
			serveExitSession(ctx, session, handler, maxStreams)
		}(accepted.Tunnel)
	}
}

func serveExitSession(ctx context.Context, session tunnel.TunnelSession, handler *exit.Handler, maxStreams int) {
	defer session.Close()
	streamSlots := make(chan struct{}, maxStreams)
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		stream, err := session.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case streamSlots <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		stopHeader := tunnel.InterruptOnCancel(ctx, stream)
		header, err := protocol.ReadStreamHeader(stream)
		stopHeader()
		if err != nil || (header.Type != protocol.FrameTypeOpenTCP &&
			header.Type != protocol.FrameTypeOpenUDP &&
			header.Type != protocol.FrameTypeSpeedTest) {
			<-streamSlots
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-streamSlots }()
			handler.HandleStreamWithHeader(ctx, stream, header)
		}()
	}
}

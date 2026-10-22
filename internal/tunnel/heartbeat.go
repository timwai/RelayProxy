package tunnel

import (
	"context"
	"errors"
	"net"
	"time"

	"relayproxy/internal/protocol"
)

// Heartbeat exchanges exactly one Ping/Pong. Soft read deadlines can be
// extended twice without losing a partially received length-prefixed frame.
// Partial writes, EOF and malformed frames remain fatal.
func Heartbeat(ctx context.Context, ctrl TunnelStream, sess TunnelSession, interval, lastRTT time.Duration, ping protocol.PingMessage) (protocol.PongMessage, time.Duration, error) {
	if q, ok := sess.(*QUICSession); ok && q.conn != nil {
		lastRTT = max(lastRTT, q.conn.ConnectionStats().SmoothedRTT)
	}
	budget := heartbeatReadBudget(interval, lastRTT)
	return heartbeatExchange(ctx, ctrl, ping, budget, min(30*time.Second, 3*budget, max(5*time.Second, 2*interval)))
}

func heartbeatReadBudget(interval, rtt time.Duration) time.Duration {
	return min(max(5*time.Second, 4*rtt), min(30*time.Second, max(5*time.Second, 2*interval)))
}

func heartbeatExchange(ctx context.Context, ctrl TunnelStream, ping protocol.PingMessage, window, total time.Duration) (pong protocol.PongMessage, rtt time.Duration, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	start := time.Now()
	stop := context.AfterFunc(ctx, func() { _ = ctrl.SetDeadline(time.Now()) })
	defer stop()
	defer func() { _ = ctrl.SetDeadline(time.Time{}) }()
	if err = ctrl.SetWriteDeadline(start.Add(window)); err != nil {
		return
	}
	if err = protocol.WriteJSON(ctrl, ping); err != nil {
		return
	}
	r := &heartbeatReader{stream: ctrl, ctx: ctx, window: window, hard: start.Add(total)}
	if err = ctrl.SetReadDeadline(minTime(time.Now().Add(window), r.hard)); err != nil {
		return
	}
	err = protocol.ReadJSON(r, &pong)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	rtt = time.Since(start)
	return
}

type heartbeatReader struct {
	stream     TunnelStream
	ctx        context.Context
	window     time.Duration
	hard       time.Time
	extensions int
}

func (r *heartbeatReader) Read(p []byte) (int, error) {
	for {
		n, err := r.stream.Read(p)
		var ne net.Error
		if err == nil || !errors.As(err, &ne) || !ne.Timeout() || r.ctx.Err() != nil || r.extensions >= 2 || !time.Now().Before(r.hard) {
			return n, err
		}
		r.extensions++
		if deadlineErr := r.stream.SetReadDeadline(minTime(time.Now().Add(r.window), r.hard)); deadlineErr != nil {
			return n, deadlineErr
		}
		// Deliver bytes already read; io.ReadFull retains its offset while the
		// next call resumes the same frame after the deadline extension.
		if n > 0 {
			return n, nil
		}
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

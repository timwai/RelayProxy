package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const defaultAuthTimeout = 5 * time.Second

type ListenerConfig struct {
	ListenAddress string
	TLSConfig     *tls.Config
	QUICConfig    *quic.Config
	Authenticator Authenticator
	AuthTimeout   time.Duration
}

type AcceptedSession struct {
	Tunnel         tunnel.TunnelSession
	ClientDeviceID string
	ExitDeviceID   string
}

type PublicListener struct {
	transport     *tunnel.DirectQUICListener
	authenticator Authenticator
	authTimeout   time.Duration
}

func Listen(config ListenerConfig) (*PublicListener, error) {
	if strings.TrimSpace(config.ListenAddress) == "" {
		return nil, errors.New("public direct listen address is required")
	}
	if config.Authenticator == nil {
		return nil, ErrAuthenticatorRequired
	}
	timeout := config.AuthTimeout
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	transport, err := tunnel.ListenDirectQUIC(config.ListenAddress, config.TLSConfig, config.QUICConfig)
	if err != nil {
		return nil, err
	}
	return &PublicListener{transport: transport, authenticator: config.Authenticator, authTimeout: timeout}, nil
}

func (l *PublicListener) Addr() string {
	if l == nil || l.transport == nil || l.transport.Addr() == nil {
		return ""
	}
	return l.transport.Addr().String()
}

func (l *PublicListener) Close() error {
	if l == nil || l.transport == nil {
		return nil
	}
	return l.transport.Close()
}

func (l *PublicListener) Accept(ctx context.Context) (*AcceptedSession, error) {
	if l == nil || l.transport == nil {
		return nil, errors.New("public direct listener is closed")
	}
	session, err := l.transport.Accept(ctx)
	if err != nil {
		return nil, err
	}
	accepted, err := l.authenticate(ctx, session)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return accepted, nil
}

func (l *PublicListener) authenticate(ctx context.Context, session tunnel.TunnelSession) (*AcceptedSession, error) {
	authCtx, cancel := context.WithTimeout(ctx, l.authTimeout)
	defer cancel()

	stream, err := session.AcceptStream(authCtx)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication stream unavailable", ErrUnauthorized)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(l.authTimeout))

	var request protocol.PublicDirectAuthRequest
	if err := protocol.ReadJSON(stream, &request); err != nil {
		_ = writeAuthFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
		return nil, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
	}
	request.ClientDeviceID = strings.TrimSpace(request.ClientDeviceID)
	request.ExitDeviceID = strings.TrimSpace(request.ExitDeviceID)
	if request.Version != protocol.PublicDirectAuthVersion ||
		request.ClientDeviceID == "" || request.ExitDeviceID == "" || len(request.Ticket) == 0 {
		_ = writeAuthFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
		return nil, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
	}
	if err := l.authenticator.Authenticate(authCtx, request); err != nil {
		_ = writeAuthFailure(stream, protocol.ErrCodeAuthFailed, "authentication failed")
		return nil, fmt.Errorf("%w: ticket rejected", ErrUnauthorized)
	}
	if err := protocol.WriteJSON(stream, protocol.PublicDirectAuthResponse{Success: true}); err != nil {
		return nil, fmt.Errorf("public direct authentication response: %w", err)
	}
	_ = stream.SetDeadline(time.Time{})
	return &AcceptedSession{
		Tunnel:         session,
		ClientDeviceID: request.ClientDeviceID,
		ExitDeviceID:   request.ExitDeviceID,
	}, nil
}

func writeAuthFailure(stream tunnel.TunnelStream, code, message string) error {
	return protocol.WriteJSON(stream, protocol.PublicDirectAuthResponse{
		Success: false, ErrorCode: code, ErrorMessage: message,
	})
}

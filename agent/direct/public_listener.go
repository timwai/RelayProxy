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
	for {
		session, err := l.transport.Accept(ctx)
		if err != nil {
			return nil, err
		}
		accepted, probe, err := l.handshake(ctx, session)
		if err != nil {
			_ = session.Close()
			return nil, err
		}
		if probe {
			// The verifier owns connection shutdown after it has consumed the
			// challenge response. Sending an application close from this side can
			// overtake stream delivery and turn a successful probe into session closed.
			continue
		}
		return accepted, nil
	}
}

func (l *PublicListener) handshake(ctx context.Context, session tunnel.TunnelSession) (*AcceptedSession, bool, error) {
	authCtx, cancel := context.WithTimeout(ctx, l.authTimeout)
	defer cancel()

	stream, err := session.AcceptStream(authCtx)
	if err != nil {
		return nil, false, fmt.Errorf("%w: authentication stream unavailable", ErrUnauthorized)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(l.authTimeout))

	var handshake protocol.PublicDirectHandshakeRequest
	if err := protocol.ReadJSON(stream, &handshake); err != nil {
		_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid handshake request")
		return nil, false, fmt.Errorf("%w: invalid handshake request", ErrUnauthorized)
	}
	switch handshake.Type {
	case protocol.PublicDirectHandshakeProbe:
		if handshake.Probe == nil || len(handshake.Probe.Nonce) < 16 || len(handshake.Probe.Nonce) > 64 {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid probe request")
			return nil, false, fmt.Errorf("%w: invalid probe request", ErrUnauthorized)
		}
		if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{
			Success: true,
			Probe: &protocol.PublicDirectProbeResponse{
				Nonce: append([]byte(nil), handshake.Probe.Nonce...),
			},
		}); err != nil {
			return nil, false, fmt.Errorf("public direct probe response: %w", err)
		}
		// Do not close the whole QUIC connection until the verifier has read the
		// response. Closing the connection immediately can race buffered stream
		// delivery and surface a spurious "session closed" on the Server.
		_ = stream.CloseWrite()
		_ = stream.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		var drain [1]byte
		_, _ = stream.Read(drain[:])
		return nil, true, nil

	case protocol.PublicDirectHandshakeAuth:
		if handshake.Auth == nil {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
			return nil, false, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
		}
		request := *handshake.Auth
		request.ClientDeviceID = strings.TrimSpace(request.ClientDeviceID)
		request.ExitDeviceID = strings.TrimSpace(request.ExitDeviceID)
		if request.Version != protocol.PublicDirectAuthVersion ||
			request.ClientDeviceID == "" || request.ExitDeviceID == "" || len(request.Ticket) == 0 {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
			return nil, false, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
		}
		if err := l.authenticator.Authenticate(authCtx, request); err != nil {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeAuthFailed, "authentication failed")
			return nil, false, fmt.Errorf("%w: ticket rejected", ErrUnauthorized)
		}
		if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{Success: true}); err != nil {
			return nil, false, fmt.Errorf("public direct authentication response: %w", err)
		}
		_ = stream.SetDeadline(time.Time{})
		return &AcceptedSession{
			Tunnel:         session,
			ClientDeviceID: request.ClientDeviceID,
			ExitDeviceID:   request.ExitDeviceID,
		}, false, nil
	default:
		_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "unsupported handshake type")
		return nil, false, fmt.Errorf("%w: unsupported handshake type", ErrUnauthorized)
	}
}

func writeHandshakeFailure(stream tunnel.TunnelStream, code, message string) error {
	return protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{
		Success: false, ErrorCode: code, ErrorMessage: message,
	})
}

package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type DialConfig struct {
	Address        string
	TLSConfig      *tls.Config
	QUICConfig     *quic.Config
	ClientDeviceID string
	ExitDeviceID   string
	Ticket         []byte
	AuthTimeout    time.Duration
}

func Dial(ctx context.Context, config DialConfig) (tunnel.TunnelSession, error) {
	config.Address = strings.TrimSpace(config.Address)
	config.ClientDeviceID = strings.TrimSpace(config.ClientDeviceID)
	config.ExitDeviceID = strings.TrimSpace(config.ExitDeviceID)
	if config.Address == "" || config.ClientDeviceID == "" || config.ExitDeviceID == "" || len(config.Ticket) == 0 {
		return nil, errors.New("public direct dial requires address, client id, exit id and ticket")
	}
	timeout := config.AuthTimeout
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	session, err := tunnel.DialDirectQUIC(ctx, config.Address, config.TLSConfig, config.QUICConfig)
	if err != nil {
		return nil, err
	}
	authCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stream, err := session.OpenStream(authCtx)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Now().Add(timeout))
	request := protocol.PublicDirectHandshakeRequest{
		Type: protocol.PublicDirectHandshakeAuth,
		Auth: &protocol.PublicDirectAuthRequest{
			Version:        protocol.PublicDirectAuthVersion,
			ClientDeviceID: config.ClientDeviceID,
			ExitDeviceID:   config.ExitDeviceID,
			Ticket:         append([]byte(nil), config.Ticket...),
		},
	}
	if err := protocol.WriteJSON(stream, request); err != nil {
		_ = stream.Close()
		_ = session.Close()
		return nil, err
	}
	var response protocol.PublicDirectHandshakeResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		_ = stream.Close()
		_ = session.Close()
		return nil, protocol.NewRelayError(protocol.ErrCodeAuthFailed, "public direct authentication failed")
	}
	_ = stream.Close()
	if !response.Success {
		_ = session.Close()
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeAuthFailed
		}
		message := response.ErrorMessage
		if message == "" {
			message = "public direct authentication failed"
		}
		return nil, protocol.NewRelayError(code, message)
	}
	return session, nil
}

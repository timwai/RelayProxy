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

var dialPublicDirectQUIC = tunnel.DialDirectQUIC

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
	config, err := normalizeDialConfig(config)
	if err != nil {
		return nil, err
	}
	session, err := dialPublicDirectQUIC(ctx, config.Address, config.TLSConfig, config.QUICConfig)
	if err != nil {
		return nil, err
	}
	if err := authenticateSession(ctx, session, config); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

// DialAny races only the QUIC/TLS transport handshake across verified endpoints.
// The one-time Direct Access Ticket is sent exactly once, after a transport
// winner is selected, so IPv4/IPv6 racing cannot consume the same nonce twice.
func DialAny(ctx context.Context, configs []DialConfig) (tunnel.TunnelSession, string, error) {
	if len(configs) == 0 {
		return nil, "", errors.New("public direct dial requires at least one endpoint")
	}
	normalized := make([]DialConfig, 0, len(configs))
	for _, config := range configs {
		value, err := normalizeDialConfig(config)
		if err != nil {
			return nil, "", err
		}
		normalized = append(normalized, value)
	}
	if len(normalized) == 1 {
		session, err := Dial(ctx, normalized[0])
		return session, normalized[0].Address, err
	}

	type result struct {
		session tunnel.TunnelSession
		config  DialConfig
		err     error
	}
	raceCtx, cancelRace := context.WithCancel(ctx)
	defer cancelRace()
	results := make(chan result)
	for _, config := range normalized {
		config := config
		go func() {
			session, err := dialPublicDirectQUIC(raceCtx, config.Address, config.TLSConfig, config.QUICConfig)
			if err != nil {
				select {
				case results <- result{config: config, err: err}:
				case <-raceCtx.Done():
				}
				return
			}
			select {
			case results <- result{session: session, config: config}:
			case <-raceCtx.Done():
				_ = session.Close()
			}
		}()
	}

	var lastErr error
	for range normalized {
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case result := <-results:
			if result.err != nil {
				lastErr = result.err
				continue
			}
			cancelRace()
			if err := authenticateSession(ctx, result.session, result.config); err != nil {
				_ = result.session.Close()
				return nil, result.config.Address, err
			}
			return result.session, result.config.Address, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("public direct transport race failed")
	}
	return nil, "", lastErr
}

func normalizeDialConfig(config DialConfig) (DialConfig, error) {
	config.Address = strings.TrimSpace(config.Address)
	config.ClientDeviceID = strings.TrimSpace(config.ClientDeviceID)
	config.ExitDeviceID = strings.TrimSpace(config.ExitDeviceID)
	if config.Address == "" || config.ClientDeviceID == "" || config.ExitDeviceID == "" || len(config.Ticket) == 0 {
		return config, errors.New("public direct dial requires address, client id, exit id and ticket")
	}
	return config, nil
}

func authenticateSession(ctx context.Context, session tunnel.TunnelSession, config DialConfig) error {
	timeout := config.AuthTimeout
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	authCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stream, err := session.OpenStream(authCtx)
	if err != nil {
		return err
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
		return err
	}
	var response protocol.PublicDirectHandshakeResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		_ = stream.Close()
		return protocol.NewRelayError(protocol.ErrCodeAuthFailed, "public direct authentication failed")
	}
	_ = stream.Close()
	if !response.Success {
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeAuthFailed
		}
		message := response.ErrorMessage
		if message == "" {
			message = "public direct authentication failed"
		}
		return protocol.NewRelayError(code, message)
	}
	return nil
}

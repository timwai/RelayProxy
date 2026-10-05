package direct

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/exit"
	directcore "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type ExitRuntimeOptions struct {
	ListenAddress           string
	ManualAdvertise         string
	AuthTimeout             time.Duration
	AuthAttemptsPerMinute   int
	MaxConcurrentHandshakes int
	MaxSessions             int
	MaxStreams              int
	RegisterTimeout         time.Duration
}

type ExitRuntime struct {
	cancel   context.CancelFunc
	listener *PublicListener
	done     chan struct{}
	once     sync.Once

	listenPort int
	candidates []protocol.PublicDirectEndpointCandidate
}

func StartExitRuntime(
	parent context.Context,
	relay tunnel.TunnelSession,
	accepted protocol.DeviceAccepted,
	handler *exit.Handler,
	options ExitRuntimeOptions,
) (*ExitRuntime, error) {
	if parent == nil {
		parent = context.Background()
	}
	if relay == nil || handler == nil {
		return nil, fmt.Errorf("public direct exit runtime requires relay session and exit handler")
	}
	if !slices.Contains(accepted.TransportCapabilities, protocol.CapabilityProxyPublicDirect) {
		return nil, fmt.Errorf("server does not advertise public direct capability")
	}
	authenticator, err := NewTicketAuthenticator(
		accepted.PublicDirectTicketIssuer,
		accepted.PublicDirectTicketKey,
		accepted.DeviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize ticket authenticator: %w", err)
	}
	identity, err := directcore.GenerateTLSIdentity()
	if err != nil {
		return nil, fmt.Errorf("generate TLS identity: %w", err)
	}

	listenAddress := strings.TrimSpace(options.ListenAddress)
	if listenAddress == "" {
		listenAddress = ":0"
	}
	listener, err := Listen(ListenerConfig{
		ListenAddress: listenAddress,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{identity.Certificate},
		},
		Authenticator:           authenticator,
		AuthTimeout:             options.AuthTimeout,
		AuthAttemptsPerMinute:   options.AuthAttemptsPerMinute,
		MaxConcurrentHandshakes: options.MaxConcurrentHandshakes,
	})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	_, portText, err := net.SplitHostPort(listener.Addr())
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("parse listener address %q: %w", listener.Addr(), err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		_ = listener.Close()
		return nil, fmt.Errorf("invalid listener port %q", portText)
	}
	candidates, err := DiscoverEndpointCandidates(uint16(port), options.ManualAdvertise)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("discover endpoints: %w", err)
	}

	registerTimeout := options.RegisterTimeout
	if registerTimeout <= 0 {
		registerTimeout = 5 * time.Second
	}
	registerCtx, cancelRegister := context.WithTimeout(parent, registerTimeout)
	response, err := RegisterEndpoint(registerCtx, relay, protocol.PublicDirectRegistrationRequest{
		ListenerPort:    uint16(port),
		CertFingerprint: identity.Fingerprint,
		Candidates:      candidates,
	})
	cancelRegister()
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("register endpoint: %w", err)
	}
	if !response.Success {
		_ = listener.Close()
		return nil, fmt.Errorf("register endpoint rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}

	runtimeCtx, cancel := context.WithCancel(parent)
	runtime := &ExitRuntime{
		cancel:      cancel,
		listener:    listener,
		done:        make(chan struct{}),
		listenPort:  port,
		candidates:  append([]protocol.PublicDirectEndpointCandidate(nil), candidates...),
	}
	go func() {
		defer close(runtime.done)
		_ = ServeExit(runtimeCtx, listener, handler, options.MaxSessions, options.MaxStreams)
	}()
	return runtime, nil
}

func (r *ExitRuntime) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.listener != nil {
			closeErr = r.listener.Close()
		}
		if r.done != nil {
			<-r.done
		}
	})
	return closeErr
}

func (r *ExitRuntime) ListenPort() int {
	if r == nil {
		return 0
	}
	return r.listenPort
}

func (r *ExitRuntime) Candidates() []protocol.PublicDirectEndpointCandidate {
	if r == nil {
		return nil
	}
	return append([]protocol.PublicDirectEndpointCandidate(nil), r.candidates...)
}

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
	"relayproxy/internal/netutil"
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
	PortStart               int
	PortEnd                 int
	RegisterTimeout         time.Duration
	NetworkCheckInterval    time.Duration
	NetworkSignature        func() string
}

type ExitRuntime struct {
	cancel       context.CancelFunc
	listener     *PublicListener
	serveDone    chan struct{}
	networkDone  chan struct{}
	once         sync.Once
	mu           sync.RWMutex
	listenPort   int
	candidates   []protocol.PublicDirectEndpointCandidate
	networkEpoch uint64
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

	listenerConfig := ListenerConfig{
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{identity.Certificate},
		},
		Authenticator:           authenticator,
		AuthTimeout:             options.AuthTimeout,
		AuthAttemptsPerMinute:   options.AuthAttemptsPerMinute,
		MaxConcurrentHandshakes: options.MaxConcurrentHandshakes,
	}
	listener, err := listenExitRuntime(listenerConfig, options.ListenAddress, options.PortStart, options.PortEnd)
	if err != nil {
		return nil, err
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
	registerTimeout := options.RegisterTimeout
	if registerTimeout <= 0 {
		registerTimeout = 5 * time.Second
	}
	const initialNetworkEpoch uint64 = 1
	candidates, err := registerExitEndpoints(parent, relay, uint16(port), identity.Fingerprint,
		options.ManualAdvertise, initialNetworkEpoch, registerTimeout)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	runtimeCtx, cancel := context.WithCancel(parent)
	runtime := &ExitRuntime{
		cancel:       cancel,
		listener:     listener,
		serveDone:    make(chan struct{}),
		networkDone:  make(chan struct{}),
		listenPort:   port,
		candidates:   append([]protocol.PublicDirectEndpointCandidate(nil), candidates...),
		networkEpoch: initialNetworkEpoch,
	}
	go func() {
		defer close(runtime.serveDone)
		_ = ServeExit(runtimeCtx, listener, handler, options.MaxSessions, options.MaxStreams)
	}()
	go runtime.watchNetwork(runtimeCtx, relay, identity.Fingerprint, options)
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
		if r.serveDone != nil {
			<-r.serveDone
		}
		if r.networkDone != nil {
			<-r.networkDone
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
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]protocol.PublicDirectEndpointCandidate(nil), r.candidates...)
}

func (r *ExitRuntime) watchNetwork(ctx context.Context, relay tunnel.TunnelSession, fingerprint string, options ExitRuntimeOptions) {
	defer close(r.networkDone)
	interval := options.NetworkCheckInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	signature := options.NetworkSignature
	if signature == nil {
		signature = netutil.CurrentNetworkSignature
	}
	previous := signature()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := signature()
			if current == "" || previous == "" {
				previous = current
				continue
			}
			if current == previous {
				continue
			}
			r.mu.RLock()
			nextEpoch := r.networkEpoch + 1
			port := r.listenPort
			r.mu.RUnlock()
			candidates, err := registerExitEndpoints(ctx, relay, uint16(port), fingerprint,
				options.ManualAdvertise, nextEpoch, options.RegisterTimeout)
			if err != nil {
				continue
			}
			r.mu.Lock()
			r.networkEpoch = nextEpoch
			r.candidates = append([]protocol.PublicDirectEndpointCandidate(nil), candidates...)
			r.mu.Unlock()
			previous = current
		}
	}
}

func registerExitEndpoints(
	ctx context.Context,
	relay tunnel.TunnelSession,
	port uint16,
	fingerprint string,
	manualAdvertise string,
	networkEpoch uint64,
	timeout time.Duration,
) ([]protocol.PublicDirectEndpointCandidate, error) {
	candidates, err := DiscoverEndpointCandidates(port, manualAdvertise)
	if err != nil {
		return nil, fmt.Errorf("discover endpoints: %w", err)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	registerCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := RegisterEndpoint(registerCtx, relay, protocol.PublicDirectRegistrationRequest{
		ListenerPort:    port,
		CertFingerprint: fingerprint,
		NetworkEpoch:    networkEpoch,
		Candidates:      candidates,
	})
	if err != nil {
		return nil, fmt.Errorf("register endpoint: %w", err)
	}
	if !response.Success {
		return nil, fmt.Errorf("register endpoint rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}
	return candidates, nil
}

func listenExitRuntime(config ListenerConfig, listenAddress string, portStart, portEnd int) (*PublicListener, error) {
	listenAddress = strings.TrimSpace(listenAddress)
	if portStart == 0 && portEnd == 0 {
		if listenAddress == "" {
			listenAddress = ":0"
		}
		config.ListenAddress = listenAddress
		listener, err := Listen(config)
		if err != nil {
			return nil, fmt.Errorf("listen: %w", err)
		}
		return listener, nil
	}
	if portStart < 1 || portStart > 65535 || portEnd < portStart || portEnd > 65535 {
		return nil, fmt.Errorf("invalid public direct port range %d-%d", portStart, portEnd)
	}

	host := ""
	if listenAddress != "" {
		parsedHost, _, err := net.SplitHostPort(listenAddress)
		if err != nil {
			return nil, fmt.Errorf("parse listen address %q: %w", listenAddress, err)
		}
		host = parsedHost
	}
	var lastErr error
	for port := portStart; port <= portEnd; port++ {
		config.ListenAddress = net.JoinHostPort(host, strconv.Itoa(port))
		listener, err := Listen(config)
		if err == nil {
			return listener, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no public direct UDP port available in %d-%d: %w", portStart, portEnd, lastErr)
}

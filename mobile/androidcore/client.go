package androidcore

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentclient "relayproxy/agent/client"
	"relayproxy/agent/exit"
	proxyp2p "relayproxy/agent/p2p"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
	"relayproxy/internal/proxy/httpproxy"
	"relayproxy/internal/proxy/socks5"
	"relayproxy/internal/traffic"
	"relayproxy/internal/tunnel"
)

const clientVersion = "android-0.2.0"
const androidUnknownProcess = "__android_unknown__"

// ValidateRoutingConfig checks settings with the same parser used by NewClient.
// Gomobile exposes a non-nil error to Kotlin as an exception.
func ValidateRoutingConfig(configJSON string) error {
	var cfg routing.Config
	if strings.TrimSpace(configJSON) == "" {
		configJSON = "{}"
	}
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("decode routing config: %w", err)
	}
	if err := routing.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid routing config: %w", err)
	}
	return nil
}

type clientConfig struct {
	ServerAddress         string         `json:"serverAddress"`
	IdentityID            string         `json:"identityId"`
	DeviceName            string         `json:"deviceName"`
	QUICPort              int            `json:"quicPort"`
	TCPPort               int            `json:"tcpPort"`
	TransportMode         string         `json:"transportMode"`
	TLSEnabled            *bool          `json:"tlsEnabled"`
	InsecureTLS           bool           `json:"insecureTLS"`
	AllowInternet         *bool          `json:"allowInternet"`
	AllowPrivateNetwork   bool           `json:"allowPrivateNetwork"`
	AllowLoopback         bool           `json:"allowLoopback"`
	ExitEnabled           *bool          `json:"exitEnabled"`
	ClientEnabled         bool           `json:"clientEnabled"`
	RequestedCapabilities []string       `json:"requestedCapabilities"`
	SOCKS5Enabled         *bool          `json:"socks5Enabled"`
	HTTPEnabled           *bool          `json:"httpEnabled"`
	ProxyP2PEnabled       *bool          `json:"proxyP2pEnabled"`
	DefaultExitID         string         `json:"defaultExitId"`
	SOCKS5Listen          string         `json:"socks5Listen"`
	HTTPListen            string         `json:"httpListen"`
	Routing               routing.Config `json:"routing"`
	VPNProxyEnabled       bool           `json:"vpnProxyEnabled"`
	VPNProxyListen        string         `json:"vpnProxyListen"`
	VPNProxyToken         string         `json:"vpnProxyToken"`
}

type statusSnapshot struct {
	ConnectionState     string               `json:"connectionState"`
	ApprovalState       string               `json:"approvalState"`
	DeviceID            string               `json:"deviceId,omitempty"`
	DeviceName          string               `json:"deviceName"`
	IdentityName        string               `json:"identityName,omitempty"`
	PolicyRevision      int64                `json:"policyRevision,omitempty"`
	Transport           string               `json:"transport,omitempty"`
	ExitApproved        bool                 `json:"exitApproved"`
	ClientApproved      bool                 `json:"clientApproved"`
	ProxyState          string               `json:"proxyState,omitempty"`
	ProxyError          string               `json:"proxyError,omitempty"`
	SelectedExit        string               `json:"selectedExit"`
	ProxyExits          []protocol.ProxyExit `json:"proxyExits,omitempty"`
	ActiveStreams       int64                `json:"activeStreams"`
	LatencyMs           int64                `json:"latencyMs"`
	PowerConstrained    bool                 `json:"powerConstrained"`
	P2PState            string               `json:"p2pState,omitempty"`
	P2PPath             string               `json:"p2pPath,omitempty"`
	P2PRTTMs            int64                `json:"p2pRttMs,omitempty"`
	P2PCandidateSummary string               `json:"p2pCandidateSummary,omitempty"`
	P2PBytesUp          uint64               `json:"p2pBytesUp,omitempty"`
	P2PBytesDown        uint64               `json:"p2pBytesDown,omitempty"`
	ProxyActiveTCP      int64                `json:"proxyActiveTcp"`
	ProxyActiveUDP      int64                `json:"proxyActiveUdp"`
	ProxyTCPFlows       uint64               `json:"proxyTcpFlows"`
	ProxyUDPFlows       uint64               `json:"proxyUdpFlows"`
	ProxyBytesUp        uint64               `json:"proxyBytesUp"`
	ProxyBytesDown      uint64               `json:"proxyBytesDown"`
	NativeUDP           tunnel.DatagramUsage `json:"nativeUdp"`
	RoutingMode         routing.Mode         `json:"routingMode"`
	LastError           string               `json:"lastError,omitempty"`
}

// Client is the small gomobile-facing wrapper for the Android exit node.
// The exported surface intentionally uses only gomobile-supported scalar values.
type Client struct {
	cfg      clientConfig
	identity *deviceidentity.Identity
	handler  *exit.Handler
	manager  *tunnel.TunnelManager

	ctx    context.Context
	cancel context.CancelFunc

	mu               sync.RWMutex
	status           statusSnapshot
	starting         bool
	started          bool
	closed           bool
	powerConstrained bool
	proxyP2P         *proxyp2p.Manager
	proxyDialer      *agentclient.TunnelDialer
	routingDialer    *routing.RoutingDialer
	traffic          *traffic.Registry
	clientApproved   atomic.Bool
	socksServer      *socks5.Server
	vpnSocksServer   *socks5.Server
	httpServer       *httpproxy.Server
	proxyActiveTCP   atomic.Int64
	proxyActiveUDP   atomic.Int64
	proxyTCPFlows    atomic.Uint64
	proxyUDPFlows    atomic.Uint64
	proxyBytesUp     atomic.Uint64
	proxyBytesDown   atomic.Uint64

	wg sync.WaitGroup
}

func normalizeConfig(raw string) (clientConfig, error) {
	var cfg clientConfig
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	cfg.ServerAddress = strings.TrimSpace(cfg.ServerAddress)
	cfg.IdentityID = strings.ToLower(strings.TrimSpace(cfg.IdentityID))
	if cfg.ServerAddress == "" {
		return cfg, errors.New("serverAddress is required")
	}
	if strings.ContainsAny(cfg.ServerAddress, " /\\\t\r\n") {
		return cfg, errors.New("serverAddress must be a host or IP without scheme or port")
	}
	if !validIdentityID(cfg.IdentityID) {
		return cfg, errors.New("identityId must be the 16-character lowercase letter-and-digit ID generated by the server")
	}
	cfg.DeviceName = strings.TrimSpace(cfg.DeviceName)
	if cfg.DeviceName == "" {
		cfg.DeviceName = "RelayProxy Android"
	}
	if cfg.QUICPort == 0 {
		cfg.QUICPort = 443
	}
	if cfg.TCPPort == 0 {
		cfg.TCPPort = 443
	}
	if cfg.QUICPort < 1 || cfg.QUICPort > 65535 || cfg.TCPPort < 1 || cfg.TCPPort > 65535 {
		return cfg, errors.New("relay ports must be between 1 and 65535")
	}
	cfg.TransportMode = strings.ToLower(strings.TrimSpace(cfg.TransportMode))
	if cfg.TransportMode == "" {
		cfg.TransportMode = string(tunnel.ModeAuto)
	}
	switch tunnel.Mode(cfg.TransportMode) {
	case tunnel.ModeAuto, tunnel.ModeQUICOnly, tunnel.ModeTCPOnly:
	default:
		return cfg, fmt.Errorf("unsupported transportMode %q", cfg.TransportMode)
	}
	if cfg.TLSEnabled == nil {
		enabled := true
		cfg.TLSEnabled = &enabled
	}
	if cfg.AllowInternet == nil {
		enabled := true
		cfg.AllowInternet = &enabled
	}
	if cfg.ExitEnabled == nil {
		enabled := true
		cfg.ExitEnabled = &enabled
	}
	cfg.RequestedCapabilities = normalizeRequestedCapabilities(cfg.RequestedCapabilities)
	if len(cfg.RequestedCapabilities) == 0 {
		if cfg.ClientEnabled {
			cfg.RequestedCapabilities = append(cfg.RequestedCapabilities, protocol.CapabilityProxyClient)
		}
		if *cfg.ExitEnabled {
			cfg.RequestedCapabilities = append(cfg.RequestedCapabilities, protocol.CapabilityProxyExit)
		}
	}
	if len(cfg.RequestedCapabilities) == 0 {
		return cfg, errors.New("at least one requested capability is required")
	}
	if cfg.ClientEnabled && !contains(cfg.RequestedCapabilities, protocol.CapabilityProxyClient) {
		return cfg, errors.New("clientEnabled requires proxy.client to be requested")
	}
	if *cfg.ExitEnabled && !contains(cfg.RequestedCapabilities, protocol.CapabilityProxyExit) {
		return cfg, errors.New("exitEnabled requires proxy.exit to be requested")
	}
	if cfg.SOCKS5Enabled == nil {
		enabled := cfg.ClientEnabled
		cfg.SOCKS5Enabled = &enabled
	}
	if cfg.HTTPEnabled == nil {
		enabled := cfg.ClientEnabled
		cfg.HTTPEnabled = &enabled
	}
	if cfg.ProxyP2PEnabled == nil {
		enabled := true
		cfg.ProxyP2PEnabled = &enabled
	}
	if cfg.ClientEnabled && !*cfg.SOCKS5Enabled && !*cfg.HTTPEnabled && !cfg.VPNProxyEnabled {
		return cfg, errors.New("clientEnabled requires a local proxy listener")
	}
	cfg.DefaultExitID = strings.TrimSpace(cfg.DefaultExitID)
	cfg.SOCKS5Listen = strings.TrimSpace(cfg.SOCKS5Listen)
	cfg.HTTPListen = strings.TrimSpace(cfg.HTTPListen)
	if cfg.SOCKS5Listen == "" {
		cfg.SOCKS5Listen = "127.0.0.1:1080"
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = "127.0.0.1:8080"
	}
	cfg.VPNProxyListen = strings.TrimSpace(cfg.VPNProxyListen)
	if cfg.VPNProxyListen == "" {
		cfg.VPNProxyListen = "127.0.0.1:1081"
	}
	if !loopbackListenAddress(cfg.SOCKS5Listen) || !loopbackListenAddress(cfg.HTTPListen) ||
		!loopbackListenAddress(cfg.VPNProxyListen) {
		return cfg, errors.New("Android local proxy listeners must use a loopback address")
	}
	if cfg.VPNProxyEnabled {
		if strings.TrimSpace(cfg.VPNProxyToken) == "" {
			return cfg, errors.New("vpnProxyToken is required when the VPN proxy is enabled")
		}
		if *cfg.SOCKS5Enabled && cfg.SOCKS5Listen == cfg.VPNProxyListen {
			return cfg, errors.New("VPN and user SOCKS5 listeners must use different addresses")
		}
		if *cfg.HTTPEnabled && cfg.HTTPListen == cfg.VPNProxyListen {
			return cfg, errors.New("VPN SOCKS5 and user HTTP listeners must use different addresses")
		}
	}
	if !*cfg.TLSEnabled && cfg.TransportMode == string(tunnel.ModeQUICOnly) {
		return cfg, errors.New("quic_only requires TLS")
	}
	engine, err := routing.NewEngine(cfg.Routing)
	if err != nil {
		return cfg, fmt.Errorf("invalid routing config: %w", err)
	}
	cfg.Routing = engine.Config()
	return cfg, nil
}

func normalizeRequestedCapabilities(values []string) []string {
	out := make([]string, 0, 2)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != protocol.CapabilityProxyClient && value != protocol.CapabilityProxyExit {
			continue
		}
		if !contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func validIdentityID(value string) bool {
	if len(value) != 16 {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			hasLetter = true
		case char >= '0' && char <= '9':
			hasDigit = true
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

// NewClient creates an Android exit-node core. identityPath should point to the
// app-private files directory; RelayProxy stores its Ed25519 identity there.
func NewClient(configJSON, identityPath string) (*Client, error) {
	cfg, err := normalizeConfig(configJSON)
	if err != nil {
		return nil, err
	}
	identity, err := deviceidentity.LoadOrCreate(identityPath)
	if err != nil {
		return nil, fmt.Errorf("load device identity: %w", err)
	}
	checker, err := acl.NewChecker(acl.Policy{
		ID:                  "android_exit_policy",
		Name:                "Android Exit Policy",
		AllowInternet:       *cfg.AllowInternet,
		AllowPrivateNetwork: cfg.AllowPrivateNetwork,
		AllowLoopback:       cfg.AllowLoopback,
	})
	if err != nil {
		return nil, fmt.Errorf("build exit policy: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		cfg:      cfg,
		identity: identity,
		handler: exit.NewHandler(exit.HandlerConfig{
			ACLChecker: checker, ConnectTimeout: 10 * time.Second,
			ResumeEnabled: true, ResumeGrace: 15 * time.Second,
			ResumeMaxSessions: 64, ResumeReplayLimit: 256 << 10,
		}),
		ctx:    ctx,
		cancel: cancel,
		status: statusSnapshot{
			ConnectionState: string(tunnel.StateDisconnected),
			ApprovalState:   "unknown",
			DeviceName:      cfg.DeviceName,
			SelectedExit:    cfg.DefaultExitID,
			RoutingMode:     cfg.Routing.Mode,
		},
	}

	var tlsConfig *tls.Config
	plainTCP := !*cfg.TLSEnabled
	if !plainTCP {
		tlsConfig = &tls.Config{
			MinVersion:         tls.VersionTLS13,
			ServerName:         cfg.ServerAddress,
			InsecureSkipVerify: cfg.InsecureTLS,
		}
	}
	c.manager = tunnel.NewTunnelManager(tunnel.ManagerConfig{
		ServerAddress:  cfg.ServerAddress,
		QUICPort:       cfg.QUICPort,
		TCPPort:        cfg.TCPPort,
		Mode:           tunnel.Mode(cfg.TransportMode),
		TLSConfig:      tlsConfig,
		PlainTCP:       plainTCP,
		ConnectTimeout: 10 * time.Second,
	}, c.onTunnelStateChange)
	c.proxyDialer = agentclient.NewTunnelDialer(func() tunnel.TunnelSession {
		if !c.clientApproved.Load() {
			return nil
		}
		return c.manager.Session()
	}, func() string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.status.DeviceID
	})
	directMode := "relay_only"
	if *cfg.ProxyP2PEnabled {
		directMode = "auto"
	}
	c.proxyDialer.ConfigureDirectPolicy(directMode, true)
	c.proxyDialer.ConfigureStreamResume(*cfg.ProxyP2PEnabled, 512<<10)
	c.proxyDialer.ConfigureDirectPath(
		func(exitDeviceID string) (tunnel.TunnelSession, bool) {
			c.mu.RLock()
			manager := c.proxyP2P
			closed := c.closed
			c.mu.RUnlock()
			if closed || manager == nil || !c.clientApproved.Load() {
				return nil, false
			}
			return manager.ReadyForExit(exitDeviceID)
		},
		func(exitDeviceID string) {
			c.mu.RLock()
			manager := c.proxyP2P
			closed := c.closed
			c.mu.RUnlock()
			if !closed && manager != nil && c.clientApproved.Load() {
				manager.EnsureClient(exitDeviceID)
			}
		},
	)
	c.proxyDialer.ConfigureDirectMetrics(func(exitDeviceID string) {
		c.mu.RLock()
		manager := c.proxyP2P
		c.mu.RUnlock()
		if manager != nil {
			manager.NoteFallback(exitDeviceID)
		}
	})
	c.proxyDialer.ConfigureDirectFailure(func(exitDeviceID, reason string) {
		c.mu.RLock()
		manager := c.proxyP2P
		c.mu.RUnlock()
		if manager != nil {
			manager.FailReadyForExit(exitDeviceID, reason)
		}
	})
	c.proxyDialer.SetDefaultExitID(cfg.DefaultExitID)
	routingEngine, err := routing.NewEngine(cfg.Routing)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("build routing engine: %w", err)
	}
	c.traffic = traffic.NewRegistry(1024, 256)
	c.routingDialer = routing.NewRoutingDialer(routingEngine, c.proxyDialer)
	c.routingDialer.Traffic = c.traffic
	return c, nil
}

func loopbackListenAddress(addr string) bool {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	port, err := strconv.Atoi(portText)
	return ip != nil && ip.IsLoopback() && err == nil && port > 0 && port <= 65535
}

// Start begins connection and automatic reconnect. It returns immediately.
func (c *Client) Start() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("client is closed")
	}
	if c.started {
		c.mu.Unlock()
		return nil
	}
	if c.starting {
		c.mu.Unlock()
		return errors.New("client is already starting")
	}
	c.starting = true
	c.mu.Unlock()
	startSucceeded := false
	defer func() {
		if !startSucceeded {
			c.mu.Lock()
			c.starting = false
			c.mu.Unlock()
		}
	}()

	var clientDialer proxy.TunnelDialer = c.proxyDialer
	if c.routingDialer != nil {
		clientDialer = c.routingDialer
	}
	countedDialer := &proxyStatsDialer{base: clientDialer, owner: c}
	var socks *socks5.Server
	var vpnSocks *socks5.Server
	var http *httpproxy.Server
	if c.cfg.ClientEnabled && *c.cfg.SOCKS5Enabled {
		socks = socks5.NewServer(socks5.ServerConfig{
			ListenAddr: c.cfg.SOCKS5Listen, GetExitNodeID: c.proxyDialer.GetDefaultExitID, Dialer: countedDialer,
		})
		if err := socks.Start(); err != nil {
			c.setLastError(err)
			return fmt.Errorf("start SOCKS5 listener: %w", err)
		}
	}
	if c.cfg.ClientEnabled && c.cfg.VPNProxyEnabled {
		vpnSocks = socks5.NewServer(socks5.ServerConfig{
			ListenAddr: c.cfg.VPNProxyListen, GetExitNodeID: c.proxyDialer.GetDefaultExitID,
			Dialer: countedDialer, Authenticate: c.authenticateVPNProxy,
		})
		if err := vpnSocks.Start(); err != nil {
			if socks != nil {
				_ = socks.Close()
			}
			c.setLastError(err)
			return fmt.Errorf("start VPN SOCKS5 listener: %w", err)
		}
	}
	if c.cfg.ClientEnabled && *c.cfg.HTTPEnabled {
		http = httpproxy.NewServer(httpproxy.ServerConfig{
			ListenAddr: c.cfg.HTTPListen, GetExitNodeID: c.proxyDialer.GetDefaultExitID, Dialer: countedDialer,
		})
		if err := http.Start(); err != nil {
			if socks != nil {
				_ = socks.Close()
			}
			if vpnSocks != nil {
				_ = vpnSocks.Close()
			}
			c.setLastError(err)
			return fmt.Errorf("start HTTP proxy listener: %w", err)
		}
	}
	c.mu.Lock()
	if c.closed {
		c.starting = false
		c.mu.Unlock()
		if socks != nil {
			_ = socks.Close()
		}
		if vpnSocks != nil {
			_ = vpnSocks.Close()
		}
		if http != nil {
			_ = http.Close()
		}
		return errors.New("client is closed")
	}
	c.socksServer, c.vpnSocksServer, c.httpServer = socks, vpnSocks, http
	if c.cfg.ClientEnabled && socks == nil && vpnSocks == nil && http == nil {
		c.starting = false
		c.mu.Unlock()
		return errors.New("no Android local proxy listener is enabled")
	}
	c.starting = false
	c.started = true
	c.status.ConnectionState = string(tunnel.StateConnecting)
	c.status.LastError = ""
	c.wg.Add(1)
	c.mu.Unlock()
	startSucceeded = true

	c.manager.StartAutoReconnect()
	go func() {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(c.ctx, 20*time.Second)
		defer cancel()
		if _, err := c.manager.Connect(ctx); err != nil && c.ctx.Err() == nil {
			c.setLastError(err)
		}
	}()
	return nil
}

// Stop terminates the relay session and all active exit streams.
func (c *Client) Stop() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.status.ConnectionState = string(tunnel.StateClosed)
	c.clientApproved.Store(false)
	socksServer, vpnSocksServer, httpServer := c.socksServer, c.vpnSocksServer, c.httpServer
	c.socksServer, c.vpnSocksServer, c.httpServer = nil, nil, nil
	c.mu.Unlock()

	c.cancel()
	var proxyErr error
	if socksServer != nil {
		proxyErr = errors.Join(proxyErr, socksServer.Close())
	}
	if vpnSocksServer != nil {
		proxyErr = errors.Join(proxyErr, vpnSocksServer.Close())
	}
	if httpServer != nil {
		proxyErr = errors.Join(proxyErr, httpServer.Close())
	}
	managerErr := c.manager.Close()
	handlerErr := c.handler.Close()
	c.wg.Wait()
	return errors.Join(proxyErr, managerErr, handlerErr)
}

func (c *Client) authenticateVPNProxy(username, password string) (string, []string, bool) {
	if subtle.ConstantTimeCompare([]byte(password), []byte(c.cfg.VPNProxyToken)) != 1 {
		return "", nil, false
	}
	parts := strings.Split(username, "|")
	if len(parts) == 0 || len(parts) > 16 {
		return "", nil, false
	}
	packages := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != androidUnknownProcess && !validAndroidPackage(part) {
			return "", nil, false
		}
		if !contains(packages, part) {
			packages = append(packages, part)
		}
	}
	if len(packages) == 0 {
		return "", nil, false
	}
	return packages[0], packages[1:], true
}

func validAndroidPackage(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" || !((segment[0] >= 'a' && segment[0] <= 'z') || (segment[0] >= 'A' && segment[0] <= 'Z')) {
			return false
		}
		for _, char := range segment {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '_' {
				continue
			}
			return false
		}
	}
	return true
}

// SetRoutingConfig atomically changes rules for new flows. Existing streams
// retain their route and the relay/TUN runtime stays alive.
func (c *Client) SetRoutingConfig(configJSON string) error {
	var cfg routing.Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("decode routing config: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.routingDialer == nil {
		return errors.New("routing runtime is unavailable")
	}
	if err := c.routingDialer.Engine().Reload(cfg); err != nil {
		return err
	}
	c.cfg.Routing = c.routingDialer.Engine().Config()
	c.status.RoutingMode = c.cfg.Routing.Mode
	return nil
}

// SetDefaultExit selects the Relay exit used by new local proxy requests.
func (c *Client) SetDefaultExit(exitID string) {
	if c == nil || c.proxyDialer == nil {
		return
	}
	exitID = strings.TrimSpace(exitID)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.cfg.DefaultExitID = exitID
	selected := effectiveProxyExit(exitID, c.status.ProxyExits)
	state, statusError := proxySelectionStatus(exitID, c.status.ProxyExits)
	c.status.SelectedExit = selected
	c.status.ProxyState = state
	c.status.ProxyError = statusError
	exitID = selected
	manager := c.proxyP2P
	c.mu.Unlock()
	c.proxyDialer.SetDefaultExitID(exitID)
	if c.routingDialer != nil {
		c.routingDialer.SetDefaultExitID(exitID)
	}
	if exitID != "" && exitID != protocol.ServerExitDeviceID && manager != nil && c.clientApproved.Load() {
		manager.EnsureClient(exitID)
	}
}

// SetPowerConstrained switches Proxy P2P into its mobile battery-aware profile.
// It is safe to call before Start and while the Relay session is connected.
func (c *Client) SetPowerConstrained(constrained bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.powerConstrained = constrained
	manager := c.proxyP2P
	c.mu.Unlock()
	if manager != nil {
		manager.SetPowerConstrained(constrained)
	}
}

// StatusJSON returns a stable JSON snapshot for the Android UI.
func (c *Client) StatusJSON() string {
	c.mu.RLock()
	s := c.status
	s.PowerConstrained = c.powerConstrained
	manager := c.proxyP2P
	c.mu.RUnlock()
	s.ActiveStreams = c.handler.ActiveStreams()
	s.ProxyActiveTCP = c.proxyActiveTCP.Load()
	s.ProxyActiveUDP = c.proxyActiveUDP.Load()
	s.ProxyTCPFlows = c.proxyTCPFlows.Load()
	s.ProxyUDPFlows = c.proxyUDPFlows.Load()
	s.ProxyBytesUp = c.proxyBytesUp.Load()
	s.ProxyBytesDown = c.proxyBytesDown.Load()
	s.NativeUDP = tunnel.NativeUDPUsage()
	if manager != nil {
		if path, ok := manager.PathStatus(s.SelectedExit); ok {
			s.P2PState = string(path.State)
			s.P2PPath = path.Path
			s.P2PRTTMs = path.RTTMs
			s.P2PCandidateSummary = path.CandidateSummary
			s.P2PBytesUp = path.BytesUp
			s.P2PBytesDown = path.BytesDown
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return `{"connectionState":"ERROR","lastError":"status encoding failed"}`
	}
	return string(data)
}

func (c *Client) setLastError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.status.LastError = err.Error()
	}
	c.mu.Unlock()
}

func (c *Client) onTunnelStateChange(_, newState tunnel.State, sess tunnel.TunnelSession) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.status.ConnectionState = string(newState)
	if newState != tunnel.StateConnected {
		c.clientApproved.Store(false)
		c.status.ClientApproved = false
		c.status.ExitApproved = false
		c.status.ProxyState = "disconnected"
		c.status.SelectedExit = c.cfg.DefaultExitID
		c.proxyDialer.SetDefaultExitID(c.cfg.DefaultExitID)
		if len(c.status.ProxyExits) > 0 {
			exits := cloneProxyExits(c.status.ProxyExits)
			for index := range exits {
				exits[index].Online = false
			}
			c.status.ProxyExits = exits
		}
	}
	if sess != nil {
		c.status.Transport = string(sess.Transport())
	} else if newState != tunnel.StateConnected {
		c.status.Transport = ""
		c.status.ExitApproved = false
	}
	shouldServe := newState == tunnel.StateConnected && sess != nil
	if shouldServe {
		c.wg.Add(1)
	}
	c.mu.Unlock()

	if !shouldServe {
		return
	}

	go func() {
		defer c.wg.Done()
		err := c.serveSession(sess)
		if err != nil && c.ctx.Err() == nil {
			c.setLastError(err)
		}
		c.manager.MarkSessionLost(sess)
	}()
}

func (c *Client) serveSession(sess tunnel.TunnelSession) error {
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stopClose()
	defer sess.Close()

	deadline := time.Now().Add(10 * time.Second)
	openCtx, cancelOpen := context.WithDeadline(ctx, deadline)
	ctrl, err := sess.OpenStream(openCtx)
	cancelOpen()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}
	defer ctrl.Close()
	if err := ctrl.SetDeadline(deadline); err != nil {
		return err
	}
	if err := protocol.WriteStreamHeader(ctrl, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeControl,
	}); err != nil {
		return fmt.Errorf("write control header: %w", err)
	}

	transportCaps := []string{
		"tcp", protocol.UDPModeStream, protocol.CapabilityTargetACL,
		protocol.CapabilityRuntimeState,
	}
	if c.cfg.ClientEnabled {
		transportCaps = append(transportCaps, protocol.CapabilityProxyClientActive)
	}
	if *c.cfg.ExitEnabled {
		transportCaps = append(transportCaps, protocol.CapabilityProxyExitActive)
	}
	if *c.cfg.ProxyP2PEnabled && (*c.cfg.ExitEnabled || c.cfg.ClientEnabled) {
		transportCaps = append(transportCaps, protocol.CapabilityProxyP2P, protocol.CapabilityProxyStreamResume)
	}
	if *c.cfg.TLSEnabled {
		transportCaps = append(transportCaps, "tls", "quic")
	}
	if tunnel.SupportsDatagrams(sess) {
		transportCaps = append(transportCaps, protocol.UDPModeDatagram)
	}

	clientNonce := make([]byte, 32)
	if _, err := rand.Read(clientNonce); err != nil {
		return fmt.Errorf("generate client nonce: %w", err)
	}
	hello := protocol.DeviceHello{
		ProtocolVersion:       protocol.IdentityDeviceProtocolVersion,
		IdentityID:            c.cfg.IdentityID,
		InstallationID:        c.identity.InstallationID,
		PublicKey:             append([]byte(nil), c.identity.PublicKey...),
		ClientNonce:           clientNonce,
		DeviceName:            c.cfg.DeviceName,
		Platform:              runtime.GOOS,
		Arch:                  runtime.GOARCH,
		ClientVersion:         clientVersion,
		RequestedCapabilities: c.requestedCapabilities(),
		TransportCapabilities: transportCaps,
	}
	if err := protocol.WriteJSON(ctrl, hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(ctrl, &challenge); err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}
	if challenge.ProtocolVersion != protocol.IdentityDeviceProtocolVersion ||
		challenge.ChallengeID == "" ||
		challenge.ServerInstanceID == "" ||
		len(challenge.ServerNonce) != 32 ||
		time.Now().Unix() > challenge.ExpiresAt {
		return errors.New("server returned an invalid authentication challenge")
	}
	proof := protocol.AuthProof{
		ChallengeID: challenge.ChallengeID,
		Signature:   c.identity.Sign(protocol.DeviceAuthPayload(hello, challenge)),
	}
	if err := protocol.WriteJSON(ctrl, proof); err != nil {
		return fmt.Errorf("send authentication proof: %w", err)
	}

	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(ctrl, &accepted); err != nil {
		return fmt.Errorf("read device approval: %w", err)
	}
	c.mu.Lock()
	c.status.ApprovalState = accepted.State
	if accepted.Success {
		c.status.IdentityName = accepted.IdentityName
		c.status.PolicyRevision = accepted.PolicyRevision
	}
	c.mu.Unlock()
	if !accepted.Success {
		return fmt.Errorf("server rejected connection: [%s] %s", accepted.ErrorCode, accepted.ErrorMessage)
	}
	if accepted.State != "approved" || accepted.DeviceID == "" {
		return errors.New("server returned an incomplete device approval")
	}
	tunnel.SetPeerCapabilities(sess, accepted.TransportCapabilities)
	if err := ctrl.SetDeadline(time.Time{}); err != nil {
		return err
	}

	exitApproved := contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyExit)
	clientApproved := contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyClient)
	exitRuntimeApproved := *c.cfg.ExitEnabled && exitApproved
	clientRuntimeApproved := c.cfg.ClientEnabled && clientApproved
	c.mu.RLock()
	configuredExit := c.cfg.DefaultExitID
	c.mu.RUnlock()
	selectedExit := configuredExit
	var acceptedExits []protocol.ProxyExit
	proxyState, proxyError := "legacy", ""
	if accepted.ProxyExits != nil {
		acceptedExits = cloneProxyExits(*accepted.ProxyExits)
		selectedExit = effectiveProxyExit(selectedExit, acceptedExits)
		proxyState, proxyError = proxySelectionStatus(configuredExit, acceptedExits)
	}
	if !clientApproved {
		proxyState, proxyError = "not_authorized", "代理客户端尚未获得服务端授权"
	}
	c.mu.Lock()
	if c.closed || c.manager.Session() != sess {
		c.mu.Unlock()
		return errors.New("session superseded during authentication")
	}
	c.status.DeviceID = accepted.DeviceID
	c.status.ApprovalState = accepted.State
	c.status.ExitApproved = exitApproved
	c.status.ClientApproved = clientApproved
	c.status.ProxyState = proxyState
	c.status.ProxyError = proxyError
	if accepted.ProxyExits != nil {
		c.status.ProxyExits = acceptedExits
		c.status.SelectedExit = selectedExit
	}
	c.status.ConnectionState = string(tunnel.StateConnected)
	c.status.Transport = string(sess.Transport())
	c.status.LastError = ""
	powerConstrained := c.powerConstrained
	c.mu.Unlock()
	c.clientApproved.Store(clientRuntimeApproved)
	if accepted.ProxyExits != nil {
		c.proxyDialer.SetDefaultExitID(selectedExit)
	}

	var proxyP2PManager *proxyp2p.Manager
	if *c.cfg.ProxyP2PEnabled && (exitRuntimeApproved || clientRuntimeApproved) && contains(accepted.TransportCapabilities, protocol.CapabilityProxyP2P) {
		lease := time.Duration(accepted.P2PLeaseSec) * time.Second
		if lease <= 0 {
			lease = 60 * time.Second
		}
		proxyP2PManager = proxyp2p.NewQUICManagerWithOptions(
			ctx,
			func(controlCtx context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
				return c.sendP2PControlRequest(controlCtx, sess, message)
			},
			accepted.P2PRendezvousAddress,
			lease,
			proxyp2p.QUICManagerOptions{
				PunchTimeout:        1200 * time.Millisecond,
				KeepAlive:           10 * time.Second,
				IdleTimeout:         120 * time.Second,
				MaxExitSessions:     1,
				PortStart:           accepted.P2PPortStart,
				PortEnd:             accepted.P2PPortEnd,
				LowPowerIdleTimeout: 60 * time.Second,
				LowPowerMaxSessions: 1,
			},
		)
		proxyP2PManager.SetPowerConstrained(powerConstrained)
		c.mu.Lock()
		if !c.closed && c.manager.Session() == sess {
			c.proxyP2P = proxyP2PManager
		} else {
			c.mu.Unlock()
			_ = proxyP2PManager.Close()
			return errors.New("session superseded while enabling proxy P2P")
		}
		c.mu.Unlock()
		selected := strings.TrimSpace(c.proxyDialer.GetDefaultExitID())
		if clientRuntimeApproved && selected != "" && selected != protocol.ServerExitDeviceID {
			proxyP2PManager.EnsureClient(selected)
		}
		defer func() {
			c.mu.Lock()
			if c.proxyP2P == proxyP2PManager {
				c.proxyP2P = nil
			}
			c.mu.Unlock()
			_ = proxyP2PManager.Close()
		}()
	}

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		c.heartbeatLoop(ctx, ctrl, sess, accepted.HeartbeatSec)
	}()
	if exitRuntimeApproved || proxyP2PManager != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			c.acceptIncomingStreams(ctx, sess, accepted.MaxConnections, exitRuntimeApproved, proxyP2PManager, &workers)
		}()
		if exitRuntimeApproved && proxyP2PManager != nil {
			workers.Add(1)
			go func() {
				defer workers.Done()
				c.serveProxyP2PExit(ctx, proxyP2PManager, accepted.MaxConnections)
			}()
		}
	}

	select {
	case <-ctx.Done():
	case <-sess.Done():
	}
	cancel()
	_ = sess.Close()
	workers.Wait()
	return nil
}

func (c *Client) requestedCapabilities() []string {
	return append([]string(nil), c.cfg.RequestedCapabilities...)
}

func cloneProxyExits(exits []protocol.ProxyExit) []protocol.ProxyExit {
	cloned := make([]protocol.ProxyExit, len(exits))
	copy(cloned, exits)
	return cloned
}

// sendP2PControlRequest keeps rendezvous, lease and authorization signaling on
// the authenticated Relay tunnel. Direct QUIC is data-plane only.
func (c *Client) sendP2PControlRequest(ctx context.Context, sess tunnel.TunnelSession, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
	if sess == nil {
		return protocol.P2PControlMessage{}, errors.New("relay session is unavailable")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return protocol.P2PControlMessage{}, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeP2PControl,
		RequestID: fmt.Sprintf("android_p2p_ctl_%d", time.Now().UnixNano()), ExitDeviceID: message.ExitDeviceID,
	}); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	if err := protocol.WriteJSON(stream, message); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	var response protocol.P2PControlMessage
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return protocol.P2PControlMessage{}, err
	}
	return response, nil
}

func (c *Client) heartbeatLoop(ctx context.Context, ctrl tunnel.TunnelStream, sess tunnel.TunnelSession, heartbeatSec int) {
	if heartbeatSec <= 0 {
		heartbeatSec = 15
	}
	ticker := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			return
		case <-ticker.C:
			start := time.Now()
			_ = ctrl.SetDeadline(start.Add(5 * time.Second))
			err := protocol.WriteJSON(ctrl, protocol.PingMessage{Timestamp: start.UnixMilli()})
			var pong protocol.PongMessage
			if err == nil {
				err = protocol.ReadJSON(ctrl, &pong)
			}
			if err != nil {
				c.setLastError(fmt.Errorf("heartbeat failed: %w", err))
				_ = sess.Close()
				return
			}
			_ = ctrl.SetDeadline(time.Time{})
			c.mu.Lock()
			if !c.closed && c.manager.Session() == sess {
				c.status.LatencyMs = time.Since(start).Milliseconds()
				if pong.ProxyExits != nil {
					exits := cloneProxyExits(*pong.ProxyExits)
					selected := effectiveProxyExit(c.cfg.DefaultExitID, exits)
					proxyState, proxyError := proxySelectionStatus(c.cfg.DefaultExitID, exits)
					c.status.ProxyExits = exits
					c.status.SelectedExit = selected
					c.status.ProxyState = proxyState
					c.status.ProxyError = proxyError
					c.proxyDialer.SetDefaultExitID(selected)
				}
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) acceptIncomingStreams(ctx context.Context, sess tunnel.TunnelSession, maxConnections int, exitApproved bool, p2pManager *proxyp2p.Manager, workers *sync.WaitGroup) {
	if maxConnections <= 0 {
		maxConnections = 256
	}
	if maxConnections > 2048 {
		maxConnections = 2048
	}
	admission := make(chan struct{}, maxConnections)

	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case admission <- struct{}{}:
		case <-ctx.Done():
			_ = stream.Close()
			return
		}

		workers.Add(1)
		go func(s tunnel.TunnelStream) {
			defer workers.Done()
			defer func() { <-admission }()
			_ = s.SetDeadline(time.Now().Add(15 * time.Second))
			header, err := protocol.ReadStreamHeader(s)
			if err != nil {
				_ = s.Close()
				return
			}
			if header.Type == protocol.FrameTypeP2PControl && p2pManager != nil {
				defer s.Close()
				var message protocol.P2PControlMessage
				if err := protocol.ReadJSON(s, &message); err == nil {
					p2pManager.HandleControl(message)
				}
				return
			}
			switch header.Type {
			case protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP:
				if exitApproved {
					c.handler.HandleStreamWithHeader(ctx, s, header)
				} else {
					_ = s.Close()
				}
			default:
				_ = s.Close()
			}
		}(stream)
	}
}

func (c *Client) serveProxyP2PExit(ctx context.Context, manager *proxyp2p.Manager, maxStreams int) {
	if manager == nil {
		return
	}
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case p2pSession := <-manager.ReadySessions():
			if p2pSession == nil {
				continue
			}
			policy := p2pSession.RelayPolicy()
			if policy == nil {
				continue
			}
			direct, ok := p2pSession.Tunnel()
			if !ok || direct == nil {
				continue
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				c.acceptProxyP2PExitSession(ctx, direct, policy, maxStreams)
			}()
		}
	}
}

func (c *Client) acceptProxyP2PExitSession(ctx context.Context, sess tunnel.TunnelSession, relayPolicy *acl.Policy, maxStreams int) {
	if sess == nil || relayPolicy == nil {
		return
	}
	if maxStreams <= 0 {
		maxStreams = 256
	}
	if maxStreams > 2048 {
		maxStreams = 2048
	}
	admission := make(chan struct{}, maxStreams)
	boundCtx := exit.BindRelayPolicy(ctx, relayPolicy)
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case admission <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		header, err := protocol.ReadStreamHeader(stream)
		if err != nil || (header.Type != protocol.FrameTypeOpenTCP && header.Type != protocol.FrameTypeOpenUDP) {
			<-admission
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func(s tunnel.TunnelStream, h *protocol.StreamHeader) {
			defer workers.Done()
			defer func() { <-admission }()
			c.handler.HandleStreamWithHeader(boundCtx, s, h)
		}(stream, header)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func effectiveProxyExit(configured string, exits []protocol.ProxyExit) string {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		return configured
	}
	selected := ""
	for _, item := range exits {
		if !item.Online || strings.TrimSpace(item.DeviceID) == "" {
			continue
		}
		if selected != "" {
			return ""
		}
		selected = item.DeviceID
	}
	return selected
}

func proxySelectionStatus(configured string, exits []protocol.ProxyExit) (string, string) {
	configured = strings.TrimSpace(configured)
	online := make([]protocol.ProxyExit, 0, len(exits))
	for _, item := range exits {
		if item.Online && strings.TrimSpace(item.DeviceID) != "" {
			online = append(online, item)
		}
	}
	if configured != "" {
		for _, item := range online {
			if item.DeviceID == configured {
				return "ready", ""
			}
		}
		return "exit_unavailable", "首选代理出口当前离线或授权已撤销"
	}
	switch len(online) {
	case 0:
		return "no_exit", "没有可用且已授权的在线出口"
	case 1:
		return "ready", ""
	default:
		return "exit_required", "存在多个可用出口，请在设置中选择一个"
	}
}

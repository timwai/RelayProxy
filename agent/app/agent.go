package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/agent/client"
	"relayproxy/agent/divert"
	"relayproxy/agent/exit"
	proxyp2p "relayproxy/agent/p2p"
	"relayproxy/agent/rdp"
	rdpp2p "relayproxy/agent/rdp/p2p"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy/httpproxy"
	"relayproxy/internal/proxy/socks5"
	"relayproxy/internal/traffic"
	"relayproxy/internal/tunnel"
)

// LogEntry represents a captured log line for UI display
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
}

type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	maxSize int
	tap     func(LogEntry)
}

func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &LogBuffer{maxSize: maxSize}
}

func (b *LogBuffer) Write(p []byte) (n int, err error) {
	b.Add(string(p))
	return len(p), nil
}

func (b *LogBuffer) Add(msg string) {
	msg = strings.TrimRight(msg, "\r\n")
	if msg == "" {
		return
	}
	entry := LogEntry{
		Timestamp: time.Now().Format("15:04:05"),
		Message:   msg,
	}
	b.mu.Lock()
	if len(b.entries) >= b.maxSize {
		b.entries = b.entries[1:]
	}
	b.entries = append(b.entries, entry)
	tap := b.tap
	b.mu.Unlock()

	// Deliver outside the lock: the tap fans out to the WebView2 UI and must
	// never block a logging goroutine on a mutex held by a reader.
	if tap != nil {
		tap(entry)
	}
}

// SetTap registers a listener that receives every log entry as it is added.
// Passing nil removes the current listener.
func (b *LogBuffer) SetTap(tap func(LogEntry)) {
	b.mu.Lock()
	b.tap = tap
	b.mu.Unlock()
}

// Clear drops all buffered entries.
func (b *LogBuffer) Clear() {
	b.mu.Lock()
	b.entries = nil
	b.mu.Unlock()
}

func (b *LogBuffer) Get(limit int) []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > len(b.entries) {
		limit = len(b.entries)
	}
	start := len(b.entries) - limit
	res := make([]LogEntry, limit)
	copy(res, b.entries[start:])
	return res
}

// GlobalLogBuffer captures logs for the UI
var GlobalLogBuffer = NewLogBuffer(1000)

func init() {
	// Tee log output to the in-memory ring buffer first and the console second.
	// The console writer must be best-effort: in a -H=windowsgui build stdout is
	// not attached, and an io.MultiWriter would stop at that first error.
	log.SetOutput(io.MultiWriter(GlobalLogBuffer, BestEffort(os.Stdout)))
}

type AgentConfig struct {
	Identity              *deviceidentity.Identity
	IdentityID            string
	DeviceID              string
	DeviceName            string
	ServerAddress         string
	QUICPort              int
	TCPPort               int
	Mode                  string // "CLIENT", "EXIT", "BOTH"
	TransportMode         string // "auto", "quic_only", "tcp_only"
	SOCKS5Enabled         *bool
	SOCKS5Listen          string // "127.0.0.1:1080"
	HTTPEnabled           *bool
	HTTPListen            string // "127.0.0.1:8080"
	DefaultExitID         string
	ExitEnabled           *bool
	ExitUpstream          exit.UpstreamConfig
	RDPEnabled            *bool
	RDPAddress            string // target-local RDP service, default 127.0.0.1:3389
	P2PEnabled            *bool
	P2PMode               string
	P2PPunchTimeout       time.Duration
	P2PKeepalive          time.Duration
	P2PIdleTimeout        time.Duration
	P2PMaxSessions        int
	P2PFallback           *bool
	PublicDirectAdvertise string
	AllowInternet         bool
	AllowPrivateNet       bool
	AllowLoopback         bool     // Allow localhost/loopback for testing
	AccessMode            string   // "" (no gate), "allow" (whitelist) or "deny" (blacklist)
	AccessDomains         []string // domain patterns for the access list (glob / .suffix / exact)
	AccessCIDRs           []string // IP ranges for the access list (CIDR / single IP / start-end)
	NetworkMode           string   // "" (off) | "divert"
	DivertConfig          divert.Config
	Routing               routing.Config
	InsecureTLS           bool // Allow self-signed TLS certificates for development/testing
	PlainTCP              bool // Disable TLS entirely; connect via plaintext TCP + yamux
	ConnectTimeout        time.Duration
}

func (c AgentConfig) IsSOCKS5Enabled() bool {
	if c.SOCKS5Enabled != nil {
		return *c.SOCKS5Enabled
	}
	return true
}

func (c AgentConfig) IsHTTPEnabled() bool {
	if c.HTTPEnabled != nil {
		return *c.HTTPEnabled
	}
	return true
}

func (c AgentConfig) IsExitEnabled() bool {
	if c.ExitEnabled != nil {
		return *c.ExitEnabled
	}
	return true
}

func (c AgentConfig) IsRDPEnabled() bool {
	if c.RDPEnabled != nil {
		return *c.RDPEnabled
	}
	return true
}

func (c AgentConfig) IsP2PEnabled() bool {
	if c.P2PEnabled != nil {
		return *c.P2PEnabled
	}
	return true
}

func (c AgentConfig) IsP2PFallbackEnabled() bool {
	if c.P2PFallback != nil {
		return *c.P2PFallback
	}
	return true
}

type ProxyExitSummary struct {
	DeviceID            string `json:"deviceId"`
	Name                string `json:"name"`
	IdentityName        string `json:"identityName,omitempty"`
	AuthorizationSource string `json:"authorizationSource,omitempty"`
	Online              bool   `json:"online"`
}

type AgentStatus struct {
	Connected           bool                       `json:"connected"`
	Transport           string                     `json:"transport"`
	TunnelDiagnostics   *tunnel.SessionDiagnostics `json:"tunnelDiagnostics,omitempty"`
	LatencyMs           int64                      `json:"latency"`
	DeviceID            string                     `json:"deviceId"`
	DeviceName          string                     `json:"deviceName"`
	IdentityName        string                     `json:"identityName,omitempty"`
	PolicyRevision      int64                      `json:"policyRevision,omitempty"`
	Mode                string                     `json:"mode"`
	SelectedExit        string                     `json:"selectedExit"`
	ProxyExits          []ProxyExitSummary         `json:"proxyExits"`
	ProxyExitRevision   uint64                     `json:"proxyExitRevision,omitempty"`
	SOCKS5Running       bool                       `json:"socks5Running"`
	HTTPRunning         bool                       `json:"httpRunning"`
	ExitRunning         bool                       `json:"exitRunning"`
	NetworkMode         string                     `json:"networkMode"`
	DivertRunning       bool                       `json:"divertRunning"`
	DivertStage         string                     `json:"divertStage"`
	DivertError         string                     `json:"divertError,omitempty"`
	DivertDiagnostics   divert.Diagnostics         `json:"divertDiagnostics"`
	ActiveStreams       int64                      `json:"activeStreams"`
	ApprovalState       string                     `json:"approvalState"`
	RDPListenAddr       string                     `json:"rdpListenAddr,omitempty"`
	RDPTargetID         string                     `json:"rdpTargetId,omitempty"`
	RDPUDPEnabled       bool                       `json:"rdpUdpEnabled"`
	RDPUDPActive        bool                       `json:"rdpUdpActive"`
	RDPUDPReason        string                     `json:"rdpUdpReason,omitempty"`
	RDPPathTCP          string                     `json:"rdpPathTcp,omitempty"`
	RDPPathUDP          string                     `json:"rdpPathUdp,omitempty"`
	DirectState         string                     `json:"directState,omitempty"`
	DirectPath          string                     `json:"directPath,omitempty"`
	DirectError         string                     `json:"directError,omitempty"`
	DirectEndpoint      string                     `json:"directEndpoint,omitempty"`
	DirectRTTMs         int64                      `json:"directRttMs,omitempty"`
	DirectFallbackCount uint64                     `json:"directFallbackCount,omitempty"`
	DirectBytesUp       uint64                     `json:"directBytesUp,omitempty"`
	DirectBytesDown     uint64                     `json:"directBytesDown,omitempty"`
	DirectQUIC          *tunnel.QUICDiagnostics    `json:"directQuic,omitempty"`
	P2PState            string                     `json:"p2pState,omitempty"`
	P2PPath             string                     `json:"p2pPath,omitempty"`
	P2PError            string                     `json:"p2pError,omitempty"`
	P2PSessionID        uint64                     `json:"p2pSessionId,omitempty"`
	P2PExitID           string                     `json:"p2pExitId,omitempty"`
	P2PRTTMs            int64                      `json:"p2pRttMs,omitempty"`
	P2PCandidateSummary string                     `json:"p2pCandidateSummary,omitempty"`
	P2PFallbackCount    uint64                     `json:"p2pFallbackCount,omitempty"`
	P2PBytesUp          uint64                     `json:"p2pBytesUp,omitempty"`
	P2PBytesDown        uint64                     `json:"p2pBytesDown,omitempty"`
	NativeUDP           tunnel.DatagramUsage       `json:"nativeUdp"`
}

// ErrRestartRequired means a saved startup setting has not changed the running
// agent. In particular, a role change must never reuse the previous exit handler.
var ErrRestartRequired = errors.New("agent role change requires restart")

type Agent struct {
	cfg               AgentConfig
	tunnelMgr         *tunnel.TunnelManager
	dialer            *routing.RoutingDialer
	rawDialer         *client.TunnelDialer
	routingEngine     *routing.Engine
	traffic           *traffic.Registry
	messages          *MessageBuffer
	exitHandler       *exit.Handler
	socksServer       *socks5.Server
	httpServer        *httpproxy.Server
	divertSrv         *divert.Server
	divertStage       atomic.Pointer[string]
	divertError       atomic.Pointer[string]
	ctrlStream        tunnel.TunnelStream
	readySession      tunnel.TunnelSession
	epoch             uint64
	started           bool
	selectedExit      atomic.Pointer[string]
	latencyMs         atomic.Int64
	handshakeOK       atomic.Bool
	approvalState     atomic.Pointer[string]
	approvedMode      string
	identityName      string
	policyRevision    int64
	proxyExits        []protocol.ProxyExit
	proxyExitRevision uint64
	rdpTargets        []rdp.Target
	rdpConnection     *rdp.Connection
	rdpP2P            *rdpp2p.Manager
	rdpSession        *rdpp2p.Session
	proxyP2P          *proxyp2p.Manager
	proxyDirect       *publicDirectClientManager
	closed            atomic.Bool
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	mu                sync.RWMutex
	policyMu          sync.RWMutex
	lifecycleMu       sync.Mutex
	closeOnce         sync.Once
	closeErr          error
	rdpConnectMu      sync.Mutex
}

func (a *Agent) setDivertStage(stage string, err error) {
	stageValue := strings.TrimSpace(stage)
	a.divertStage.Store(&stageValue)
	if err == nil {
		a.divertError.Store(nil)
		return
	}
	message := err.Error()
	a.divertError.Store(&message)
	log.Printf("[divert] stage=%s error=%v", stageValue, err)
}

func NewAgent(cfg AgentConfig) (*Agent, error) {
	cfg = cloneAgentConfig(cfg)
	if cfg.Identity == nil {
		var err error
		cfg.Identity, err = deviceidentity.Generate()
		if err != nil {
			return nil, err
		}
	}
	if len(cfg.Identity.PublicKey) == 0 || len(cfg.Identity.PrivateKey) == 0 || cfg.Identity.InstallationID == "" {
		return nil, errors.New("device identity is incomplete")
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = "Relay-Agent"
	}
	cfg.Mode = strings.ToUpper(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = "CLIENT"
	}
	switch cfg.Mode {
	case "CLIENT", "EXIT", "BOTH":
	default:
		return nil, fmt.Errorf("invalid agent mode %q", cfg.Mode)
	}
	if cfg.TransportMode == "" {
		cfg.TransportMode = "auto"
	}
	switch tunnel.Mode(cfg.TransportMode) {
	case tunnel.ModeAuto, tunnel.ModeQUICOnly, tunnel.ModeTCPOnly:
	default:
		return nil, fmt.Errorf("invalid transport mode %q", cfg.TransportMode)
	}
	cfg.NetworkMode = strings.ToLower(strings.TrimSpace(cfg.NetworkMode))
	if cfg.NetworkMode != "" && cfg.NetworkMode != "divert" {
		return nil, fmt.Errorf("unsupported network mode %q", cfg.NetworkMode)
	}
	if cfg.SOCKS5Listen == "" {
		cfg.SOCKS5Listen = "127.0.0.1:1080"
	}
	if cfg.HTTPListen == "" {
		cfg.HTTPListen = "127.0.0.1:8080"
	}
	if strings.TrimSpace(cfg.RDPAddress) == "" {
		cfg.RDPAddress = "127.0.0.1:3389"
	}
	cfg.P2PMode = strings.ToLower(strings.TrimSpace(cfg.P2PMode))
	if cfg.P2PMode == "" {
		cfg.P2PMode = "auto"
	}
	switch cfg.P2PMode {
	case "auto", "direct_only", "relay_only", "p2p_only":
	default:
		return nil, fmt.Errorf("invalid P2P mode %q", cfg.P2PMode)
	}
	if cfg.P2PPunchTimeout <= 0 {
		cfg.P2PPunchTimeout = 1200 * time.Millisecond
	}
	if cfg.P2PKeepalive <= 0 {
		cfg.P2PKeepalive = 10 * time.Second
	}
	if cfg.P2PIdleTimeout <= 0 {
		cfg.P2PIdleTimeout = 120 * time.Second
	}
	if cfg.P2PMaxSessions <= 0 {
		cfg.P2PMaxSessions = 4
	}
	if cfg.ConnectTimeout < 0 {
		return nil, errors.New("connect timeout must not be negative")
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	cfg.ExitUpstream = exit.NormalizeUpstreamConfig(cfg.ExitUpstream)
	if err := exit.ValidateUpstreamConfig(cfg.ExitUpstream); err != nil {
		return nil, fmt.Errorf("invalid exit upstream: %w", err)
	}

	engine, err := routing.NewEngine(cfg.Routing)
	if err != nil {
		return nil, err
	}
	cfg.Routing = engine.Config()
	cfg.DivertConfig.Mode = cfg.NetworkMode
	if err := divert.ValidateConfig(cfg.DivertConfig); err != nil {
		return nil, err
	}
	// Compile the pending exit policy even in CLIENT mode. A bad ACL is never
	// interpreted as an absent checker or deferred until after pairing.
	checker, err := acl.NewChecker(acl.Policy{
		ID: "default_exit_policy", Name: "Default Exit Policy",
		AllowInternet: cfg.AllowInternet, AllowPrivateNetwork: cfg.AllowPrivateNet,
		AllowLoopback: cfg.AllowLoopback,
		AccessMode:    acl.AccessMode(cfg.AccessMode),
		AccessHosts:   cfg.AccessDomains, AccessCIDRs: cfg.AccessCIDRs,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid exit ACL: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{cfg: cfg, ctx: ctx, cancel: cancel, routingEngine: engine, traffic: traffic.NewRegistry(0, 0), messages: NewMessageBuffer(defaultMessageHistorySize)}
	a.setDivertStage("disabled", nil)
	a.initPublicDirectClient()
	a.rawDialer = client.NewTunnelDialer(func() tunnel.TunnelSession {
		a.mu.RLock()
		defer a.mu.RUnlock()
		if a.closed.Load() {
			return nil
		}
		return a.readySession
	}, func() string {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.cfg.DeviceID
	})
	a.rawDialer.ConfigureDirectPolicy(cfg.P2PMode, cfg.IsP2PFallbackEnabled())
	resumeClient := cfg.IsP2PEnabled() && cfg.P2PMode != "relay_only" && cfg.P2PMode != "p2p_only" && cfg.IsP2PFallbackEnabled()
	a.rawDialer.ConfigureStreamResume(resumeClient, 512<<10)
	a.configureProxyPathProvider()
	a.dialer = routing.NewRoutingDialer(engine, a.rawDialer, &a.policyMu)
	a.dialer.Traffic, a.dialer.LookupProcess = a.traffic, divert.LookupLocalProcess
	a.SelectExit(cfg.DefaultExitID)
	if (cfg.Mode == "EXIT" || cfg.Mode == "BOTH") && cfg.IsExitEnabled() {
		a.exitHandler = exit.NewHandler(exit.HandlerConfig{
			ACLChecker: checker, ConnectTimeout: cfg.ConnectTimeout, Upstream: cfg.ExitUpstream,
			ResumeEnabled: cfg.IsP2PEnabled() && cfg.P2PMode != "relay_only",
			ResumeGrace:   15 * time.Second, ResumeMaxSessions: 128, ResumeReplayLimit: 512 << 10,
		})
	}
	var tunnelTLS *tls.Config
	if !cfg.PlainTCP {
		tunnelTLS = &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: cfg.InsecureTLS, ServerName: cfg.ServerAddress}
	}
	a.tunnelMgr = tunnel.NewTunnelManager(tunnel.ManagerConfig{
		ServerAddress: cfg.ServerAddress, QUICPort: cfg.QUICPort, TCPPort: cfg.TCPPort,
		Mode: tunnel.Mode(cfg.TransportMode), ConnectTimeout: cfg.ConnectTimeout,
		TLSConfig: tunnelTLS, PlainTCP: cfg.PlainTCP,
	}, a.onTunnelStateChange)

	if (cfg.Mode == "CLIENT" || cfg.Mode == "BOTH") && cfg.NetworkMode == "divert" {
		guard := divert.LoopGuard{RelayHost: cfg.ServerAddress, RelayPorts: []int{cfg.QUICPort, cfg.TCPPort}}
		if cfg.IsSOCKS5Enabled() {
			guard.LocalProxy = append(guard.LocalProxy, cfg.SOCKS5Listen)
		}
		if cfg.IsHTTPEnabled() {
			guard.LocalProxy = append(guard.LocalProxy, cfg.HTTPListen)
		}
		a.divertSrv, err = divert.New(divert.Options{
			Config: cfg.DivertConfig, Dialer: a.rawDialer, Guard: guard, PolicyMu: &a.policyMu,
			Traffic: a.traffic, DefaultExitID: a.rawDialer.GetDefaultExitID,
			ProxyReady: func() bool {
				return a.handshakeOK.Load()
			},
			SharedPolicy: func(flow divert.Flow) divert.Decision {
				d := engine.DecideFlow(routing.Flow{Process: flow.Process, ProcessAliases: flow.ProcessAliases, Host: flow.Host, IP: flow.IP, Port: flow.Port, Protocol: string(flow.Protocol)})
				return divert.Decision{Action: divert.Action(d.Action), ExitID: d.ExitID, Rule: d.Rule, DatagramRequired: d.DatagramRequired, HandleDirect: d.HandleDirect}
			},
		})
		if err != nil {
			cancel()
			_ = a.tunnelMgr.Close()
			return nil, fmt.Errorf("invalid divert configuration: %w", err)
		}
		a.setDivertStage("waiting_relay", nil)
	}
	return a, nil
}

func (a *Agent) onTunnelStateChange(oldState, newState tunnel.State, sess tunnel.TunnelSession) {
	log.Printf("[Agent] Tunnel state change: %s -> %s", oldState, newState)
	// A late callback from a replaced transport cannot clear the current Ready state.
	if a.tunnelMgr == nil || a.tunnelMgr.State() != newState || a.tunnelMgr.Session() != sess {
		return
	}
	a.mu.Lock()
	if a.closed.Load() || a.tunnelMgr.State() != newState || a.tunnelMgr.Session() != sess {
		a.mu.Unlock()
		return
	}
	a.epoch++
	epoch := a.epoch
	a.handshakeOK.Store(false)
	a.readySession = nil
	a.approvedMode = ""
	oldControl := a.ctrlStream
	oldRDP := a.rdpConnection
	oldP2P := a.rdpP2P
	oldProxyP2P := a.proxyP2P
	a.ctrlStream = nil
	a.rdpConnection = nil
	a.rdpP2P = nil
	a.proxyP2P = nil
	a.rdpSession = nil
	a.rdpTargets = nil
	// Keep the last authoritative exit inventory across transient transport
	// reconnects. The UI marks it stale while disconnected, and the next
	// Welcome/heartbeat/push replaces it. Clearing here caused visible list
	// flicker and could leave startup/reconnect screens empty for a full
	// heartbeat interval.
	if newState != tunnel.StateConnected || sess == nil {
		a.mu.Unlock()
		if oldControl != nil {
			_ = oldControl.Close()
		}
		if oldRDP != nil {
			_ = oldRDP.Close()
		}
		if oldP2P != nil {
			_ = oldP2P.Close()
		}
		if oldProxyP2P != nil {
			_ = oldProxyP2P.Close()
		}
		return
	}
	cfg, handler := cloneAgentConfig(a.cfg), a.exitHandler
	a.wg.Add(1)
	a.mu.Unlock()
	if oldControl != nil {
		_ = oldControl.Close()
	}
	if oldRDP != nil {
		_ = oldRDP.Close()
	}
	if oldP2P != nil {
		_ = oldP2P.Close()
	}
	if oldProxyP2P != nil {
		_ = oldProxyP2P.Close()
	}
	go func() {
		defer a.wg.Done()
		err := a.serveSession(sess, cfg, handler, epoch)
		if err != nil && !a.closed.Load() {
			log.Printf("[Agent] Session ended: %v", err)
		}
		a.mu.Lock()
		if a.epoch == epoch && a.readySession == sess {
			a.readySession = nil
			a.ctrlStream = nil
			a.approvedMode = ""
			a.handshakeOK.Store(false)
		}
		a.mu.Unlock()
		a.tunnelMgr.MarkSessionLost(sess)
	}()
}

func (a *Agent) serveSession(sess tunnel.TunnelSession, cfg AgentConfig, handler *exit.Handler, epoch uint64) error {
	if strings.TrimSpace(cfg.IdentityID) == "" {
		return errors.New("identity id is required")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = sess.Close() })
	defer stopClose()
	defer sess.Close()

	deadline := time.Now().Add(cfg.ConnectTimeout)
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
		"tcp", "quic", "tls", protocol.UDPModeStream, protocol.CapabilitySpeedTest,
		protocol.CapabilityResourceInventoryPush,
	}
	if cfg.IsP2PEnabled() && cfg.P2PMode != "relay_only" {
		transportCaps = append(transportCaps, protocol.CapabilityProxyP2P, protocol.CapabilityProxyStreamResume)
	}
	if publicDirectAllowedForMode(cfg.P2PMode) {
		transportCaps = append(transportCaps, protocol.CapabilityProxyPublicDirect)
	}
	if tunnel.SupportsDatagrams(sess) {
		transportCaps = append(transportCaps, protocol.UDPModeDatagram)
	}
	requested := make([]string, 0, 2)
	if cfg.Mode == "CLIENT" || cfg.Mode == "BOTH" {
		requested = append(requested, protocol.CapabilityProxyClient)
	}
	if handler != nil {
		requested = append(requested, protocol.CapabilityProxyExit)
		transportCaps = append(transportCaps, protocol.CapabilityTargetACL)
	}
	if cfg.IsRDPEnabled() {
		requested = append(requested, protocol.CapabilityRDPClient, protocol.CapabilityRDPHost, protocol.CapabilityRDPPublic)
	}
	clientNonce := make([]byte, 32)
	if _, err := rand.Read(clientNonce); err != nil {
		return fmt.Errorf("generate client nonce: %w", err)
	}
	hello := protocol.DeviceHello{
		ProtocolVersion: protocol.IdentityDeviceProtocolVersion,
		IdentityID:      strings.ToLower(strings.TrimSpace(cfg.IdentityID)),
		InstallationID:  cfg.Identity.InstallationID,
		PublicKey:       append([]byte(nil), cfg.Identity.PublicKey...),
		ClientNonce:     clientNonce, DeviceName: cfg.DeviceName, Platform: runtime.GOOS,
		Arch: runtime.GOARCH, ClientVersion: "2.0.0",
		RequestedCapabilities: requested, TransportCapabilities: transportCaps,
	}
	if err := protocol.WriteJSON(ctrl, hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(ctrl, &challenge); err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}
	if challenge.ProtocolVersion != protocol.IdentityDeviceProtocolVersion || challenge.ChallengeID == "" ||
		challenge.ServerInstanceID == "" || len(challenge.ServerNonce) != 32 || time.Now().Unix() > challenge.ExpiresAt {
		return errors.New("server returned an invalid authentication challenge")
	}
	proof := protocol.AuthProof{ChallengeID: challenge.ChallengeID, Signature: cfg.Identity.Sign(protocol.DeviceAuthPayload(hello, challenge))}
	if err := protocol.WriteJSON(ctrl, proof); err != nil {
		return fmt.Errorf("send authentication proof: %w", err)
	}
	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(ctrl, &accepted); err != nil {
		return fmt.Errorf("read device approval: %w", err)
	}
	a.setApprovalState(accepted.State)
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
	a.mu.Lock()
	if a.closed.Load() || a.epoch != epoch || a.tunnelMgr.Session() != sess {
		a.mu.Unlock()
		return errors.New("session superseded during authentication")
	}
	a.cfg.DeviceID = accepted.DeviceID
	a.identityName = accepted.IdentityName
	a.policyRevision = accepted.PolicyRevision
	a.approvedMode = modeForApprovedCapabilities(accepted.ApprovedCapabilities)
	a.rdpTargets = rdpTargetsFromProtocol(accepted.RDPTargets)
	if accepted.ProxyExitRevision != 0 {
		a.proxyExitRevision = accepted.ProxyExitRevision
	}
	if accepted.ProxyExits != nil {
		a.proxyExits = proxyExitsFromProtocol(*accepted.ProxyExits)
	}
	a.ctrlStream, a.readySession = ctrl, sess
	a.handshakeOK.Store(true)
	a.mu.Unlock()
	if accepted.ProxyExits != nil {
		a.updatePublicDirectInventory(proxyExitsFromProtocol(*accepted.ProxyExits))
	}

	if a.divertSrv != nil && !a.divertSrv.Running() {
		a.setDivertStage("starting", nil)
		log.Printf("[divert] stage=starting relay_ready=true")
		if err := a.divertSrv.Start(); err != nil {
			// Transparent proxy startup is best-effort. Never sacrifice the
			// host's ordinary networking because interception cannot be armed.
			a.setDivertStage("error", err)
			log.Printf("[Agent] Transparent proxy not armed: %v", err)
		} else {
			a.setDivertStage("running", nil)
			log.Printf("[divert] stage=running")
		}
	}
	log.Printf("[Agent] Device approved. SessionID: %s, Heartbeat: %ds", accepted.SessionID, accepted.HeartbeatSec)

	var p2pManager *rdpp2p.Manager
	if cfg.IsRDPEnabled() && (slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPClient) || slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost)) {
		lease := time.Duration(accepted.RDPLeaseSec) * time.Second
		if lease <= 0 {
			lease = 60 * time.Second
		}
		p2pManager = rdpp2p.NewManager(ctx, func(controlCtx context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
			return a.sendRDPControlRequest(controlCtx, sess, message)
		}, cfg.RDPAddress, lease, accepted.RendezvousAddress)
		p2pManager.SetTargetMode(slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost))
		if err := p2pManager.Start(); err != nil {
			log.Printf("[RDP] direct path registration unavailable, relay fallback remains active: %v", err)
			_ = p2pManager.Close()
			p2pManager = nil
		} else {
			keepManager := false
			a.mu.Lock()
			if a.epoch == epoch && a.readySession == sess {
				a.rdpP2P = p2pManager
				keepManager = true
			}
			a.mu.Unlock()
			if !keepManager {
				// The authenticated session was superseded while the local
				// listeners were starting. Nothing owns this manager in that
				// case, so close it immediately instead of leaking its listeners.
				_ = p2pManager.Close()
				p2pManager = nil
			}
		}
	}

	var proxyP2PManager *proxyp2p.Manager
	if cfg.IsP2PEnabled() && cfg.P2PMode != "relay_only" &&
		slices.Contains(accepted.TransportCapabilities, protocol.CapabilityProxyP2P) &&
		(slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyClient) ||
			slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyExit)) {
		lease := time.Duration(accepted.P2PLeaseSec) * time.Second
		if lease <= 0 {
			lease = 60 * time.Second
		}
		proxyP2PManager = proxyp2p.NewQUICManagerWithOptions(ctx, func(controlCtx context.Context, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
			return a.sendP2PControlRequest(controlCtx, sess, message)
		}, accepted.P2PRendezvousAddress, lease, proxyp2p.QUICManagerOptions{
			PunchTimeout:    cfg.P2PPunchTimeout,
			KeepAlive:       cfg.P2PKeepalive,
			IdleTimeout:     cfg.P2PIdleTimeout,
			MaxExitSessions: cfg.P2PMaxSessions,
			PortStart:       accepted.P2PPortStart,
			PortEnd:         accepted.P2PPortEnd,
			UPnPEnabled:     accepted.P2PUPnPEnabled,
		})
		keepManager := false
		a.mu.Lock()
		if a.epoch == epoch && a.readySession == sess {
			a.proxyP2P = proxyP2PManager
			keepManager = true
		}
		a.mu.Unlock()
		if !keepManager {
			_ = proxyP2PManager.Close()
			proxyP2PManager = nil
		} else if slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyClient) &&
			strings.TrimSpace(cfg.DefaultExitID) != "" {
			proxyP2PManager.PrewarmClient(cfg.DefaultExitID)
		}
	}

	allowRDP := slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost)
	allowExit := handler != nil && slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyExit)
	stopPublicDirect := func() {}
	publicDirectExitEnabled := allowExit && publicDirectAllowedForMode(cfg.P2PMode)
	if publicDirectExitEnabled {
		stop, err := a.startPublicDirectExit(ctx, sess, accepted, handler, accepted.MaxConnections)
		if err != nil {
			log.Printf("[PublicDirect] listener unavailable; P2P/Relay fallback remains active: %v", err)
		} else {
			stopPublicDirect = stop
		}
	}

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		a.heartbeatLoop(ctx, ctrl, sess, accepted.HeartbeatSec, epoch)
	}()
	// Every approved Agent accepts server-originated message streams, including
	// CLIENT-only devices that do not expose proxy-exit or RDP services.
	workers.Add(1)
	go func() {
		defer workers.Done()
		a.acceptIncomingStreams(ctx, sess, epoch, func() *exit.Handler {
			if allowExit {
				return handler
			}
			return nil
		}(), allowRDP, cfg.RDPAddress, accepted.MaxConnections, p2pManager, proxyP2PManager, &workers)
	}()
	if proxyP2PManager != nil && allowExit {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a.serveProxyP2PExit(ctx, proxyP2PManager, handler, accepted.MaxConnections)
		}()
	}
	select {
	case <-ctx.Done():
	case <-sess.Done():
	}
	cancel()
	stopPublicDirect()
	_ = sess.Close()
	if proxyP2PManager != nil {
		_ = proxyP2PManager.Close()
	}
	workers.Wait()
	a.clearRDPState(sess, epoch)
	a.clearProxyP2PState(sess, epoch, proxyP2PManager)
	a.clearPublicDirectState(sess, epoch)
	return nil
}

func (a *Agent) heartbeatLoop(ctx context.Context, ctrl tunnel.TunnelStream, sess tunnel.TunnelSession, heartbeatSec int, epoch uint64) {
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
			var pong protocol.PongMessage
			diagnostics, marshalErr := json.Marshal(a.Diagnostics())
			if marshalErr != nil {
				log.Printf("[Agent] Encode diagnostics failed: %v", marshalErr)
				diagnostics = nil
			} else if len(diagnostics) > 256*1024 {
				log.Printf("[Agent] Diagnostics snapshot omitted: %d bytes exceeds 256 KiB", len(diagnostics))
				diagnostics = nil
			}
			err := protocol.WriteJSON(ctrl, protocol.PingMessage{Timestamp: start.UnixMilli(), Diagnostics: diagnostics})
			if err == nil {
				err = protocol.ReadJSON(ctrl, &pong)
			}
			if err != nil {
				// A timeout can leave a partial JSON frame. Reusing this stream
				// after the error would lose framing; reconnect the session.
				log.Printf("[Agent] Heartbeat failed: %v", err)
				_ = sess.Close()
				return
			}
			_ = ctrl.SetDeadline(time.Time{})
			if pong.RDPTargets != nil {
				a.refreshRDPTargets(sess, epoch, *pong.RDPTargets)
			}
			if pong.ProxyExits != nil {
				a.refreshProxyExits(sess, epoch, *pong.ProxyExits, pong.ProxyExitRevision)
			}
			a.mu.RLock()
			if a.epoch == epoch && a.readySession == sess {
				a.latencyMs.Store(time.Since(start).Milliseconds())
			}
			a.mu.RUnlock()
		}
	}
}

func proxyExitSummaries(exits []protocol.ProxyExit) []ProxyExitSummary {
	result := make([]ProxyExitSummary, 0, len(exits))
	for _, exit := range exits {
		result = append(result, ProxyExitSummary{
			DeviceID:            exit.DeviceID,
			Name:                exit.Name,
			IdentityName:        exit.IdentityName,
			AuthorizationSource: exit.AuthorizationSource,
			Online:              exit.Online,
		})
	}
	return result
}

func proxyExitsFromProtocol(exits []protocol.ProxyExit) []protocol.ProxyExit {
	result := make([]protocol.ProxyExit, 0, len(exits))
	seen := make(map[string]bool, len(exits))
	for _, exit := range exits {
		exit.DeviceID = strings.TrimSpace(exit.DeviceID)
		if exit.DeviceID == "" || seen[exit.DeviceID] {
			continue
		}
		seen[exit.DeviceID] = true
		if strings.TrimSpace(exit.Name) == "" {
			exit.Name = exit.DeviceID
		}
		result = append(result, exit)
	}
	return result
}

func (a *Agent) refreshProxyExits(sess tunnel.TunnelSession, epoch uint64, exits []protocol.ProxyExit, revision uint64) {
	refreshed := proxyExitsFromProtocol(exits)
	a.mu.Lock()
	if a.epoch != epoch || a.readySession != sess {
		a.mu.Unlock()
		return
	}
	// Revision zero is the compatibility path for older servers. For versioned
	// pushes, reject only strictly older snapshots; the same revision may be
	// replayed by heartbeat as a recovery copy after a lost/corrupt push.
	if revision != 0 && a.proxyExitRevision != 0 && revision < a.proxyExitRevision {
		a.mu.Unlock()
		return
	}
	a.proxyExits = refreshed
	if revision != 0 {
		a.proxyExitRevision = revision
	}
	a.mu.Unlock()
	a.updatePublicDirectInventory(refreshed)
}

func rdpTargetsFromProtocol(targets []protocol.RDPTarget) []rdp.Target {
	result := make([]rdp.Target, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.DeviceID) == "" {
			continue
		}
		result = append(result, rdp.Target{DeviceID: target.DeviceID, Name: target.Name, Online: target.Online})
	}
	return result
}

func (a *Agent) refreshRDPTargets(sess tunnel.TunnelSession, epoch uint64, targets []protocol.RDPTarget) {
	refreshed := rdpTargetsFromProtocol(targets)
	a.mu.Lock()
	if a.epoch != epoch || a.readySession != sess {
		a.mu.Unlock()
		return
	}
	a.rdpTargets = refreshed
	if a.rdpConnection == nil || slices.ContainsFunc(refreshed, func(target rdp.Target) bool {
		return target.DeviceID == a.rdpConnection.Target.DeviceID
	}) {
		a.mu.Unlock()
		return
	}
	connection := a.rdpConnection
	p2pSession := a.rdpSession
	a.rdpConnection = nil
	a.rdpSession = nil
	a.mu.Unlock()
	_ = connection.Close()
	if p2pSession != nil {
		_ = p2pSession.Close()
	}
}

// sendRDPControlRequest uses one short-lived stream per rendezvous operation.
// The heartbeat control stream remains a dedicated ping/pong channel, avoiding
// read races and preserving its framing after a timeout.
func (a *Agent) sendRDPControlRequest(ctx context.Context, sess tunnel.TunnelSession, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
	if sess == nil {
		return protocol.RDPControlMessage{}, errors.New("relay session is unavailable")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return protocol.RDPControlMessage{}, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeRDPControl, RequestID: fmt.Sprintf("rdp_ctl_%d", time.Now().UnixNano()), ExitDeviceID: message.TargetID}); err != nil {
		return protocol.RDPControlMessage{}, err
	}
	if err := protocol.WriteJSON(stream, message); err != nil {
		return protocol.RDPControlMessage{}, err
	}
	var response protocol.RDPControlMessage
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return protocol.RDPControlMessage{}, err
	}
	return response, nil
}

// sendP2PControlRequest keeps proxy P2P signaling on the authenticated Relay
// session. Direct QUIC never carries authorization or rendezvous control.
func (a *Agent) sendP2PControlRequest(ctx context.Context, sess tunnel.TunnelSession, message protocol.P2PControlMessage) (protocol.P2PControlMessage, error) {
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
		RequestID: fmt.Sprintf("p2p_ctl_%d", time.Now().UnixNano()), ExitDeviceID: message.ExitDeviceID,
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

func (a *Agent) acceptIncomingStreams(ctx context.Context, sess tunnel.TunnelSession, epoch uint64, handler *exit.Handler, allowRDP bool, rdpAddress string, maxStreams int, p2pManager *rdpp2p.Manager, proxyP2PManager *proxyp2p.Manager, workers *sync.WaitGroup) {
	if maxStreams <= 0 {
		maxStreams = 1024
	}
	var admitted atomic.Int64
	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			_ = sess.Close()
			return
		}
		if admitted.Add(1) > int64(maxStreams) {
			admitted.Add(-1)
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		stopHeader := tunnel.InterruptOnCancel(ctx, stream)
		header, err := protocol.ReadStreamHeader(stream)
		stopHeader()
		if err != nil {
			admitted.Add(-1)
			_ = stream.Close()
			continue
		}
		if header.Type == protocol.FrameTypePushMessage {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				defer stream.Close()
				var message protocol.PushMessage
				if err := protocol.ReadJSON(stream, &message); err != nil {
					_ = protocol.WriteJSON(stream, protocol.PushMessageReceipt{MessageID: message.ID, Received: false, Error: err.Error()})
					return
				}
				if message.ID == "" || strings.TrimSpace(message.Content) == "" {
					_ = protocol.WriteJSON(stream, protocol.PushMessageReceipt{MessageID: message.ID, Received: false, Error: "invalid message"})
					return
				}
				a.messages.Add(message)
				log.Printf("[Message] received id=%s source=%s type=%s popup=%v verification=%v",
					message.ID, message.Source, message.MessageType, message.Popup, message.VerificationCode != "")
				_ = protocol.WriteJSON(stream, protocol.PushMessageReceipt{MessageID: message.ID, Received: true})
			}()
			continue
		}
		if (header.Type == protocol.FrameTypeOpenRDP || header.Type == protocol.FrameTypeOpenRDPUDP) && allowRDP {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				if err := rdp.HandleTargetStreamWithHeader(ctx, stream, sess, rdpAddress, header); err != nil &&
					!errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
					log.Printf("[RDP] target stream type=%d failed: %v", header.Type, err)
				}
			}()
			continue
		}
		if header.Type == protocol.FrameTypeRDPControl && p2pManager != nil {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				defer stream.Close()
				var message protocol.RDPControlMessage
				if err := protocol.ReadJSON(stream, &message); err == nil {
					p2pManager.HandleControl(message)
				}
			}()
			continue
		}
		if header.Type == protocol.FrameTypeP2PControl && proxyP2PManager != nil {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				defer stream.Close()
				var message protocol.P2PControlMessage
				if err := protocol.ReadJSON(stream, &message); err == nil {
					proxyP2PManager.HandleControl(message)
				}
			}()
			continue
		}
		if header.Type == protocol.FrameTypeResourceInventory {
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				defer stream.Close()
				var inventory protocol.ResourceInventory
				if err := protocol.ReadJSON(stream, &inventory); err != nil {
					return
				}
				if inventory.ProxyExits != nil {
					a.refreshProxyExits(sess, epoch, *inventory.ProxyExits, inventory.ProxyExitRevision)
				}
			}()
			continue
		}
		if handler == nil {
			admitted.Add(-1)
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer admitted.Add(-1)
			handler.HandleStreamWithHeader(ctx, stream, header)
		}()
	}
}

func (a *Agent) serveProxyP2PExit(ctx context.Context, manager *proxyp2p.Manager, handler *exit.Handler, maxStreams int) {
	if manager == nil || handler == nil {
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
				// Client-side READY sessions also arrive on this channel when the
				// Agent has BOTH grants. Only Exit-side offers carry server ACL.
				continue
			}
			direct, ok := p2pSession.Tunnel()
			if !ok || direct == nil {
				continue
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				a.acceptProxyP2PExitSession(ctx, direct, handler, policy, maxStreams)
			}()
		}
	}
}

func (a *Agent) acceptProxyP2PExitSession(ctx context.Context, sess tunnel.TunnelSession, handler *exit.Handler, relayPolicy *acl.Policy, maxStreams int) {
	if sess == nil || handler == nil || relayPolicy == nil {
		return
	}
	if maxStreams <= 0 {
		maxStreams = 1024
	}
	sem := make(chan struct{}, maxStreams)
	var workers sync.WaitGroup
	defer workers.Wait()
	boundCtx := exit.BindRelayPolicy(ctx, relayPolicy)
	for {
		stream, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		header, err := protocol.ReadStreamHeader(stream)
		if err != nil || (header.Type != protocol.FrameTypeOpenTCP && header.Type != protocol.FrameTypeOpenUDP && header.Type != protocol.FrameTypeSpeedTest) {
			<-sem
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-sem }()
			handler.HandleStreamWithHeader(boundCtx, stream, header)
		}()
	}
}

// Messages returns the newest local push-message history in receive order.
func (a *Agent) Messages(limit int) []Message {
	return a.messages.Get(limit)
}

func (a *Agent) ClearMessages() {
	a.messages.Clear()
}

func (a *Agent) RestoreMessages(messages []Message) {
	a.messages.Restore(messages)
}

func (a *Agent) SetMessageTap(tap func(Message)) {
	a.messages.SetTap(tap)
}

// Start prepares local services independently of the availability of an exit.
// Any failed startup rolls back the resources already acquired by this agent.
func (a *Agent) Start() (err error) {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	if a.closed.Load() {
		return errors.New("agent closed")
	}
	a.mu.RLock()
	started, cfg := a.started, cloneAgentConfig(a.cfg)
	a.mu.RUnlock()
	if started {
		return errors.New("agent already started")
	}
	defer func() {
		if err != nil {
			_ = a.closeRuntime()
		}
	}()
	// Do not take ownership of system traffic before the Relay session is
	// authenticated. SOCKS/HTTP listeners may start immediately, while the
	// transparent interceptor is armed lazily after handshake readiness.
	getExit := func() string {
		if ptr := a.selectedExit.Load(); ptr != nil {
			return *ptr
		}
		return ""
	}
	if cfg.Mode == "CLIENT" || cfg.Mode == "BOTH" {
		if cfg.IsSOCKS5Enabled() {
			srv := socks5.NewServer(socks5.ServerConfig{ListenAddr: cfg.SOCKS5Listen, GetExitNodeID: getExit, Dialer: a.dialer})
			if err := srv.Start(); err != nil {
				return fmt.Errorf("failed to start SOCKS5: %w", err)
			}
			a.mu.Lock()
			a.socksServer = srv
			a.mu.Unlock()
		}
		if cfg.IsHTTPEnabled() {
			srv := httpproxy.NewServer(httpproxy.ServerConfig{ListenAddr: cfg.HTTPListen, GetExitNodeID: getExit, Dialer: a.dialer})
			if err := srv.Start(); err != nil {
				return fmt.Errorf("failed to start HTTP proxy: %w", err)
			}
			a.mu.Lock()
			a.httpServer = srv
			a.mu.Unlock()
		}
	}
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
	a.ConnectTunnel()
	return nil
}

func (a *Agent) SelectExit(exitID string) {
	a.mu.Lock()
	a.cfg.DefaultExitID = exitID
	a.selectedExit.Store(&exitID)
	a.dialer.SetDefaultExitID(exitID)
	a.mu.Unlock()
	a.ensureProxyDirectPath(exitID)
}

func (a *Agent) Status() AgentStatus {
	a.mu.RLock()
	st := AgentStatus{
		DeviceID: a.cfg.DeviceID, DeviceName: a.cfg.DeviceName,
		IdentityName: a.identityName, PolicyRevision: a.policyRevision, Mode: a.approvedMode,
		ProxyExits: proxyExitSummaries(a.proxyExits), ProxyExitRevision: a.proxyExitRevision,
		SOCKS5Running: a.started && a.socksServer != nil,
		HTTPRunning:   a.started && a.httpServer != nil,
		ExitRunning:   a.started && a.exitHandler != nil,
		NetworkMode:   a.cfg.NetworkMode, LatencyMs: a.latencyMs.Load(),
		NativeUDP: tunnel.NativeUDPUsage(),
	}
	sess, handler, divertSrv, proxyP2P, proxyDirect := a.readySession, a.exitHandler, a.divertSrv, a.proxyP2P, a.proxyDirect
	if a.rdpConnection != nil {
		st.RDPListenAddr = a.rdpConnection.ListenAddr
		st.RDPTargetID = a.rdpConnection.Target.DeviceID
		st.RDPUDPEnabled, st.RDPUDPActive, st.RDPUDPReason = a.rdpConnection.UDPStatus()
		st.RDPPathTCP = "relay"
		if st.RDPUDPEnabled {
			// Without a P2P session the only possible UDP path is the native
			// QUIC datagram relay. A P2P session overwrites this below with its
			// independently selected path.
			st.RDPPathUDP = "relay"
			if st.RDPUDPActive {
				st.RDPUDPReason = "UDP 已建立，正在转发 mstsc 数据"
			} else if st.RDPUDPReason == "" {
				st.RDPUDPReason = "UDP 已监听，等待 mstsc 发起数据"
			}
		} else {
			st.RDPPathUDP = "disabled"
			if st.RDPUDPReason == "" {
				st.RDPUDPReason = "当前传输不提供 RDP UDP"
			}
		}
	}
	if !st.RDPUDPEnabled && st.RDPListenAddr != "" && st.RDPUDPReason == "" {
		st.RDPUDPReason = "当前传输不提供 RDP UDP"
	}
	if a.rdpSession != nil {
		st.RDPPathTCP = a.rdpSession.PathTCP()
		st.RDPPathUDP = a.rdpSession.PathUDP()
		if st.RDPUDPEnabled {
			if st.RDPUDPActive {
				st.RDPUDPReason = "UDP 已建立，路径：" + st.RDPPathUDP
			} else {
				st.RDPUDPReason = "UDP 已监听，等待 mstsc 发起数据"
			}
		}
	}
	st.Connected = a.handshakeOK.Load() && sess != nil
	a.mu.RUnlock()
	if ptr := a.selectedExit.Load(); ptr != nil {
		st.SelectedExit = *ptr
	}
	if sess != nil {
		st.Transport = string(sess.Transport())
		st.TunnelDiagnostics = tunnel.DiagnoseSession(sess)
		select {
		case <-sess.Done():
			st.Connected = false
		default:
		}
	}
	if proxyDirect != nil {
		if path, ok := proxyDirect.PathStatus(st.SelectedExit); ok {
			st.DirectState = path.State
			st.DirectError = path.Error
			st.DirectEndpoint = path.Endpoint
			st.DirectFallbackCount = path.FallbackCount
			if directSession, ready := proxyDirect.ReadyForExit(st.SelectedExit); ready {
				st.DirectPath = string(protocol.ProxyPathPublicDirectQUIC)
				if diagnostics := tunnel.DiagnoseSession(directSession); diagnostics != nil && diagnostics.QUIC != nil {
					st.DirectRTTMs = int64(diagnostics.QUIC.SmoothedRTTMS)
					st.DirectBytesUp = diagnostics.QUIC.BytesSent
					st.DirectBytesDown = diagnostics.QUIC.BytesReceived
					st.DirectQUIC = diagnostics.QUIC
				}
			}
		}
	}
	if proxyP2P != nil {
		if path, ok := proxyP2P.PathStatus(st.SelectedExit); ok {
			st.P2PState = string(path.State)
			st.P2PPath = path.Path
			st.P2PError = path.Error
			st.P2PSessionID = path.SessionID
			st.P2PExitID = path.ExitDeviceID
			st.P2PRTTMs = path.RTTMs
			st.P2PCandidateSummary = path.CandidateSummary
			st.P2PFallbackCount = path.FallbackCount
			st.P2PBytesUp = path.BytesUp
			st.P2PBytesDown = path.BytesDown
			if st.DirectPath == "" && path.Path != "" {
				st.DirectState = string(path.State)
				st.DirectPath = path.Path
				st.DirectError = path.Error
				st.DirectRTTMs = path.RTTMs
				st.DirectFallbackCount = path.FallbackCount
				st.DirectBytesUp = path.BytesUp
				st.DirectBytesDown = path.BytesDown
			}
		} else {
			st.P2PState = "IDLE"
		}
	}
	if st.P2PState != "" && st.P2PPath == "" && st.Connected {
		switch st.Transport {
		case string(tunnel.TransportQUIC):
			st.P2PPath = protocol.P2PPathRelayQUIC
		case string(tunnel.TransportTLS):
			st.P2PPath = protocol.P2PPathRelayTLS
		}
	}
	if st.DirectPath == "" && st.Connected {
		if st.DirectState == "" {
			st.DirectState = "IDLE"
		}
		switch st.Transport {
		case string(tunnel.TransportQUIC):
			st.DirectPath = string(protocol.ProxyPathRelayQUIC)
		case string(tunnel.TransportTLS):
			st.DirectPath = string(protocol.ProxyPathRelayTLS)
		}
		if st.TunnelDiagnostics != nil && st.TunnelDiagnostics.QUIC != nil {
			st.DirectRTTMs = int64(st.TunnelDiagnostics.QUIC.SmoothedRTTMS)
			st.DirectBytesUp = st.TunnelDiagnostics.QUIC.BytesSent
			st.DirectBytesDown = st.TunnelDiagnostics.QUIC.BytesReceived
		}
	}
	if st.RDPListenAddr != "" && !st.RDPUDPEnabled {
		switch st.Transport {
		case string(tunnel.TransportTLS):
			st.RDPUDPReason = "当前隧道为 TLS/TCP；请开启 QUIC 并放通服务端 UDP 端口"
		case string(tunnel.TransportQUIC):
			if st.RDPUDPReason == "" || st.RDPUDPReason == "当前传输不提供 RDP UDP" {
				st.RDPUDPReason = "QUIC Datagram 未完成协商，已降级为仅 TCP"
			}
		}
	}
	st.DivertRunning = divertSrv != nil && divertSrv.Running()
	if stage := a.divertStage.Load(); stage != nil {
		st.DivertStage = *stage
	}
	if divertErr := a.divertError.Load(); divertErr != nil {
		st.DivertError = *divertErr
	}
	if divertSrv != nil {
		st.DivertDiagnostics = divertSrv.Diagnostics()
	}
	if state := a.approvalState.Load(); state != nil {
		st.ApprovalState = *state
	}
	if handler != nil {
		st.ActiveStreams += handler.ActiveStreams()
	}
	return st
}

func modeForApprovedCapabilities(capabilities []string) string {
	client := slices.Contains(capabilities, protocol.CapabilityProxyClient)
	exit := slices.Contains(capabilities, protocol.CapabilityProxyExit)
	switch {
	case client && exit:
		return "BOTH"
	case exit:
		return "EXIT"
	case client:
		return "CLIENT"
	default:
		return ""
	}
}

// ProxyExits returns the server-approved exit inventory received during the
// last authenticated session. The configured selected exit is intentionally
// kept separate so a revoked/deleted exit remains visible to the UI as a stale
// selection instead of silently changing routing behavior.
func (a *Agent) ProxyExits() []protocol.ProxyExit {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]protocol.ProxyExit{}, a.proxyExits...)
}

// RDPTargets returns the server-approved target list received during the last
// authenticated session.
func (a *Agent) RDPTargets() []rdp.Target {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]rdp.Target{}, a.rdpTargets...)
}

// ConnectRDP starts the loopback TCP/UDP listener for one approved target.
// The server remains the authority: this method only accepts IDs from the
// current server-provided target list.
func (a *Agent) ConnectRDP(targetID string, autoLaunch bool) (rdp.Target, error) {
	a.rdpConnectMu.Lock()
	defer a.rdpConnectMu.Unlock()

	a.mu.Lock()
	if a.closed.Load() || !a.handshakeOK.Load() || a.readySession == nil {
		a.mu.Unlock()
		return rdp.Target{}, errors.New("relay session is not approved")
	}
	var target rdp.Target
	for _, candidate := range a.rdpTargets {
		if candidate.DeviceID == targetID {
			target = candidate
			break
		}
	}
	if target.DeviceID == "" {
		a.mu.Unlock()
		return rdp.Target{}, errors.New("RDP target is not approved")
	}
	if !target.Online {
		a.mu.Unlock()
		return rdp.Target{}, errors.New("RDP target is offline")
	}
	old := a.rdpConnection
	oldP2P := a.rdpSession
	a.rdpConnection = nil
	a.rdpSession = nil
	p2pManager := a.rdpP2P
	sess := a.readySession
	a.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	if oldP2P != nil {
		_ = oldP2P.Close()
	}
	var directSession *rdpp2p.Session
	var err error
	if p2pManager != nil {
		directSession, err = p2pManager.StartController(a.ctx, targetID)
		if err != nil {
			log.Printf("[RDP] direct session unavailable, using relay fallback: %v", err)
			directSession = nil
		}
	}
	dialTCP := a.rawDialer.DialRDPTCP
	dialUDP := a.rawDialer.DialRDPUDP
	if directSession != nil {
		dialTCP = func(ctx context.Context, id string) (net.Conn, error) {
			if id == targetID {
				return dialRDPTCPHappy(ctx, func(directCtx context.Context) (net.Conn, error) {
					return directSession.DialTCP(directCtx)
				}, func(relayCtx context.Context) (net.Conn, error) {
					return a.rawDialer.DialRDPTCP(relayCtx, id)
				})
			}
			return a.rawDialer.DialRDPTCP(ctx, id)
		}
		dialUDP = func(ctx context.Context, id string) (net.PacketConn, error) {
			if id == targetID {
				return dialRDPUDPHappy(ctx, func(directCtx context.Context) (net.PacketConn, error) {
					return directSession.DialUDP(directCtx)
				}, func(relayCtx context.Context) (net.PacketConn, error) {
					return a.rawDialer.DialRDPUDP(relayCtx, id)
				})
			}
			return a.rawDialer.DialRDPUDP(ctx, id)
		}
	}
	conn, err := rdp.StartController(a.ctx, target, rdp.ControllerOptions{
		DialTCP: dialTCP,
		DialUDP: dialUDP,
		SupportsUDP: func() bool {
			return directSession != nil || (sess != nil && tunnel.PeerSupportsDatagrams(sess))
		},
		OnClose: func() {
			if directSession != nil {
				_ = directSession.Close()
			}
		},
	}, autoLaunch)
	if err != nil {
		if directSession != nil {
			_ = directSession.Close()
		}
		return rdp.Target{}, err
	}
	a.mu.Lock()
	stillApproved := slices.ContainsFunc(a.rdpTargets, func(candidate rdp.Target) bool {
		return candidate.DeviceID == targetID
	})
	if a.closed.Load() || a.readySession != sess || !a.handshakeOK.Load() || !stillApproved {
		a.mu.Unlock()
		_ = conn.Close()
		if directSession != nil {
			_ = directSession.Close()
		}
		if !stillApproved {
			return rdp.Target{}, errors.New("RDP target authorization changed while connecting")
		}
		return rdp.Target{}, errors.New("relay session ended while starting RDP")
	}
	a.rdpConnection = conn
	a.rdpSession = directSession
	a.mu.Unlock()
	if directSession != nil {
		directSession.SetOnClose(func() {
			a.mu.Lock()
			if a.rdpSession != directSession {
				a.mu.Unlock()
				return
			}
			active := a.rdpConnection
			a.rdpConnection = nil
			a.rdpSession = nil
			a.mu.Unlock()
			if active != nil {
				// The callback can run from Connection.Close's OnClose hook;
				// close asynchronously to avoid re-entering that sync.Once.
				go active.Close()
			}
		})
	}
	return target, nil
}

// dialRDPTCPHappy starts a direct attempt immediately and warms the existing
// relay after 300ms. The first successful authenticated path wins; the loser
// is cancelled and any late connection is closed.
func dialRDPTCPHappy(ctx context.Context, direct, relay func(context.Context) (net.Conn, error)) (net.Conn, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	defer close(done)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	directCh := make(chan result)
	relayCh := make(chan result)
	run := func(fn func(context.Context) (net.Conn, error), ch chan<- result) {
		conn, err := fn(raceCtx)
		select {
		case ch <- result{conn: conn, err: err}:
		case <-done:
			if conn != nil {
				_ = conn.Close()
			}
		}
	}
	go run(direct, directCh)
	relayStarted := false
	startRelay := func() {
		if relayStarted {
			return
		}
		relayStarted = true
		go run(relay, relayCh)
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	var firstErr error
	directDone, relayDone := false, false
	for {
		select {
		case item := <-directCh:
			directDone = true
			if item.err == nil {
				return item.conn, nil
			}
			firstErr = item.err
			startRelay()
		case item := <-relayCh:
			relayDone = true
			if item.err == nil {
				return item.conn, nil
			}
			if firstErr == nil {
				firstErr = item.err
			}
		case <-timer.C:
			startRelay()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if directDone && relayDone {
			return nil, firstErr
		}
	}
}

func dialRDPUDPHappy(ctx context.Context, direct, relay func(context.Context) (net.PacketConn, error)) (net.PacketConn, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	defer close(done)
	defer cancel()
	type result struct {
		conn net.PacketConn
		err  error
	}
	directCh := make(chan result)
	relayCh := make(chan result)
	run := func(fn func(context.Context) (net.PacketConn, error), ch chan<- result) {
		conn, err := fn(raceCtx)
		select {
		case ch <- result{conn: conn, err: err}:
		case <-done:
			if conn != nil {
				_ = conn.Close()
			}
		}
	}
	go run(direct, directCh)
	relayStarted := false
	startRelay := func() {
		if relayStarted {
			return
		}
		relayStarted = true
		go run(relay, relayCh)
	}
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	var firstErr error
	directDone, relayDone := false, false
	for {
		select {
		case item := <-directCh:
			directDone = true
			if item.err == nil {
				return item.conn, nil
			}
			firstErr = item.err
			startRelay()
		case item := <-relayCh:
			relayDone = true
			if item.err == nil {
				return item.conn, nil
			}
			if firstErr == nil {
				firstErr = item.err
			}
		case <-timer.C:
			startRelay()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if directDone && relayDone {
			return nil, firstErr
		}
	}
}

func (a *Agent) DisconnectRDP() {
	a.rdpConnectMu.Lock()
	defer a.rdpConnectMu.Unlock()

	a.mu.Lock()
	conn := a.rdpConnection
	p2pSession := a.rdpSession
	a.rdpConnection = nil
	a.rdpSession = nil
	a.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if p2pSession != nil {
		_ = p2pSession.Close()
	}
}

func (a *Agent) clearRDPState(sess tunnel.TunnelSession, epoch uint64) {
	a.mu.Lock()
	if a.epoch != epoch || a.readySession != sess {
		a.mu.Unlock()
		return
	}
	conn := a.rdpConnection
	p2pSession := a.rdpSession
	p2pManager := a.rdpP2P
	a.rdpConnection = nil
	a.rdpSession = nil
	a.rdpP2P = nil
	a.rdpTargets = nil
	a.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if p2pSession != nil {
		_ = p2pSession.Close()
	}
	if p2pManager != nil {
		_ = p2pManager.Close()
	}
}

// SetP2PPowerConstrained switches the active direct-path manager to its mobile
// battery-aware profile. Android lifecycle code can call this without
// rebuilding the Relay tunnel.
func (a *Agent) SetP2PPowerConstrained(constrained bool) {
	if a == nil {
		return
	}
	a.mu.RLock()
	manager := a.proxyP2P
	a.mu.RUnlock()
	if manager != nil {
		manager.SetPowerConstrained(constrained)
	}
}

func (a *Agent) clearProxyP2PState(sess tunnel.TunnelSession, epoch uint64, manager *proxyp2p.Manager) {
	if manager == nil {
		return
	}
	a.mu.Lock()
	if a.epoch == epoch && a.readySession == sess && a.proxyP2P == manager {
		a.proxyP2P = nil
	}
	a.mu.Unlock()
	_ = manager.Close()
}

func (a *Agent) clearPublicDirectState(sess tunnel.TunnelSession, epoch uint64) {
	if a == nil {
		return
	}
	a.mu.RLock()
	if a.epoch != epoch || a.readySession != sess {
		a.mu.RUnlock()
		return
	}
	manager := a.proxyDirect
	a.mu.RUnlock()
	if manager != nil {
		// Public Direct authorization is derived from the active Relay control
		// session. When that session ends, every direct session and cached ticket
		// must be discarded before a reconnect can authorize fresh paths.
		manager.UpdateInventory(nil)
	}
}

func (a *Agent) setApprovalState(state string) {
	copy := state
	a.approvalState.Store(&copy)
}

// Config returns an independent snapshot of fields that actually took effect.
func (a *Agent) Config() AgentConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneAgentConfig(a.cfg)
}

func (a *Agent) ConnectTunnel() {
	a.mu.Lock()
	if a.closed.Load() || !a.started {
		a.mu.Unlock()
		return
	}
	timeout := a.cfg.ConnectTimeout
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, timeout)
		defer cancel()
		if _, err := a.tunnelMgr.Connect(ctx); err != nil && !a.closed.Load() {
			log.Printf("[Agent] Connection to relay failed: %v", err)
		}
		a.tunnelMgr.StartAutoReconnect()
	}()
}

func (a *Agent) Close() error {
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()
	return a.closeRuntime()
}

func (a *Agent) closeRuntime() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed.Store(true)
		a.started = false
		a.epoch++
		a.handshakeOK.Store(false)
		a.readySession = nil
		socks, httpSrv, divertSrv, ctrl, rdpConn := a.socksServer, a.httpServer, a.divertSrv, a.ctrlStream, a.rdpConnection
		rdpSession, rdpP2P, proxyP2P := a.rdpSession, a.rdpP2P, a.proxyP2P
		exitHandler := a.exitHandler
		a.socksServer, a.httpServer, a.ctrlStream, a.rdpConnection = nil, nil, nil, nil
		a.rdpSession, a.rdpP2P, a.proxyP2P, a.exitHandler = nil, nil, nil, nil
		a.rdpTargets = nil
		a.cancel()
		a.mu.Unlock()
		var errs []error
		if socks != nil {
			errs = append(errs, socks.Close())
		}
		if httpSrv != nil {
			errs = append(errs, httpSrv.Close())
		}
		if divertSrv != nil {
			errs = append(errs, divertSrv.Close())
		}
		if ctrl != nil {
			errs = append(errs, ctrl.Close())
		}
		if rdpConn != nil {
			errs = append(errs, rdpConn.Close())
		}
		if rdpSession != nil {
			errs = append(errs, rdpSession.Close())
		}
		if rdpP2P != nil {
			errs = append(errs, rdpP2P.Close())
		}
		if proxyP2P != nil {
			errs = append(errs, proxyP2P.Close())
		}
		errs = append(errs, a.closePublicDirectClient())
		if exitHandler != nil {
			errs = append(errs, exitHandler.Close())
		}
		if a.tunnelMgr != nil {
			errs = append(errs, a.tunnelMgr.Close())
		}
		a.wg.Wait()
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

func (a *Agent) ApplyPolicies(routeCfg routing.Config, divertCfg divert.Config) error {
	routeCfg = routing.CloneConfig(routeCfg)
	divertCfg = cloneAgentConfig(AgentConfig{DivertConfig: divertCfg}).DivertConfig
	if err := routing.ValidateConfig(routeCfg); err != nil {
		return err
	}
	if err := divert.ValidateConfig(divertCfg); err != nil {
		return err
	}
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return errors.New("agent closed")
	}
	if divertCfg.Mode != a.cfg.NetworkMode {
		return ErrRestartRequired
	}
	if a.divertSrv != nil {
		if err := a.divertSrv.ReloadRules(divertCfg); err != nil {
			return err
		}
	}
	// Both inputs were compiled before publishing under the shared policy lock.
	if err := a.routingEngine.Reload(routeCfg); err != nil {
		return err
	}
	a.cfg.Routing = a.routingEngine.Config()
	a.cfg.DivertConfig = divertCfg
	return nil
}

func (a *Agent) ReloadRouting(cfg routing.Config) error {
	return a.ApplyPolicies(cfg, a.Config().DivertConfig)
}

func (a *Agent) ReloadDivert(cfg divert.Config) error {
	return a.ApplyPolicies(a.Config().Routing, cfg)
}

func cloneAgentConfig(cfg AgentConfig) AgentConfig {
	cloneBool := func(p *bool) *bool {
		if p == nil {
			return nil
		}
		value := *p
		return &value
	}
	cfg.SOCKS5Enabled, cfg.HTTPEnabled, cfg.ExitEnabled, cfg.RDPEnabled = cloneBool(cfg.SOCKS5Enabled), cloneBool(cfg.HTTPEnabled), cloneBool(cfg.ExitEnabled), cloneBool(cfg.RDPEnabled)
	cfg.P2PEnabled, cfg.P2PFallback = cloneBool(cfg.P2PEnabled), cloneBool(cfg.P2PFallback)
	cfg.AccessDomains, cfg.AccessCIDRs = slices.Clone(cfg.AccessDomains), slices.Clone(cfg.AccessCIDRs)
	cfg.Routing = routing.CloneConfig(cfg.Routing)
	cfg.DivertConfig.ExcludeProcesses = slices.Clone(cfg.DivertConfig.ExcludeProcesses)
	cfg.DivertConfig.Rules = slices.Clone(cfg.DivertConfig.Rules)
	for i := range cfg.DivertConfig.Rules {
		r := &cfg.DivertConfig.Rules[i]
		r.Hosts, r.CIDRs, r.Ports, r.Protocols = slices.Clone(r.Hosts), slices.Clone(r.CIDRs), slices.Clone(r.Ports), slices.Clone(r.Protocols)
	}
	return cfg
}

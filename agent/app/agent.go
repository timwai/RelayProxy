package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
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
	"relayproxy/agent/desktop"
	"relayproxy/agent/divert"
	"relayproxy/agent/exit"
	"relayproxy/agent/rdp"
	rdpp2p "relayproxy/agent/rdp/p2p"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
	desktopmedia "relayproxy/internal/desktop"
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
	Identity        *deviceidentity.Identity
	DeviceID        string
	DeviceName      string
	ServerAddress   string
	QUICPort        int
	TCPPort         int
	Mode            string // "CLIENT", "EXIT", "BOTH"
	TransportMode   string // "auto", "quic_only", "tcp_only"
	SOCKS5Enabled   *bool
	SOCKS5Listen    string // "127.0.0.1:1080"
	HTTPEnabled     *bool
	HTTPListen      string // "127.0.0.1:8080"
	DefaultExitID   string
	ExitEnabled     *bool
	RDPEnabled      *bool
	RDPAddress      string // target-local RDP service, default 127.0.0.1:3389
	AllowInternet   bool
	AllowPrivateNet bool
	AllowLoopback   bool     // Allow localhost/loopback for testing
	AccessMode      string   // "" (no gate), "allow" (whitelist), "deny" (blacklist)
	AccessDomains   []string // domain patterns for the access list (glob / .suffix / exact)
	AccessCIDRs     []string // IP ranges for the access list (CIDR / single IP / start-end)
	NetworkMode     string   // "" (off) | "divert"
	DivertConfig    divert.Config
	Routing         routing.Config
	InsecureTLS     bool // Allow self-signed TLS certificates for development/testing
	PlainTCP        bool // Disable TLS entirely; connect via plaintext TCP + yamux
	ConnectTimeout  time.Duration
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

type AgentStatus struct {
	Connected     bool   `json:"connected"`
	Transport     string `json:"transport"`
	LatencyMs     int64  `json:"latency"`
	DeviceID      string `json:"deviceId"`
	DeviceName    string `json:"deviceName"`
	Mode          string `json:"mode"`
	SelectedExit  string `json:"selectedExit"`
	SOCKS5Running bool   `json:"socks5Running"`
	HTTPRunning   bool   `json:"httpRunning"`
	ExitRunning   bool   `json:"exitRunning"`
	NetworkMode   string `json:"networkMode"`
	DivertRunning bool   `json:"divertRunning"`
	ActiveStreams int64  `json:"activeStreams"`
	ApprovalState string `json:"approvalState"`
	RDPListenAddr string `json:"rdpListenAddr,omitempty"`
	RDPTargetID   string `json:"rdpTargetId,omitempty"`
	RDPUDPEnabled bool   `json:"rdpUdpEnabled"`
	RDPUDPActive  bool   `json:"rdpUdpActive"`
	RDPUDPReason  string `json:"rdpUdpReason,omitempty"`
	RDPPathTCP    string `json:"rdpPathTcp,omitempty"`
	RDPPathUDP    string `json:"rdpPathUdp,omitempty"`
}

// ErrRestartRequired means a saved startup setting has not changed the running
// agent. In particular, a role change must never reuse the previous exit handler.
var ErrRestartRequired = errors.New("agent role change requires restart")

type Agent struct {
	cfg                    AgentConfig
	tunnelMgr              *tunnel.TunnelManager
	dialer                 *routing.RoutingDialer
	rawDialer              *client.TunnelDialer
	routingEngine          *routing.Engine
	traffic                *traffic.Registry
	exitHandler            *exit.Handler
	socksServer            *socks5.Server
	httpServer             *httpproxy.Server
	divertSrv              *divert.Server
	ctrlStream             tunnel.TunnelStream
	readySession           tunnel.TunnelSession
	epoch                  uint64
	started                bool
	selectedExit           atomic.Pointer[string]
	latencyMs              atomic.Int64
	handshakeOK            atomic.Bool
	approvalState          atomic.Pointer[string]
	approvedMode           string
	rdpTargets             []rdp.Target
	remoteDesktopTargets   []protocol.RemoteDesktopTarget
	rdpConnection          *rdp.Connection
	rdpP2P                 *rdpp2p.Manager
	rdpSession             *rdpp2p.Session
	desktopHost            desktop.HostHandler
	desktopConnection      *desktop.ControllerSession
	desktopP2PSession      *rdpp2p.Session
	lastDesktopDiagnostics desktop.DesktopDiagnosticsReport
	desktopTargetMedia     map[string]*desktopmedia.MediaConn
	desktopTargetPaths     map[string]*rdpp2p.ApplicationPath
	closed                 atomic.Bool
	ctx                    context.Context
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	mu                     sync.RWMutex
	policyMu               sync.RWMutex
	lifecycleMu            sync.Mutex
	closeOnce              sync.Once
	closeErr               error
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
	if cfg.ConnectTimeout < 0 {
		return nil, errors.New("connect timeout must not be negative")
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = 10 * time.Second
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
	a := &Agent{
		cfg: cfg, ctx: ctx, cancel: cancel, routingEngine: engine, traffic: traffic.NewRegistry(0, 0),
		desktopTargetMedia: make(map[string]*desktopmedia.MediaConn),
		desktopTargetPaths: make(map[string]*rdpp2p.ApplicationPath),
	}
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
	a.dialer = routing.NewRoutingDialer(engine, a.rawDialer, &a.policyMu)
	a.dialer.Traffic, a.dialer.LookupProcess = a.traffic, divert.LookupLocalProcess
	a.SelectExit(cfg.DefaultExitID)
	if (cfg.Mode == "EXIT" || cfg.Mode == "BOTH") && cfg.IsExitEnabled() {
		a.exitHandler = exit.NewHandler(exit.HandlerConfig{ACLChecker: checker, ConnectTimeout: cfg.ConnectTimeout})
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
			SharedPolicy: func(flow divert.Flow) divert.Decision {
				d := engine.DecideFlow(routing.Flow{Process: flow.Process, Host: flow.Host, IP: flow.IP, Port: flow.Port, Protocol: string(flow.Protocol)})
				return divert.Decision{Action: divert.Action(d.Action), ExitID: d.ExitID, Rule: d.Rule, DatagramRequired: d.DatagramRequired}
			},
		})
		if err != nil {
			cancel()
			_ = a.tunnelMgr.Close()
			return nil, fmt.Errorf("invalid divert configuration: %w", err)
		}
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
	oldDesktop := a.desktopConnection
	oldDesktopP2P := a.desktopP2PSession
	a.ctrlStream = nil
	a.rdpConnection = nil
	a.rdpP2P = nil
	a.rdpSession = nil
	a.desktopConnection = nil
	a.desktopP2PSession = nil
	a.desktopTargetMedia = make(map[string]*desktopmedia.MediaConn)
	a.desktopTargetPaths = make(map[string]*rdpp2p.ApplicationPath)
	a.rdpTargets = nil
	a.remoteDesktopTargets = nil
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
		if oldDesktop != nil {
			_ = oldDesktop.Close()
		}
		if oldDesktopP2P != nil {
			_ = oldDesktopP2P.Close()
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
	if oldDesktop != nil {
		_ = oldDesktop.Close()
	}
	if oldDesktopP2P != nil {
		_ = oldDesktopP2P.Close()
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
	transportCaps := []string{"tcp", "quic", "tls", protocol.UDPModeStream}
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
	var desktopCapabilities *protocol.DesktopCapabilities
	if cfg.IsRDPEnabled() {
		requested = append(requested,
			protocol.CapabilityRDPClient, protocol.CapabilityRDPHost, protocol.CapabilityRDPPublic,
			protocol.CapabilityDesktopController,
		)
		a.mu.RLock()
		desktopHost := a.desktopHost
		a.mu.RUnlock()
		if desktopHost != nil {
			requested = append(requested, protocol.CapabilityDesktopHost)
			if provider, ok := desktopHost.(desktop.HostCapabilityProvider); ok {
				capabilityCtx, cancelCapabilities := context.WithTimeout(ctx, 2*time.Second)
				snapshot := provider.DesktopCapabilities(capabilityCtx)
				cancelCapabilities()
				snapshot.RelayDesktop = true
				desktopCapabilities = &snapshot
			}
		}
	}
	clientNonce := make([]byte, 32)
	if _, err := rand.Read(clientNonce); err != nil {
		return fmt.Errorf("generate client nonce: %w", err)
	}
	hello := protocol.DeviceHello{
		ProtocolVersion: protocol.DeviceProtocolVersion,
		InstallationID:  cfg.Identity.InstallationID,
		PublicKey:       append([]byte(nil), cfg.Identity.PublicKey...),
		ClientNonce:     clientNonce, DeviceName: cfg.DeviceName, Platform: runtime.GOOS,
		Arch: runtime.GOARCH, ClientVersion: "2.0.0",
		RequestedCapabilities: requested, TransportCapabilities: transportCaps,
		DesktopCapabilities: desktopCapabilities,
	}
	if err := protocol.WriteJSON(ctrl, hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(ctrl, &challenge); err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}
	if challenge.ProtocolVersion != protocol.DeviceProtocolVersion || challenge.ChallengeID == "" ||
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
	a.approvedMode = modeForApprovedCapabilities(accepted.ApprovedCapabilities)
	a.rdpTargets = make([]rdp.Target, 0, len(accepted.RDPTargets))
	for _, target := range accepted.RDPTargets {
		a.rdpTargets = append(a.rdpTargets, rdp.Target{DeviceID: target.DeviceID, Name: target.Name, Online: target.Online})
	}
	a.remoteDesktopTargets = slices.Clone(accepted.RemoteDesktopTargets)
	a.ctrlStream, a.readySession = ctrl, sess
	a.handshakeOK.Store(true)
	a.mu.Unlock()
	log.Printf("[Agent] Device approved. SessionID: %s, Heartbeat: %ds", accepted.SessionID, accepted.HeartbeatSec)

	var p2pManager *rdpp2p.Manager
	hasRDPDirect := cfg.IsRDPEnabled() &&
		(slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPClient) ||
			slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost))
	hasDesktopDirect := slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityDesktopController) ||
		slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityDesktopHost)
	if hasRDPDirect || hasDesktopDirect {
		lease := time.Duration(accepted.RDPLeaseSec) * time.Second
		if lease <= 0 {
			lease = 60 * time.Second
		}
		p2pManager = rdpp2p.NewManager(ctx, func(controlCtx context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
			return a.sendRDPControlRequest(controlCtx, sess, message)
		}, cfg.RDPAddress, lease, accepted.RendezvousAddress)
		p2pManager.SetTargetMode(cfg.IsRDPEnabled() && slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost))
		if slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityDesktopHost) {
			p2pManager.SetApplicationHandler(protocol.P2PPurposeDesktopMedia, a.handleDesktopApplicationPath)
		}
		if err := p2pManager.Start(); err != nil {
			log.Printf("[P2P] direct path registration unavailable, relay fallback remains active: %v", err)
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

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		a.heartbeatLoop(ctx, ctrl, sess, accepted.HeartbeatSec, epoch)
	}()
	allowRDP := slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityRDPHost)
	allowDesktop := slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityDesktopHost)
	allowExit := handler != nil && slices.Contains(accepted.ApprovedCapabilities, protocol.CapabilityProxyExit)
	if allowExit || allowRDP || allowDesktop || p2pManager != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a.acceptIncomingStreams(ctx, sess, handler, allowRDP, allowDesktop, cfg.RDPAddress, accepted.MaxConnections, p2pManager, &workers)
		}()
	}
	select {
	case <-ctx.Done():
	case <-sess.Done():
	}
	cancel()
	_ = sess.Close()
	workers.Wait()
	a.clearRDPState(sess, epoch)
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
			err := protocol.WriteJSON(ctrl, protocol.PingMessage{Timestamp: start.UnixMilli()})
			if err == nil {
				var pong protocol.PongMessage
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
			a.mu.RLock()
			if a.epoch == epoch && a.readySession == sess {
				a.latencyMs.Store(time.Since(start).Milliseconds())
			}
			a.mu.RUnlock()
		}
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

// SetDesktopHost installs the Relay Desktop target-side media consumer. The
// handler owns capture/encode session lifecycle; nil keeps Relay Desktop
// explicitly unavailable instead of accepting a session that can only black-screen.
func (a *Agent) SetDesktopHost(handler desktop.HostHandler) {
	a.mu.Lock()
	a.desktopHost = handler
	a.mu.Unlock()
}

func (a *Agent) handleDesktopApplicationPath(session *rdpp2p.Session, path *rdpp2p.ApplicationPath) {
	if session == nil || path == nil || session.ControllerID == "" {
		if path != nil {
			_ = path.Close()
		}
		return
	}
	controllerID := session.ControllerID
	a.mu.Lock()
	if a.desktopTargetMedia == nil {
		a.desktopTargetMedia = make(map[string]*desktopmedia.MediaConn)
	}
	if a.desktopTargetPaths == nil {
		a.desktopTargetPaths = make(map[string]*rdpp2p.ApplicationPath)
	}
	conn := a.desktopTargetMedia[controllerID]
	if conn == nil {
		old := a.desktopTargetPaths[controllerID]
		a.desktopTargetPaths[controllerID] = path
		a.mu.Unlock()
		if old != nil && old != path {
			_ = old.Close()
		}
		return
	}
	delete(a.desktopTargetPaths, controllerID)
	a.mu.Unlock()
	conn.SetDatagramPath(path)
	log.Printf("[Desktop] target media path switched controller=%s path=%s", controllerID, path.Name())
}

func (a *Agent) bindDesktopTargetMedia(controllerID string, conn *desktopmedia.MediaConn) func() {
	if controllerID == "" || conn == nil {
		return func() {}
	}
	a.mu.Lock()
	if a.desktopTargetMedia == nil {
		a.desktopTargetMedia = make(map[string]*desktopmedia.MediaConn)
	}
	if a.desktopTargetPaths == nil {
		a.desktopTargetPaths = make(map[string]*rdpp2p.ApplicationPath)
	}
	a.desktopTargetMedia[controllerID] = conn
	path := a.desktopTargetPaths[controllerID]
	delete(a.desktopTargetPaths, controllerID)
	a.mu.Unlock()
	if path != nil {
		conn.SetDatagramPath(path)
		log.Printf("[Desktop] target media path attached controller=%s path=%s", controllerID, path.Name())
	}
	return func() {
		a.mu.Lock()
		if a.desktopTargetMedia[controllerID] == conn {
			delete(a.desktopTargetMedia, controllerID)
		}
		a.mu.Unlock()
	}
}

func (a *Agent) acceptIncomingStreams(ctx context.Context, sess tunnel.TunnelSession, handler *exit.Handler, allowRDP, allowDesktop bool, rdpAddress string, maxStreams int, p2pManager *rdpp2p.Manager, workers *sync.WaitGroup) {
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
		if header.Type == protocol.FrameTypeOpenDesktopMedia && allowDesktop {
			a.mu.RLock()
			desktopHost := a.desktopHost
			a.mu.RUnlock()
			controllerID := header.ClientDeviceID
			if desktopHost != nil {
				baseHost := desktopHost
				desktopHost = desktop.HostHandlerFunc(func(hostCtx context.Context, conn *desktopmedia.MediaConn, options protocol.RemoteDesktopConnectOptions) error {
					unbind := a.bindDesktopTargetMedia(controllerID, conn)
					defer unbind()
					return baseHost.HandleDesktopMedia(hostCtx, conn, options)
				})
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer admitted.Add(-1)
				if err := desktop.HandleTargetMediaStreamWithHeader(ctx, stream, sess, header, desktopHost); err != nil &&
					!errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
					log.Printf("[Desktop] target media stream failed: %v", err)
				}
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
	// Preflight interception before opening listeners or taking ownership of traffic.
	if a.divertSrv != nil {
		if err := a.divertSrv.Start(); err != nil {
			return fmt.Errorf("failed to start divert network: %w", err)
		}
	}
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
}

func (a *Agent) Status() AgentStatus {
	a.mu.RLock()
	st := AgentStatus{
		DeviceID: a.cfg.DeviceID, DeviceName: a.cfg.DeviceName, Mode: a.approvedMode,
		SOCKS5Running: a.started && a.socksServer != nil,
		HTTPRunning:   a.started && a.httpServer != nil,
		ExitRunning:   a.started && a.exitHandler != nil,
		NetworkMode:   a.cfg.NetworkMode, LatencyMs: a.latencyMs.Load(),
	}
	sess, handler, divertSrv := a.readySession, a.exitHandler, a.divertSrv
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
		select {
		case <-sess.Done():
			st.Connected = false
		default:
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

// RemoteDesktopTargets returns the server-owned unified target list. Legacy
// servers that only send RDPTargets remain supported as a Native-RDP fallback.
func (a *Agent) RemoteDesktopTargets() []protocol.RemoteDesktopTarget {
	a.mu.RLock()
	remoteTargets := slices.Clone(a.remoteDesktopTargets)
	a.mu.RUnlock()
	if len(remoteTargets) > 0 {
		return remoteTargets
	}
	rdpTargets := a.RDPTargets()
	targets := make([]protocol.RemoteDesktopTarget, 0, len(rdpTargets))
	for _, target := range rdpTargets {
		targets = append(targets, protocol.RemoteDesktopTarget{
			DeviceID: target.DeviceID,
			Name:     target.Name,
			Online:   target.Online,
			Capabilities: protocol.DesktopCapabilities{
				NativeRDP: true,
			},
		})
	}
	return targets
}

func (a *Agent) remoteDesktopTarget(targetID string) (protocol.RemoteDesktopTarget, bool) {
	for _, target := range a.RemoteDesktopTargets() {
		if target.DeviceID == targetID {
			return target, true
		}
	}
	return protocol.RemoteDesktopTarget{}, false
}

func (a *Agent) startRelayDesktopDirectPath(controller *desktop.ControllerSession, targetID string, manager *rdpp2p.Manager) {
	if controller == nil || manager == nil || targetID == "" {
		return
	}
	policy := desktop.DefaultPathRetryPolicy()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		failures := 0

		waitRetry := func(delay time.Duration) bool {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-a.ctx.Done():
				return false
			case <-controller.Done():
				return false
			case <-timer.C:
				return true
			}
		}

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-controller.Done():
				return
			default:
			}

			attemptCtx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			direct, err := manager.StartControllerForPurpose(attemptCtx, targetID, protocol.P2PPurposeDesktopMedia)
			if err == nil {
				var packetConn net.PacketConn
				packetConn, err = direct.DialUDP(attemptCtx)
				if err == nil {
					path := desktopmedia.NewPacketConnDatagramPath("udp_p2p", packetConn, 5*time.Second, func() {
						_ = direct.Close()
					})
					lost := make(chan struct{})
					var lostOnce sync.Once

					a.mu.Lock()
					if a.closed.Load() || a.desktopConnection != controller || !controller.Active() {
						a.mu.Unlock()
						cancel()
						_ = path.Close()
						return
					}
					old := a.desktopP2PSession
					a.desktopP2PSession = direct
					a.mu.Unlock()
					direct.SetOnClose(func() {
						a.mu.Lock()
						if a.desktopP2PSession == direct {
							a.desktopP2PSession = nil
						}
						a.mu.Unlock()
						lostOnce.Do(func() { close(lost) })
					})
					cancel()

					if old != nil && old != direct {
						_ = old.Close()
					}
					relayQuality := controller.PathQuality()
					connectedAt := time.Now()
					controller.SetDatagramPath(path)
					a.requestRelayDesktopPathIDR(controller, targetID, "relay", path.Name())
					log.Printf("[Desktop] controller media path switched target=%s path=%s", targetID, path.Name())

					qualityFallback, sessionAlive := a.waitRelayDesktopDirectPath(controller, targetID, path, direct, lost, relayQuality)
					if !sessionAlive {
						_ = path.Close()
						return
					}
					controller.ClearDatagramPath(path)
					if controller.Active() {
						a.requestRelayDesktopPathIDR(controller, targetID, path.Name(), "relay")
					}

					if !controller.Active() {
						return
					}
					aliveFor := time.Since(connectedAt)
					if policy.Stable(aliveFor) {
						failures = 0
					}
					delay := policy.Delay(failures)
					failures++
					if qualityFallback != "" {
						log.Printf("[Desktop] P2P media path demoted after %s reason=%s; Relay Datagram active, retrying in %s", aliveFor.Round(time.Second), qualityFallback, delay)
					} else {
						log.Printf("[Desktop] P2P media path lost after %s; Relay Datagram active, retrying in %s", aliveFor.Round(time.Second), delay)
					}
					if !waitRetry(delay) {
						return
					}
					continue
				}
				_ = direct.Close()
			}
			cancel()

			delay := policy.Delay(failures)
			failures++
			log.Printf("[Desktop] P2P media unavailable, Relay Datagram active; retrying in %s: %v", delay, err)
			if !waitRetry(delay) {
				return
			}
		}
	}()
}

// ConnectRemoteDesktop is the single entry point used by the GUI. Native RDP
// and Relay Desktop share discovery but retain separate media implementations.
func (a *Agent) ConnectRemoteDesktop(targetID string, options protocol.RemoteDesktopConnectOptions) (protocol.RemoteDesktopSessionInfo, error) {
	targetID = strings.TrimSpace(targetID)
	target, ok := a.remoteDesktopTarget(targetID)
	if !ok {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("remote desktop target is not approved")
	}
	if !target.Online {
		return protocol.RemoteDesktopSessionInfo{}, errors.New("remote desktop target is offline")
	}
	backend, err := desktop.SelectBackend(target, options)
	if err != nil {
		return protocol.RemoteDesktopSessionInfo{}, err
	}
	switch backend {
	case protocol.DesktopBackendRDP:
		a.disconnectRelayDesktop()
		autoLaunch := true
		if options.AutoLaunch != nil {
			autoLaunch = *options.AutoLaunch
		}
		if _, err := a.ConnectRDP(targetID, autoLaunch); err != nil {
			return protocol.RemoteDesktopSessionInfo{}, err
		}
		status := a.Status()
		return protocol.RemoteDesktopSessionInfo{
			Target:       target,
			Backend:      protocol.DesktopBackendRDP,
			State:        "connected",
			ListenAddr:   status.RDPListenAddr,
			PathTCP:      status.RDPPathTCP,
			PathUDP:      status.RDPPathUDP,
			UDPEnabled:   status.RDPUDPEnabled,
			AutoLaunched: autoLaunch,
		}, nil
	case protocol.DesktopBackendRelay:
		a.DisconnectRDP()
		a.mu.RLock()
		desktopHost := a.desktopHost
		a.mu.RUnlock()
		var localCodecCapabilities []protocol.DesktopCodecCapability
		if provider, ok := desktopHost.(interface {
			CodecCapabilities() []protocol.DesktopCodecCapability
		}); ok {
			localCodecCapabilities = provider.CodecCapabilities()
		}
		options, err = negotiateRemoteDesktopVideo(target, localCodecCapabilities, options)
		if err != nil {
			return protocol.RemoteDesktopSessionInfo{}, err
		}
		options, err = negotiateRemoteDesktopAudio(target, options)
		if err != nil {
			return protocol.RemoteDesktopSessionInfo{}, err
		}
		session, err := desktop.StartControllerWithOptions(a.ctx, targetID, func(ctx context.Context, id string) (*desktopmedia.MediaConn, error) {
			dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return a.rawDialer.DialDesktopMediaWithOptions(dialCtx, id, options)
		}, options)
		if err != nil {
			return protocol.RemoteDesktopSessionInfo{}, err
		}
		a.mu.Lock()
		if a.closed.Load() || !a.handshakeOK.Load() || a.readySession == nil {
			a.mu.Unlock()
			_ = session.Close()
			return protocol.RemoteDesktopSessionInfo{}, errors.New("relay session is no longer approved")
		}
		old := a.desktopConnection
		oldDirect := a.desktopP2PSession
		p2pManager := a.rdpP2P
		a.desktopConnection = session
		a.desktopP2PSession = nil
		a.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		if oldDirect != nil {
			_ = oldDirect.Close()
		}
		a.startRelayDesktopDirectPath(session, targetID, p2pManager)
		return protocol.RemoteDesktopSessionInfo{
			Target: target, Backend: protocol.DesktopBackendRelay, State: "connected",
			PathTCP: "relay-control", PathUDP: "quic-datagram", UDPEnabled: true,
		}, nil
	default:
		return protocol.RemoteDesktopSessionInfo{}, fmt.Errorf("unsupported remote desktop backend %q", backend)
	}
}

func (a *Agent) RemoteDesktopStatus() protocol.RemoteDesktopStatus {
	a.mu.RLock()
	desktopSession := a.desktopConnection
	a.mu.RUnlock()
	if desktopSession != nil && desktopSession.Active() {
		targetID := desktopSession.TargetID()
		pathUDP := desktopSession.DatagramPathName()
		if pathUDP == "" || pathUDP == "relay" {
			pathUDP = "quic-datagram"
		}
		out := protocol.RemoteDesktopStatus{
			State: "connected", Backend: protocol.DesktopBackendRelay, TargetID: targetID,
			PathTCP: "relay-control", PathUDP: pathUDP, UDPEnabled: true, UDPActive: true,
		}
		config := desktopSession.VideoConfigSnapshot()
		out.DisplayID = config.DisplayID
		out.Generation = config.Generation
		out.Codec = config.Codec
		out.Width = config.Width
		out.Height = config.Height
		out.MaxWidth = config.MaxWidth
		out.MaxHeight = config.MaxHeight
		out.FPS = config.FPS
		if target, ok := a.remoteDesktopTarget(targetID); ok {
			out.TargetName = target.Name
			for _, display := range target.Capabilities.Displays {
				if display.ID == out.DisplayID {
					out.DisplayName = display.Name
					break
				}
			}
		}
		return out
	}
	status := a.Status()
	out := protocol.RemoteDesktopStatus{State: "idle"}
	if status.RDPTargetID == "" {
		return out
	}
	out.State = "connected"
	out.Backend = protocol.DesktopBackendRDP
	out.TargetID = status.RDPTargetID
	if target, ok := a.remoteDesktopTarget(status.RDPTargetID); ok {
		out.TargetName = target.Name
	}
	out.ListenAddr = status.RDPListenAddr
	out.PathTCP = status.RDPPathTCP
	out.PathUDP = status.RDPPathUDP
	out.UDPEnabled = status.RDPUDPEnabled
	out.UDPActive = status.RDPUDPActive
	out.UDPReason = status.RDPUDPReason
	return out
}

func (a *Agent) RemoteDesktopStats() protocol.DesktopSessionStats {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return protocol.DesktopSessionStats{}
	}
	return session.Stats()
}

func (a *Agent) RemoteDesktopAudioDiagnostics() desktop.DesktopAudioDiagnostics {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return desktop.DesktopAudioDiagnostics{}
	}
	return session.AudioDiagnosticsSnapshot()
}

func (a *Agent) RemoteDesktopDiagnostics() desktop.DesktopDiagnosticsReport {
	a.mu.RLock()
	session := a.desktopConnection
	last := a.lastDesktopDiagnostics
	a.mu.RUnlock()
	if session != nil {
		return session.Diagnostics()
	}
	return last
}

func (a *Agent) ReportRemoteDesktopViewerStats(stats protocol.DesktopSessionStats) {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return
	}
	session.UpdateViewerStats(stats)
}

func (a *Agent) RemoteDesktopAudioEnabled() bool {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	return session != nil && session.Active() && session.AudioEnabled()
}

func (a *Agent) NextRemoteDesktopAudioFrame(ctx context.Context) (desktop.AudioFrameSnapshot, protocol.DesktopAudioConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return desktop.AudioFrameSnapshot{}, protocol.DesktopAudioConfig{}, errors.New("Relay Desktop session is not connected")
	}
	return session.NextAudioFrame(ctx)
}

func (a *Agent) RemoteDesktopFrame() protocol.RemoteDesktopFrame {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return protocol.RemoteDesktopFrame{}
	}
	frame, ok := session.LatestFrame()
	if !ok {
		return protocol.RemoteDesktopFrame{}
	}
	return protocol.RemoteDesktopFrame{
		Sequence: frame.Sequence, Generation: frame.Generation, MimeType: frame.MimeType, Codec: frame.Codec,
		Width: frame.Width, Height: frame.Height, Timestamp: frame.Timestamp,
		KeyFrame: frame.KeyFrame, Data: frame.Data,
	}
}

func (a *Agent) RemoteDesktopCursor(knownCursorID string) protocol.DesktopCursorState {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return protocol.DesktopCursorState{}
	}
	cursor, ok := session.LatestCursor(knownCursorID)
	if !ok {
		return protocol.DesktopCursorState{}
	}
	return cursor
}

func (a *Agent) RemoteDesktopClipboard(knownSequence uint64) protocol.DesktopClipboardState {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return protocol.DesktopClipboardState{}
	}
	clipboard, ok := session.LatestClipboard(knownSequence)
	if !ok {
		return protocol.DesktopClipboardState{}
	}
	return clipboard
}

func (a *Agent) SendRemoteDesktopClipboard(text string) error {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.SendClipboard(ctx, text)
}

func (a *Agent) SendRemoteDesktopInput(event protocol.DesktopInputEvent) error {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.SendInput(ctx, event)
}

func (a *Agent) SetRemoteDesktopResolution(width, height int) error {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.RequestResolution(ctx, width, height)
}

func (a *Agent) SetRemoteDesktopViewportResolution(width, height int) error {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.RequestViewportResolution(ctx, width, height)
}

func (a *Agent) RemoteDesktopViewportFollowEnabled() bool {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	return session != nil && session.Active() && session.ViewportFollowEnabled()
}

func (a *Agent) RequestRemoteDesktopIDR() error {
	a.mu.RLock()
	session := a.desktopConnection
	a.mu.RUnlock()
	if session == nil || !session.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	return session.RequestIDR(ctx)
}

func (a *Agent) disconnectRelayDesktop() {
	a.mu.Lock()
	session := a.desktopConnection
	direct := a.desktopP2PSession
	a.desktopConnection = nil
	a.desktopP2PSession = nil
	a.mu.Unlock()

	var report desktop.DesktopDiagnosticsReport
	if session != nil {
		_ = session.Close()
		report = session.Diagnostics()
	}
	if report.SchemaVersion != 0 {
		a.mu.Lock()
		a.lastDesktopDiagnostics = report
		a.mu.Unlock()
	}
	if direct != nil {
		_ = direct.Close()
	}
}

func (a *Agent) DisconnectRemoteDesktop() {
	a.disconnectRelayDesktop()
	a.DisconnectRDP()
}

// RDPTargets returns the server-approved target list received during the last
// authenticated session.
func (a *Agent) RDPTargets() []rdp.Target {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return slices.Clone(a.rdpTargets)
}

// ConnectRDP starts the loopback TCP/UDP listener for one approved target.
// The server remains the authority: this method only accepts IDs from the
// current server-provided target list.
func (a *Agent) ConnectRDP(targetID string, autoLaunch bool) (rdp.Target, error) {
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
	if a.closed.Load() || a.readySession != sess || !a.handshakeOK.Load() {
		a.mu.Unlock()
		_ = conn.Close()
		if directSession != nil {
			_ = directSession.Close()
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
	desktopSession := a.desktopConnection
	desktopP2P := a.desktopP2PSession
	a.rdpConnection = nil
	a.rdpSession = nil
	a.rdpP2P = nil
	a.desktopConnection = nil
	a.desktopP2PSession = nil
	a.desktopTargetMedia = make(map[string]*desktopmedia.MediaConn)
	a.desktopTargetPaths = make(map[string]*rdpp2p.ApplicationPath)
	a.rdpTargets = nil
	a.remoteDesktopTargets = nil
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
	if desktopSession != nil {
		_ = desktopSession.Close()
	}
	if desktopP2P != nil {
		_ = desktopP2P.Close()
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
		rdpSession, rdpP2P, desktopSession := a.rdpSession, a.rdpP2P, a.desktopConnection
		desktopP2P := a.desktopP2PSession
		a.socksServer, a.httpServer, a.ctrlStream, a.rdpConnection = nil, nil, nil, nil
		a.rdpSession, a.rdpP2P, a.desktopConnection, a.desktopP2PSession = nil, nil, nil, nil
		a.desktopTargetMedia = make(map[string]*desktopmedia.MediaConn)
		a.desktopTargetPaths = make(map[string]*rdpp2p.ApplicationPath)
		a.rdpTargets = nil
		a.remoteDesktopTargets = nil
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
		if desktopSession != nil {
			errs = append(errs, desktopSession.Close())
		}
		if desktopP2P != nil {
			errs = append(errs, desktopP2P.Close())
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

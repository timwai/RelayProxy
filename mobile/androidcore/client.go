package androidcore

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const clientVersion = "android-0.1.0"

type clientConfig struct {
	ServerAddress       string `json:"serverAddress"`
	DeviceName          string `json:"deviceName"`
	QUICPort            int    `json:"quicPort"`
	TCPPort             int    `json:"tcpPort"`
	TransportMode       string `json:"transportMode"`
	TLSEnabled          *bool  `json:"tlsEnabled"`
	InsecureTLS         bool   `json:"insecureTLS"`
	AllowInternet       *bool  `json:"allowInternet"`
	AllowPrivateNetwork bool   `json:"allowPrivateNetwork"`
	AllowLoopback       bool   `json:"allowLoopback"`
}

type statusSnapshot struct {
	ConnectionState string `json:"connectionState"`
	ApprovalState   string `json:"approvalState"`
	DeviceID        string `json:"deviceId,omitempty"`
	DeviceName      string `json:"deviceName"`
	Transport       string `json:"transport,omitempty"`
	ExitApproved    bool   `json:"exitApproved"`
	ActiveStreams   int64  `json:"activeStreams"`
	LatencyMs       int64  `json:"latencyMs"`
	LastError       string `json:"lastError,omitempty"`
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

	mu      sync.RWMutex
	status  statusSnapshot
	started bool
	closed  bool

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
	if cfg.ServerAddress == "" {
		return cfg, errors.New("serverAddress is required")
	}
	if strings.ContainsAny(cfg.ServerAddress, " /\\\t\r\n") {
		return cfg, errors.New("serverAddress must be a host or IP without scheme or port")
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
	if !*cfg.TLSEnabled && cfg.TransportMode == string(tunnel.ModeQUICOnly) {
		return cfg, errors.New("quic_only requires TLS")
	}
	return cfg, nil
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
		handler:  exit.NewHandler(exit.HandlerConfig{ACLChecker: checker, ConnectTimeout: 10 * time.Second}),
		ctx:      ctx,
		cancel:   cancel,
		status: statusSnapshot{
			ConnectionState: string(tunnel.StateDisconnected),
			ApprovalState:   "unknown",
			DeviceName:      cfg.DeviceName,
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
	return c, nil
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
	c.started = true
	c.status.ConnectionState = string(tunnel.StateConnecting)
	c.status.LastError = ""
	c.mu.Unlock()

	c.manager.StartAutoReconnect()
	c.wg.Add(1)
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
	c.mu.Unlock()

	c.cancel()
	err := c.manager.Close()
	c.wg.Wait()
	return err
}

// StatusJSON returns a stable JSON snapshot for the Android UI.
func (c *Client) StatusJSON() string {
	c.mu.RLock()
	s := c.status
	c.mu.RUnlock()
	s.ActiveStreams = c.handler.ActiveStreams()
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
	if sess != nil {
		c.status.Transport = string(sess.Transport())
	} else if newState != tunnel.StateConnected {
		c.status.Transport = ""
		c.status.ExitApproved = false
	}
	c.mu.Unlock()

	if newState != tunnel.StateConnected || sess == nil {
		return
	}

	c.wg.Add(1)
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

	transportCaps := []string{"tcp", protocol.UDPModeStream, protocol.CapabilityTargetACL}
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
		ProtocolVersion:       protocol.DeviceProtocolVersion,
		InstallationID:        c.identity.InstallationID,
		PublicKey:             append([]byte(nil), c.identity.PublicKey...),
		ClientNonce:           clientNonce,
		DeviceName:            c.cfg.DeviceName,
		Platform:              runtime.GOOS,
		Arch:                  runtime.GOARCH,
		ClientVersion:         clientVersion,
		RequestedCapabilities: []string{protocol.CapabilityProxyExit},
		TransportCapabilities: transportCaps,
	}
	if err := protocol.WriteJSON(ctrl, hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(ctrl, &challenge); err != nil {
		return fmt.Errorf("read authentication challenge: %w", err)
	}
	if challenge.ProtocolVersion != protocol.DeviceProtocolVersion ||
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
	c.mu.Lock()
	if c.closed || c.manager.Session() != sess {
		c.mu.Unlock()
		return errors.New("session superseded during authentication")
	}
	c.status.DeviceID = accepted.DeviceID
	c.status.ApprovalState = accepted.State
	c.status.ExitApproved = exitApproved
	c.status.ConnectionState = string(tunnel.StateConnected)
	c.status.Transport = string(sess.Transport())
	c.status.LastError = ""
	c.mu.Unlock()

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		c.heartbeatLoop(ctx, ctrl, sess, accepted.HeartbeatSec)
	}()
	if exitApproved {
		workers.Add(1)
		go func() {
			defer workers.Done()
			c.acceptExitStreams(ctx, sess, accepted.MaxConnections, &workers)
		}()
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
			if err == nil {
				var pong protocol.PongMessage
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
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) acceptExitStreams(ctx context.Context, sess tunnel.TunnelSession, maxConnections int, workers *sync.WaitGroup) {
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
			switch header.Type {
			case protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP:
				c.handler.HandleStreamWithHeader(ctx, s, header)
			default:
				_ = s.Close()
			}
		}(stream)
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

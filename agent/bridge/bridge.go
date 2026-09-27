package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"relayproxy/agent/app"
	"relayproxy/agent/desktop"
	"relayproxy/agent/divert"
	"relayproxy/agent/rdp"
	"relayproxy/agent/routing"
	"relayproxy/agent/startup"
	"relayproxy/internal/config"
	"relayproxy/internal/protocol"
)

const autoStartName = "RelayProxy Agent"

// UIBridge decouples the UI layer (WebView2 desktop window / JS bridge) from the
// Agent core. It is the only package the GUI talks to, so the UI never reaches
// into the agent's internals directly.
type UIBridge struct {
	agent         *app.Agent
	configPath    string
	mu            sync.RWMutex
	writeConfig   func(string, *config.AgentConfigFile) error
	syncAutoStart func(string, bool) (func() error, error)
	setAutoStart  func(string, bool, bool) error
}

func NewUIBridge(agent *app.Agent, configPath string) *UIBridge {
	return &UIBridge{
		agent:       agent,
		configPath:  configPath,
		writeConfig: config.SaveAgentConfig,
		syncAutoStart: func(path string, requireAdmin bool) (func() error, error) {
			executable, err := os.Executable()
			if err != nil {
				return nil, err
			}
			return startup.SyncAutoStart(autoStartName, executable, path, requireAdmin)
		},
		setAutoStart: func(path string, enabled, requireAdmin bool) error {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			return startup.SetAutoStart(autoStartName, executable, path, enabled, requireAdmin)
		},
	}
}

// ---------------------------------------------------------------------------
// Status & logs
// ---------------------------------------------------------------------------

// GetStatus returns the current agent runtime state
func (b *UIBridge) GetStatus() app.AgentStatus {
	return b.agent.Status()
}

func (b *UIBridge) GetRDPTargets() []rdp.Target {
	return b.agent.RDPTargets()
}

func (b *UIBridge) ConnectRDP(targetID string, autoLaunch bool) (rdp.Target, error) {
	return b.agent.ConnectRDP(strings.TrimSpace(targetID), autoLaunch)
}

func (b *UIBridge) DisconnectRDP() {
	b.agent.DisconnectRDP()
}

func (b *UIBridge) GetRemoteDesktopTargets() []protocol.RemoteDesktopTarget {
	return b.agent.RemoteDesktopTargets()
}

func (b *UIBridge) ConnectRemoteDesktop(targetID string, options protocol.RemoteDesktopConnectOptions) (protocol.RemoteDesktopSessionInfo, error) {
	return b.agent.ConnectRemoteDesktop(strings.TrimSpace(targetID), options)
}

func (b *UIBridge) DisconnectRemoteDesktop() {
	b.agent.DisconnectRemoteDesktop()
}

func (b *UIBridge) GetRemoteDesktopStatus() protocol.RemoteDesktopStatus {
	return b.agent.RemoteDesktopStatus()
}

func (b *UIBridge) GetRemoteDesktopStats() protocol.DesktopSessionStats {
	return b.agent.RemoteDesktopStats()
}

func (b *UIBridge) GetRemoteDesktopDiagnostics() desktop.DesktopDiagnosticsReport {
	return b.agent.RemoteDesktopDiagnostics()
}

func (b *UIBridge) ReportRemoteDesktopViewerStats(stats protocol.DesktopSessionStats) {
	b.agent.ReportRemoteDesktopViewerStats(stats)
}

func (b *UIBridge) GetRemoteDesktopFrame() protocol.RemoteDesktopFrame {
	return b.agent.RemoteDesktopFrame()
}

func (b *UIBridge) GetRemoteDesktopCursor(knownCursorID string) protocol.DesktopCursorState {
	return b.agent.RemoteDesktopCursor(strings.TrimSpace(knownCursorID))
}

func (b *UIBridge) GetRemoteDesktopClipboard(knownSequence uint64) protocol.DesktopClipboardState {
	return b.agent.RemoteDesktopClipboard(knownSequence)
}

func (b *UIBridge) SendRemoteDesktopClipboard(text string) error {
	return b.agent.SendRemoteDesktopClipboard(text)
}

func (b *UIBridge) SendRemoteDesktopInput(event protocol.DesktopInputEvent) error {
	return b.agent.SendRemoteDesktopInput(event)
}

func (b *UIBridge) SetRemoteDesktopResolution(width, height int) error {
	return b.agent.SetRemoteDesktopResolution(width, height)
}

func (b *UIBridge) RequestRemoteDesktopIDR() error {
	return b.agent.RequestRemoteDesktopIDR()
}

// GetLogs returns recent running logs
func (b *UIBridge) GetLogs(limit int) []app.LogEntry {
	return app.GlobalLogBuffer.Get(limit)
}

// ClearLogs drops the in-memory log history
func (b *UIBridge) ClearLogs() {
	app.GlobalLogBuffer.Clear()
}

// SetLogTap registers a live log listener (used to push lines into the UI).
func (b *UIBridge) SetLogTap(tap func(app.LogEntry)) {
	app.GlobalLogBuffer.SetTap(tap)
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// ConfigPath returns the config file the agent was started with.
func (b *UIBridge) ConfigPath() string {
	p := b.rawConfigPath()
	if p == "" {
		return "(未指定)"
	}
	return p
}

// rawConfigPath returns the absolute config path, or "" when none was given.
func (b *UIBridge) rawConfigPath() string {
	b.mu.RLock()
	path := b.configPath
	b.mu.RUnlock()
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// GetConfig returns the on-disk configuration (falling back to the running
// agent's view when the file cannot be read).
func (b *UIBridge) GetConfig() config.AgentConfigFile {
	if state, err := b.GetConfigState(); err == nil {
		return state.Config
	}
	return b.runtimeConfig()
}

type ConfigState struct {
	Config          config.AgentConfigFile
	Runtime         config.AgentConfigFile
	Revision        string
	RestartRequired bool
	RestartFields   []string
	ReloadPending   bool
}

// GetConfigState never substitutes defaults for an unreadable configuration.
// The UI must disable saving if the desired configuration could not be loaded.
func (b *UIBridge) GetConfigState() (*ConfigState, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.configPath == "" {
		return nil, fmt.Errorf("未指定配置文件")
	}
	cfg, revision, err := config.LoadAgentConfigWithRevision(b.configPath)
	if err != nil {
		return nil, err
	}
	return b.configState(cfg, revision), nil
}

func (b *UIBridge) configState(cfg *config.AgentConfigFile, revision string) *ConfigState {
	runtime := b.runtimeConfig()
	fields := restartFields(cfg, &runtime)
	return &ConfigState{Config: *cfg, Runtime: runtime, Revision: revision,
		RestartRequired: len(fields) > 0, RestartFields: fields,
		ReloadPending: policyFingerprint(cfg) != policyFingerprint(&runtime)}
}

func (b *UIBridge) runtimeConfig() config.AgentConfigFile {
	c := b.agent.Config()
	var res config.AgentConfigFile
	res.Server.Address = c.ServerAddress
	res.Server.QUICPort = c.QUICPort
	res.Server.TCPPort = c.TCPPort
	res.Server.TLSEnabled = config.BoolPtr(!c.PlainTCP)
	res.Device.Name = c.DeviceName
	res.Transport.Mode = c.TransportMode
	res.Proxy.DefaultExitID = c.DefaultExitID
	res.Proxy.SOCKS5.Enabled = c.SOCKS5Enabled
	res.Proxy.SOCKS5.Listen, res.Proxy.SOCKS5.Port = splitListen(c.SOCKS5Listen)
	res.Proxy.HTTP.Enabled = c.HTTPEnabled
	res.Proxy.HTTP.Listen, res.Proxy.HTTP.Port = splitListen(c.HTTPListen)
	res.Exit.Enabled = c.ExitEnabled
	res.RDP.Enabled = c.RDPEnabled
	res.RDP.Address = c.RDPAddress
	res.Exit.AllowInternet = c.AllowInternet
	res.Exit.AllowPrivateNetwork = c.AllowPrivateNet
	res.Exit.AllowLoopback = c.AllowLoopback
	res.Exit.Access.Mode = c.AccessMode
	res.Exit.Access.Domains = c.AccessDomains
	res.Exit.Access.CIDRs = c.AccessCIDRs
	res.Network.Mode = c.NetworkMode
	res.Network.ExcludeProcesses = c.DivertConfig.ExcludeProcesses
	res.Routing = c.Routing
	return res
}

// ConfigUpdate carries the subset of settings the desktop UI is allowed to
// change. Pointer fields mean "leave unchanged when nil".
type ConfigUpdate struct {
	Revision *string `json:"revision,omitempty"`
	Server   struct {
		Address    *string `json:"address"`
		QUICPort   *int    `json:"quicPort"`
		TCPPort    *int    `json:"tcpPort"`
		TLSEnabled *bool   `json:"tlsEnabled"`
	} `json:"server"`
	Device struct {
		Name *string `json:"name"`
	} `json:"device"`
	Transport *string `json:"transport"`
	Proxy     struct {
		SOCKS5Enabled *bool   `json:"socks5Enabled"`
		SOCKS5Listen  *string `json:"socks5Listen"`
		SOCKS5Port    *int    `json:"socks5Port"`
		HTTPEnabled   *bool   `json:"httpEnabled"`
		HTTPListen    *string `json:"httpListen"`
		HTTPPort      *int    `json:"httpPort"`
		DefaultExitID *string `json:"defaultExitId"`
	} `json:"proxy"`
	Exit struct {
		Enabled             *bool `json:"enabled"`
		AllowInternet       *bool `json:"allowInternet"`
		AllowPrivateNetwork *bool `json:"allowPrivateNetwork"`
		AllowLoopback       *bool `json:"allowLoopback"`
		Access              struct {
			Mode    *string   `json:"mode"`    // "" (off) | "allow" | "deny"
			Domains *[]string `json:"domains"` // glob / .suffix / exact, one per line
			CIDRs   *[]string `json:"cidrs"`   // CIDR / single IP / range, one per line
		} `json:"access"`
	} `json:"exit"`
	Network struct {
		Mode             *string   `json:"mode"` // "" (off) | "divert"
		ExcludeProcesses *[]string `json:"excludeProcesses"`
	} `json:"network"`
	Routing *RoutingConfigUpdate `json:"routing"`
	GUI     struct {
		Enabled        *bool   `json:"enabled"`
		MinimizeToTray *bool   `json:"minimizeToTray"`
		StartMinimized *bool   `json:"startMinimized"`
		Theme          *string `json:"theme"`
	} `json:"gui"`
}

type RoutingConfigUpdate struct {
	Mode          *string        `json:"mode"`
	DefaultAction *string        `json:"default_action"`
	Rules         []routing.Rule `json:"rules"` // Full replacement
}

// SaveResult tells the UI whether the change took effect immediately.
type SaveResult struct {
	OK              bool     `json:"ok"`
	RestartRequired bool     `json:"restartRequired"`
	Message         string   `json:"message"`
	Revision        string   `json:"revision"`
	RestartFields   []string `json:"restartFields"`
	ReloadPending   bool     `json:"reloadPending"`
}

// SaveConfig validates and persists a config update, then applies the parts that
// can change without restarting the agent.
func (b *UIBridge) SaveConfig(in ConfigUpdate) (*SaveResult, error) {
	return b.saveConfig(in, false)
}

func (b *UIBridge) saveConfig(in ConfigUpdate, reload bool) (*SaveResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	path := b.configPath

	if path == "" {
		return nil, fmt.Errorf("未指定配置文件，无法保存")
	}

	cfg, revision, err := config.LoadAgentConfigWithRevision(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	if in.Revision != nil && *in.Revision != revision {
		return nil, fmt.Errorf("配置已被其他操作修改，请重新载入后再保存")
	}

	if in.Server.Address != nil {
		addr := strings.TrimSpace(*in.Server.Address)
		if addr == "" {
			return nil, fmt.Errorf("中继服务器地址不能为空")
		}
		cfg.Server.Address = addr
	}
	if in.Server.QUICPort != nil {
		if err := checkPort(*in.Server.QUICPort, "QUIC 端口"); err != nil {
			return nil, err
		}
		cfg.Server.QUICPort = *in.Server.QUICPort
	}
	if in.Server.TCPPort != nil {
		if err := checkPort(*in.Server.TCPPort, "TLS/TCP 端口"); err != nil {
			return nil, err
		}
		cfg.Server.TCPPort = *in.Server.TCPPort
	}
	if in.Server.TLSEnabled != nil {
		cfg.Server.TLSEnabled = config.BoolPtr(*in.Server.TLSEnabled)
	}
	if in.Device.Name != nil {
		cfg.Device.Name = strings.TrimSpace(*in.Device.Name)
	}
	if in.Transport != nil {
		mode := strings.ToLower(strings.TrimSpace(*in.Transport))
		switch mode {
		case "auto", "quic_only", "tcp_only":
			cfg.Transport.Mode = mode
		default:
			return nil, fmt.Errorf("传输模式必须是 auto / quic_only / tcp_only")
		}
	}

	if in.Proxy.SOCKS5Enabled != nil {
		cfg.Proxy.SOCKS5.Enabled = config.BoolPtr(*in.Proxy.SOCKS5Enabled)
	}
	if in.Proxy.SOCKS5Listen != nil {
		cfg.Proxy.SOCKS5.Listen = strings.TrimSpace(*in.Proxy.SOCKS5Listen)
	}
	if in.Proxy.SOCKS5Port != nil {
		if err := checkPort(*in.Proxy.SOCKS5Port, "SOCKS5 端口"); err != nil {
			return nil, err
		}
		cfg.Proxy.SOCKS5.Port = *in.Proxy.SOCKS5Port
	}
	if in.Proxy.HTTPEnabled != nil {
		cfg.Proxy.HTTP.Enabled = config.BoolPtr(*in.Proxy.HTTPEnabled)
	}
	if in.Proxy.HTTPListen != nil {
		cfg.Proxy.HTTP.Listen = strings.TrimSpace(*in.Proxy.HTTPListen)
	}
	if in.Proxy.HTTPPort != nil {
		if err := checkPort(*in.Proxy.HTTPPort, "HTTP 代理端口"); err != nil {
			return nil, err
		}
		cfg.Proxy.HTTP.Port = *in.Proxy.HTTPPort
	}
	if in.Proxy.DefaultExitID != nil {
		cfg.Proxy.DefaultExitID = strings.TrimSpace(*in.Proxy.DefaultExitID)
	}

	if in.Exit.Enabled != nil {
		cfg.Exit.Enabled = config.BoolPtr(*in.Exit.Enabled)
	}
	if in.Exit.AllowInternet != nil {
		cfg.Exit.AllowInternet = *in.Exit.AllowInternet
	}
	if in.Exit.AllowPrivateNetwork != nil {
		cfg.Exit.AllowPrivateNetwork = *in.Exit.AllowPrivateNetwork
	}
	if in.Exit.AllowLoopback != nil {
		cfg.Exit.AllowLoopback = *in.Exit.AllowLoopback
	}
	if in.Exit.Access.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*in.Exit.Access.Mode))
		switch mode {
		case "", "allow", "deny":
			cfg.Exit.Access.Mode = mode
		default:
			return nil, fmt.Errorf("访问控制模式必须是 allow / deny（或留空关闭）")
		}
	}
	if in.Exit.Access.Domains != nil {
		cfg.Exit.Access.Domains = cleanLines(*in.Exit.Access.Domains)
	}
	if in.Exit.Access.CIDRs != nil {
		cfg.Exit.Access.CIDRs = cleanLines(*in.Exit.Access.CIDRs)
	}

	if in.Network.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*in.Network.Mode))
		switch mode {
		case "", "divert":
			cfg.Network.Mode = mode
		case "tun":
			return nil, fmt.Errorf("network.mode=tun 已废弃，请使用 divert")
		default:
			return nil, fmt.Errorf("网络模式必须是 divert（或留空关闭）")
		}
	}
	if in.Network.ExcludeProcesses != nil {
		cfg.Network.ExcludeProcesses = cleanLines(*in.Network.ExcludeProcesses)
	}

	if in.Routing != nil {
		if in.Routing.Mode != nil {
			mode := strings.ToLower(strings.TrimSpace(*in.Routing.Mode))
			switch routing.Mode(mode) {
			case routing.ModeRule, routing.ModeGlobalProxy, routing.ModeDirect:
				cfg.Routing.Mode = routing.Mode(mode)
			default:
				return nil, fmt.Errorf("路由模式必须是 rule / global_proxy / direct")
			}
		}
		if in.Routing.DefaultAction != nil {
			action := strings.ToUpper(strings.TrimSpace(*in.Routing.DefaultAction))
			switch routing.Action(action) {
			case routing.ActionProxy, routing.ActionDirect, routing.ActionReject:
				cfg.Routing.DefaultAction = routing.Action(action)
			default:
				return nil, fmt.Errorf("默认路由动作必须是 PROXY / DIRECT / REJECT")
			}
		}
		if in.Routing.Rules != nil {
			cfg.Routing.Rules = in.Routing.Rules
		}
	}

	if in.GUI.Enabled != nil {
		cfg.GUI.Enabled = config.BoolPtr(*in.GUI.Enabled)
	}
	if in.GUI.MinimizeToTray != nil {
		cfg.GUI.MinimizeToTray = config.BoolPtr(*in.GUI.MinimizeToTray)
	}
	if in.GUI.StartMinimized != nil {
		cfg.GUI.StartMinimized = *in.GUI.StartMinimized
	}
	if in.GUI.Theme != nil {
		cfg.GUI.Theme = strings.ToLower(strings.TrimSpace(*in.GUI.Theme))
	}

	if err := config.NormalizeAgentConfig(cfg); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}
	if cfg.Network.Mode == "divert" {
		if err := divert.Preflight(cfg.DivertConfig()); err != nil {
			return nil, fmt.Errorf("无法启用系统透明代理，配置未保存: %w", err)
		}
	}
	if b.agent.Config().NetworkMode == "divert" {
		if err := divert.ValidatePlatformRules(cfg.DivertConfig()); err != nil {
			return nil, fmt.Errorf("当前透明代理无法应用这些规则，配置未保存: %w", err)
		}
	}
	var rollbackAutoStart func() error
	if in.Network.Mode != nil || (reload && cfg.Network.Mode != b.agent.Config().NetworkMode) {
		rollbackAutoStart, err = b.syncAutoStart(path, cfg.Network.Mode == "divert")
		if err != nil {
			return nil, fmt.Errorf("更新开机自启权限失败: %w", err)
		}
	}
	if !reload {
		if err := b.writeConfig(path, cfg); err != nil {
			if rollbackAutoStart != nil {
				if rollbackErr := rollbackAutoStart(); rollbackErr != nil {
					return nil, errors.Join(fmt.Errorf("写入配置失败: %w", err), fmt.Errorf("恢复原自启动设置失败: %w", rollbackErr))
				}
			}
			return nil, fmt.Errorf("写入配置失败: %w", err)
		}
		revision = config.AgentConfigRevision(cfg)
	}
	// The same validators ran before persistence. Publish both policy sets only
	// after saving; mode, credentials and ACL remain the actual startup values.
	dcfg := cfg.DivertConfig()
	dcfg.Mode = b.agent.Config().NetworkMode
	if err := b.agent.ApplyPolicies(cfg.Routing, dcfg); err != nil {
		return nil, fmt.Errorf("配置已保存在磁盘，但应用失败: %w", err)
	}
	b.agent.SelectExit(cfg.Proxy.DefaultExitID)
	state := b.configState(cfg, revision)
	message := "配置已保存，规则已应用。"
	if reload {
		message = "已重新读取配置并应用规则。"
	}
	if state.RestartRequired {
		message += " 以下设置仍需重启客户端：" + strings.Join(state.RestartFields, "、") + "。"
	}
	return &SaveResult{OK: true, RestartRequired: state.RestartRequired, Message: message,
		Revision: revision, RestartFields: state.RestartFields, ReloadPending: state.ReloadPending}, nil
}

// ReloadConfig applies validated hot settings from disk without rewriting it.
func (b *UIBridge) ReloadConfig() (*SaveResult, error) {
	return b.saveConfig(ConfigUpdate{}, true)
}

func startupSettings(c *config.AgentConfigFile) map[string]any {
	name := c.Device.Name
	if name == "" {
		name = "Relay-Agent"
	}
	enabled := func(v *bool) bool { return v == nil || *v }
	return map[string]any{
		"中继地址": c.Server.Address, "QUIC 端口": c.Server.QUICPort, "TCP 端口": c.Server.TCPPort,
		"设备名称": name, "传输模式": c.Transport.Mode, "TLS 开关": c.IsServerTLSEnabled(),
		"SOCKS5 开关": enabled(c.Proxy.SOCKS5.Enabled), "SOCKS5 地址": c.Proxy.SOCKS5.Listen,
		"SOCKS5 端口": c.Proxy.SOCKS5.Port, "HTTP 开关": enabled(c.Proxy.HTTP.Enabled),
		"HTTP 地址": c.Proxy.HTTP.Listen, "HTTP 端口": c.Proxy.HTTP.Port,
		"出口开关": enabled(c.Exit.Enabled), "互联网访问": c.Exit.AllowInternet,
		"私网访问": c.Exit.AllowPrivateNetwork, "回环访问": c.Exit.AllowLoopback,
		"访问控制模式": c.Exit.Access.Mode, "访问域名": strings.Join(c.Exit.Access.Domains, "\n"),
		"访问地址": strings.Join(c.Exit.Access.CIDRs, "\n"), "透明代理开关": c.Network.Mode,
	}
}

func restartFields(desired, running *config.AgentConfigFile) []string {
	want, active := startupSettings(desired), startupSettings(running)
	fields := []string{}
	for key, value := range want {
		if !reflect.DeepEqual(value, active[key]) {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func policyFingerprint(c *config.AgentConfigFile) string {
	r := c.Routing
	if r.Rules == nil {
		r.Rules = []routing.Rule{}
	}
	d := c.DivertConfig()
	d.Mode = "" // mode is a startup setting, never published by a policy reload.
	if d.Rules == nil {
		d.Rules = []divert.Rule{}
	}
	if d.ExcludeProcesses == nil {
		d.ExcludeProcesses = []string{}
	}
	// YAML and JSON represent omitted and empty selectors differently, while
	// both mean an unconstrained selector to the matcher.
	d.Rules = append([]divert.Rule{}, d.Rules...)
	for i := range d.Rules {
		rule := &d.Rules[i]
		if rule.Hosts == nil {
			rule.Hosts = []string{}
		}
		if rule.CIDRs == nil {
			rule.CIDRs = []string{}
		}
		if rule.Ports == nil {
			rule.Ports = []string{}
		}
		if rule.Protocols == nil {
			rule.Protocols = []string{}
		}
	}
	data, _ := json.Marshal(struct {
		Routing routing.Config
		Divert  divert.Config
		ExitID  string
	}{r, d, c.Proxy.DefaultExitID})
	return string(data)
}

func splitListen(addr string) (string, int) {
	host, rawPort, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	port, _ := strconv.Atoi(rawPort)
	return host, port
}

func checkPort(p int, label string) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("%s 必须在 1-65535 之间", label)
	}
	return nil
}

// cleanLines trims whitespace and drops empty entries from a textarea-style
// list so each access entry is stored exactly once and free of stray blanks.
func cleanLines(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SelectExit dynamically switches the target exit node and persists the choice.
func (b *UIBridge) SelectExit(exitID string) error {
	exitID = strings.TrimSpace(exitID)
	var in ConfigUpdate
	in.Proxy.DefaultExitID = &exitID
	_, err := b.SaveConfig(in)
	return err
}

// ---------------------------------------------------------------------------
// Autostart
// ---------------------------------------------------------------------------

// IsAutoStart reports whether a login task or ordinary Run entry is enabled.
func (b *UIBridge) IsAutoStart() bool {
	return startup.IsAutoStartEnabled(autoStartName)
}

// SetAutoStart follows the saved mode, including changes awaiting a restart.
func (b *UIBridge) SetAutoStart(enable bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	requireAdmin := false
	if enable {
		cfg, err := config.LoadAgentConfig(b.configPath)
		if err != nil {
			return fmt.Errorf("读取自启动配置失败: %w", err)
		}
		requireAdmin = cfg.Network.Mode == "divert"
	}
	return b.setAutoStart(b.configPath, enable, requireAdmin)
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Close terminates the agent
func (b *UIBridge) Close() error {
	return b.agent.Close()
}

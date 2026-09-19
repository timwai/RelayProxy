package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
)

type ServerConfig struct {
	RelayACL *RelayACLConfig `yaml:"relay_acl"`
	Server   struct {
		QUIC struct {
			Listen string `yaml:"listen"` // e.g. ":443"
		} `yaml:"quic"`
		TLS struct {
			Listen string `yaml:"listen"` // e.g. ":443"
		} `yaml:"tls"`
		Admin struct {
			Listen     string `yaml:"listen"`                // e.g. ":8443"
			TLSEnabled *bool  `yaml:"tls_enabled,omitempty"` // nil inherits server.tls_enabled
		} `yaml:"admin"`
		CertFile   string `yaml:"cert_file"`
		KeyFile    string `yaml:"key_file"`
		TLSEnabled *bool  `yaml:"tls_enabled"` // nil or true = TLS on; false = plaintext
	} `yaml:"server"`

	Database struct {
		Driver string `yaml:"driver"` // V1: "sqlite" only
		DSN    string `yaml:"dsn"`    // e.g. "relayproxy.db"
	} `yaml:"database"`

	Tunnel struct {
		HeartbeatSec            int `yaml:"heartbeat_sec"`
		MaxConnectionsPerDevice int `yaml:"max_connections_per_device"`
		MaxConnections          int `yaml:"max_connections"` // global tunnel sessions
	} `yaml:"tunnel"`

	RDP struct {
		LeaseSec            int    `yaml:"lease_sec"`
		RendezvousListen    string `yaml:"rendezvous_listen"`
		RendezvousAdvertise string `yaml:"rendezvous_advertise"`
		Ingress             struct {
			Enabled         *bool    `yaml:"enabled"`
			Listen          string   `yaml:"listen"`
			PortStart       int      `yaml:"port_start"`
			PortEnd         int      `yaml:"port_end"`
			SourceCIDRs     []string `yaml:"source_cidrs"`
			RateLimitPerMin int      `yaml:"rate_limit_per_minute"`
		} `yaml:"ingress"`
	} `yaml:"rdp"`

	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`
}

type AgentConfigFile struct {
	Server struct {
		Address    string `yaml:"address"` // e.g. "127.0.0.1" or "relay.example.com"
		QUICPort   int    `yaml:"quic_port"`
		TCPPort    int    `yaml:"tcp_port"`
		TLSEnabled *bool  `yaml:"tls_enabled"` // nil or true = TLS on; false = plaintext TCP
	} `yaml:"server"`

	Device struct {
		Name string `yaml:"name"`
	} `yaml:"device"`

	Transport struct {
		Mode string `yaml:"mode"` // "auto", "quic_only", "tcp_only"
	} `yaml:"transport"`

	Mode string `yaml:"-"` // runtime-only capability view; grants come from the server

	Proxy struct {
		SOCKS5 struct {
			Enabled *bool  `yaml:"enabled"`
			Listen  string `yaml:"listen"`
			Port    int    `yaml:"port"`
		} `yaml:"socks5"`
		HTTP struct {
			Enabled *bool  `yaml:"enabled"`
			Listen  string `yaml:"listen"`
			Port    int    `yaml:"port"`
		} `yaml:"http"`
		DefaultExitID string `yaml:"default_exit_id"`
	} `yaml:"proxy"`

	RDP struct {
		Enabled *bool  `yaml:"enabled"`
		Address string `yaml:"address"` // target-local RDP service; defaults to 127.0.0.1:3389
	} `yaml:"rdp"`

	Routing routing.Config `yaml:"routing"` // 新增路由配置

	Exit struct {
		Enabled             *bool `yaml:"enabled"`
		AllowInternet       bool  `yaml:"allow_internet"`
		AllowPrivateNetwork bool  `yaml:"allow_private_network"`
		AllowLoopback       bool  `yaml:"allow_loopback"`
		Access              struct {
			// Mode is "" (no gate), "allow" (whitelist) or "deny" (blacklist).
			Mode    string   `yaml:"mode"`
			Domains []string `yaml:"domains"` // domain patterns: glob (*/?), ".suffix", exact
			CIDRs   []string `yaml:"cidrs"`   // IP ranges: CIDR / single IP / "start-end"
		} `yaml:"access"`
	} `yaml:"exit"`

	Network struct {
		// Mode: "" (off, use SOCKS5/HTTP) or "divert" (system intercept via
		// WinDivert / iptables / Network Extension). Legacy "tun" is rejected.
		Mode             string   `yaml:"mode"`
		DNSMode          string   `yaml:"dns_mode"`
		ExcludeProcesses []string `yaml:"exclude_processes"`
	} `yaml:"network"`

	GUI struct {
		Enabled        *bool  `yaml:"enabled"`          // Default: true — launch the desktop window on start
		MinimizeToTray *bool  `yaml:"minimize_to_tray"` // Default: true — closing the window hides to the tray
		StartMinimized bool   `yaml:"start_minimized"`  // Default: false — boot straight into the tray
		Theme          string `yaml:"theme"`            // "dark", "light", or "system"
	} `yaml:"gui"`

	Web struct {
		Enabled *bool  `yaml:"enabled"`
		Listen  string `yaml:"listen"`
		Port    int    `yaml:"port"`
		Token   string `yaml:"token,omitempty"` // Deprecated: accepted for old configs, ignored; Agent web UI is loopback-only
	} `yaml:"web"`

	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`
}

func LoadServerConfig(path string) (*ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read server config file failed: %w", err)
	}
	return parseServerConfig(data)
}

func parseServerConfig(data []byte) (*ServerConfig, error) {
	cfg := &ServerConfig{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && err != io.EOF {
		return nil, fmt.Errorf("unmarshal server config failed: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("unmarshal server config failed: %w", err)
		}
		return nil, fmt.Errorf("server config must contain a single YAML document")
	}

	if err := NormalizeServerConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyServerDefaults(cfg *ServerConfig) {
	if cfg.Server.TLSEnabled == nil {
		cfg.Server.TLSEnabled = BoolPtr(true)
	}
	if cfg.Server.QUIC.Listen == "" {
		cfg.Server.QUIC.Listen = ":443"
	}
	if cfg.Server.TLS.Listen == "" {
		cfg.Server.TLS.Listen = ":443"
	}
	if cfg.Server.Admin.Listen == "" {
		cfg.Server.Admin.Listen = ":8443"
	}
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "sqlite"
	}
	if cfg.Database.DSN == "" {
		cfg.Database.DSN = "relayproxy.db"
	}
	if cfg.Tunnel.HeartbeatSec == 0 {
		cfg.Tunnel.HeartbeatSec = 15
	}
	if cfg.Tunnel.MaxConnectionsPerDevice == 0 {
		cfg.Tunnel.MaxConnectionsPerDevice = 1024
	}
	if cfg.Tunnel.MaxConnections == 0 {
		cfg.Tunnel.MaxConnections = 2048
	}
	if cfg.RDP.LeaseSec == 0 {
		cfg.RDP.LeaseSec = 60
	}
	if cfg.RDP.RendezvousListen == "" {
		cfg.RDP.RendezvousListen = ""
	}
	if cfg.RDP.Ingress.PortStart == 0 {
		cfg.RDP.Ingress.PortStart = 33900
	}
	if cfg.RDP.Ingress.PortEnd == 0 {
		cfg.RDP.Ingress.PortEnd = 34000
	}
	if cfg.RDP.Ingress.Listen == "" {
		cfg.RDP.Ingress.Listen = "0.0.0.0:0"
	}
	if cfg.RDP.Ingress.RateLimitPerMin == 0 {
		cfg.RDP.Ingress.RateLimitPerMin = 120
	}
	if cfg.RDP.Ingress.Enabled == nil {
		cfg.RDP.Ingress.Enabled = BoolPtr(false)
	}
	if cfg.RelayACL == nil {
		cfg.RelayACL = &RelayACLConfig{}
	}
	if cfg.RelayACL.AllowInternet == nil {
		cfg.RelayACL.AllowInternet = BoolPtr(true)
	}
	if cfg.RelayACL.Access.Domains == nil {
		cfg.RelayACL.Access.Domains = []string{}
	}
	if cfg.RelayACL.Access.CIDRs == nil {
		cfg.RelayACL.Access.CIDRs = []string{}
	}
}

func LoadAgentConfig(path string) (*AgentConfigFile, error) {
	cfg, _, err := LoadAgentConfigWithRevision(path)
	return cfg, err
}

// LoadAgentConfigWithRevision binds the decoded config to the exact file read.
// The revision lets a UI reject a stale read/modify/write operation.
func LoadAgentConfigWithRevision(path string) (*AgentConfigFile, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read agent config file failed: %w", err)
	}

	cfg := &AgentConfigFile{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, "", fmt.Errorf("unmarshal agent config failed: %w", err)
	}

	if strings.EqualFold(strings.TrimSpace(cfg.Network.Mode), "tun") {
		return nil, "", fmt.Errorf("network.mode=tun 已废弃：请改为 divert（系统透明代理）或留空仅使用 SOCKS5/HTTP")
	}

	if err := NormalizeAgentConfig(cfg); err != nil {
		return nil, "", err
	}
	return cfg, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func SaveAgentConfig(path string, cfg *AgentConfigFile) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}
	prepared := CloneAgentConfig(cfg)
	if err := NormalizeAgentConfig(prepared); err != nil {
		return err
	}
	data, err := yaml.Marshal(prepared)
	if err != nil {
		return fmt.Errorf("marshal agent config failed: %w", err)
	}
	if err := atomicWriteConfig(path, data); err != nil {
		return fmt.Errorf("write agent config failed: %w", err)
	}
	return nil
}

// SaveBootstrapAgentConfig writes the intentionally tiny first-run file. All
// omitted values are safe defaults applied in memory by NormalizeAgentConfig.
func SaveBootstrapAgentConfig(path, serverAddress string) error {
	bootstrap := struct {
		Server struct {
			Address string `yaml:"address"`
		} `yaml:"server"`
	}{}
	bootstrap.Server.Address = strings.TrimSpace(serverAddress)
	if bootstrap.Server.Address == "" {
		bootstrap.Server.Address = "127.0.0.1"
	}
	data, err := yaml.Marshal(bootstrap)
	if err != nil {
		return fmt.Errorf("marshal bootstrap agent config failed: %w", err)
	}
	if err := atomicWriteConfig(path, data); err != nil {
		return fmt.Errorf("write bootstrap agent config failed: %w", err)
	}
	return nil
}

func applyAgentDefaults(cfg *AgentConfigFile) {
	if cfg.Server.Address == "" {
		cfg.Server.Address = "127.0.0.1"
	}
	if cfg.Server.TCPPort == 0 {
		cfg.Server.TCPPort = 443
	}
	if cfg.Server.QUICPort == 0 {
		cfg.Server.QUICPort = 443
	}
	if cfg.Transport.Mode == "" {
		cfg.Transport.Mode = "auto"
	}
	if cfg.Mode == "" {
		cfg.Mode = "CLIENT"
	}
	if cfg.Proxy.SOCKS5.Listen == "" {
		cfg.Proxy.SOCKS5.Listen = "127.0.0.1"
	}
	if cfg.Proxy.SOCKS5.Port == 0 {
		cfg.Proxy.SOCKS5.Port = 1080
	}
	if cfg.Proxy.HTTP.Listen == "" {
		cfg.Proxy.HTTP.Listen = "127.0.0.1"
	}
	if cfg.Proxy.HTTP.Port == 0 {
		cfg.Proxy.HTTP.Port = 8080
	}
	if cfg.RDP.Address == "" {
		cfg.RDP.Address = "127.0.0.1:3389"
	}
	if cfg.GUI.Theme == "" {
		cfg.GUI.Theme = "system"
	}
	if cfg.Web.Listen == "" {
		cfg.Web.Listen = "127.0.0.1"
	}
	if cfg.Web.Port == 0 {
		cfg.Web.Port = 9090
	}
	// Materialize every effective boolean default. Keeping these fields nil made
	// the runtime behave as enabled while a later full save serialized them as
	// YAML null, leaving the generated configuration ambiguous to operators and
	// other tooling.
	for _, field := range []**bool{
		&cfg.Server.TLSEnabled,
		&cfg.Proxy.SOCKS5.Enabled,
		&cfg.Proxy.HTTP.Enabled,
		&cfg.RDP.Enabled,
		&cfg.Exit.Enabled,
		&cfg.GUI.Enabled,
		&cfg.GUI.MinimizeToTray,
		&cfg.Web.Enabled,
	} {
		if *field == nil {
			*field = BoolPtr(true)
		}
	}
	if cfg.Network.DNSMode == "" {
		cfg.Network.DNSMode = divert.DNSModeAuto
	}
	if cfg.Network.ExcludeProcesses == nil {
		cfg.Network.ExcludeProcesses = []string{"relayproxy", "relayproxy.exe", "RelayProxy.exe"}
	}
	if cfg.Exit.Access.Domains == nil {
		cfg.Exit.Access.Domains = []string{}
	}
	if cfg.Exit.Access.CIDRs == nil {
		cfg.Exit.Access.CIDRs = []string{}
	}

	// Only a completely omitted routing configuration receives the sample
	// rules. An explicit mode, fallback or empty list expresses user intent.
	if cfg.Routing.Mode == "" && cfg.Routing.DefaultAction == "" && cfg.Routing.Rules == nil {
		cfg.Routing = routing.DefaultConfig()
	}
	if cfg.Routing.Mode == "" {
		cfg.Routing.Mode = routing.ModeRule
	}
	if cfg.Routing.DefaultAction == "" {
		cfg.Routing.DefaultAction = routing.ActionProxy
	}
	if cfg.Routing.Rules == nil {
		cfg.Routing.Rules = []routing.Rule{}
	}

}

// IsGUIEnabled reports whether the desktop window should be launched.
func (c *AgentConfigFile) IsGUIEnabled() bool {
	if c.GUI.Enabled != nil {
		return *c.GUI.Enabled
	}
	return true
}

// IsMinimizeToTray reports whether closing the window should hide it to the
// system tray instead of quitting.
func (c *AgentConfigFile) IsMinimizeToTray() bool {
	if c.GUI.MinimizeToTray != nil {
		return *c.GUI.MinimizeToTray
	}
	return true
}

// IsWebEnabled reports whether the embedded management page should be served.
// It defaults to enabled on loopback so a headless agent remains manageable.
func (c *AgentConfigFile) IsWebEnabled() bool {
	if c.Web.Enabled != nil {
		return *c.Web.Enabled
	}
	return true
}

func BoolPtr(v bool) *bool {
	return &v
}

// IsTLSEnabled reports whether the server should use TLS for agent tunnels.
// Defaults to true when the field is nil (omitted from config).
func (c *ServerConfig) IsTLSEnabled() bool {
	if c.Server.TLSEnabled != nil {
		return *c.Server.TLSEnabled
	}
	return true
}

// IsAdminTLSEnabled lets the management UI use HTTP independently of encrypted
// agent tunnels. Omitted settings retain the existing deployment behavior.
func (c *ServerConfig) IsAdminTLSEnabled() bool {
	if c.Server.Admin.TLSEnabled != nil {
		return *c.Server.Admin.TLSEnabled
	}
	return c.IsTLSEnabled()
}

func (c *ServerConfig) NeedsCertificate() bool {
	return c.IsTLSEnabled() || c.IsAdminTLSEnabled()
}

// IsServerTLSEnabled reports whether the agent should connect using TLS.
// Defaults to true when the field is nil (omitted from config).
func (c *AgentConfigFile) IsServerTLSEnabled() bool {
	if c.Server.TLSEnabled != nil {
		return *c.Server.TLSEnabled
	}
	return true
}

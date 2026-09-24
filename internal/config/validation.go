package config

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
	"relayproxy/internal/acl"
)

type AccessConfig struct {
	Mode    string   `yaml:"mode"`
	Domains []string `yaml:"domains"`
	CIDRs   []string `yaml:"cidrs"`
}

// RelayACLConfig keeps the existing relay defaults unless explicitly overridden.
// The relay and exit policies are independent gates; allowing an address here
// does not bypass the destination exit's local policy.
type RelayACLConfig struct {
	AllowInternet       *bool        `yaml:"allow_internet"`
	AllowPrivateNetwork bool         `yaml:"allow_private_network"`
	AllowLoopback       bool         `yaml:"allow_loopback"`
	Access              AccessConfig `yaml:"access"`
}

func (c *ServerConfig) RelayPolicy() acl.Policy {
	p := acl.Policy{ID: "relay_acl", Name: "Relay access policy", AllowInternet: true}
	if c.RelayACL == nil {
		return p
	}
	r := c.RelayACL
	if r.AllowInternet != nil {
		p.AllowInternet = *r.AllowInternet
	}
	p.AllowPrivateNetwork = r.AllowPrivateNetwork
	p.AllowLoopback = r.AllowLoopback
	p.AccessMode = acl.AccessMode(strings.ToLower(strings.TrimSpace(r.Access.Mode)))
	p.AccessHosts = slices.Clone(r.Access.Domains)
	p.AccessCIDRs = slices.Clone(r.Access.CIDRs)
	return p
}

func (c *AgentConfigFile) ExitPolicy() acl.Policy {
	return acl.Policy{
		AllowInternet: c.Exit.AllowInternet, AllowPrivateNetwork: c.Exit.AllowPrivateNetwork,
		AllowLoopback: c.Exit.AllowLoopback, AccessMode: acl.AccessMode(c.Exit.Access.Mode),
		AccessHosts: c.Exit.Access.Domains, AccessCIDRs: c.Exit.Access.CIDRs,
	}
}

func (c *AgentConfigFile) DivertConfig() divert.Config {
	return divert.Config{Mode: c.Network.Mode, ExcludeProcesses: c.Network.ExcludeProcesses}
}

func validateAccess(p acl.Policy) error {
	switch p.AccessMode {
	case acl.AccessModeNone, acl.AccessModeAllow, acl.AccessModeDeny:
	default:
		return fmt.Errorf("access.mode 必须是 allow / deny（或留空关闭）")
	}
	_, err := acl.NewChecker(p)
	return err
}

// NormalizeAgentConfig fills only omitted defaults, normalizes enums, and
// compiles every policy even when its runtime feature is currently disabled.
func NormalizeAgentConfig(c *AgentConfigFile) error {
	if c == nil {
		return fmt.Errorf("config cannot be nil")
	}
	c.Server.Address = strings.TrimSpace(c.Server.Address)
	c.Mode = strings.ToUpper(strings.TrimSpace(c.Mode))
	c.Transport.Mode = strings.ToLower(strings.TrimSpace(c.Transport.Mode))
	c.Network.Mode = strings.ToLower(strings.TrimSpace(c.Network.Mode))
	c.Exit.Access.Mode = strings.ToLower(strings.TrimSpace(c.Exit.Access.Mode))
	c.Routing.Mode = routing.Mode(strings.ToLower(strings.TrimSpace(string(c.Routing.Mode))))
	c.Routing.DefaultAction = routing.Action(strings.ToUpper(strings.TrimSpace(string(c.Routing.DefaultAction))))
	c.GUI.Theme = strings.ToLower(strings.TrimSpace(c.GUI.Theme))
	c.Proxy.SOCKS5.Listen = strings.TrimSpace(c.Proxy.SOCKS5.Listen)
	c.Proxy.HTTP.Listen = strings.TrimSpace(c.Proxy.HTTP.Listen)
	c.Web.Listen = strings.TrimSpace(c.Web.Listen)
	applyAgentDefaults(c)
	return ValidateAgentConfig(c)
}

// ValidateAgentConfig is also called before saving. No invalid rule is silently
// discarded, including a disabled rule that could later be enabled from the UI.
func ValidateAgentConfig(c *AgentConfigFile) error {
	if c == nil {
		return fmt.Errorf("config cannot be nil")
	}
	if c.Mode != "CLIENT" {
		return fmt.Errorf("客户端角色由服务端授权，配置文件不再接受 mode")
	}
	switch c.Transport.Mode {
	case "auto", "quic_only", "tcp_only":
	default:
		return fmt.Errorf("transport.mode 必须是 auto / quic_only / tcp_only")
	}
	for _, item := range []struct {
		name string
		port int
	}{
		{"server.quic_port", c.Server.QUICPort}, {"server.tcp_port", c.Server.TCPPort},
		{"proxy.socks5.port", c.Proxy.SOCKS5.Port},
		{"proxy.http.port", c.Proxy.HTTP.Port},
	} {
		if item.port < 1 || item.port > 65535 {
			return fmt.Errorf("%s 必须在 1-65535 之间", item.name)
		}
	}
	for _, item := range []struct{ name, host string }{
		{"server.address", c.Server.Address}, {"proxy.socks5.listen", c.Proxy.SOCKS5.Listen},
		{"proxy.http.listen", c.Proxy.HTTP.Listen}, {"web.listen", c.Web.Listen},
	} {
		if item.host == "" || strings.ContainsAny(item.host, " /\\\t\r\n") {
			return fmt.Errorf("%s 必须是主机名或 IP，不包含协议和端口", item.name)
		}
		if strings.ContainsAny(item.host, ":[]") {
			if _, err := netip.ParseAddr(item.host); err != nil {
				return fmt.Errorf("%s 必须是主机名或 IP，不包含协议和端口", item.name)
			}
		}
	}
	if c.Web.Port < 1 || c.Web.Port > 65535 {
		return fmt.Errorf("web.port 必须在 1-65535 之间")
	}
	if c.IsWebEnabled() && !isLoopbackHost(c.Web.Listen) {
		return fmt.Errorf("web.listen: Agent Web 管理仅允许监听本机 loopback 地址")
	}
	if c.GUI.Theme != "dark" && c.GUI.Theme != "light" && c.GUI.Theme != "system" {
		return fmt.Errorf("gui.theme 必须是 dark / light / system")
	}
	if p := c.GUI.NativeViewer; p.Width != 0 || p.Height != 0 {
		if p.Width < 320 || p.Height < 180 || p.Width > 16384 || p.Height > 16384 {
			return fmt.Errorf("gui.native_viewer 尺寸必须在 320x180 到 16384x16384 之间")
		}
	}
	if c.Mode != "EXIT" && (c.Proxy.SOCKS5.Enabled == nil || *c.Proxy.SOCKS5.Enabled) && (c.Proxy.HTTP.Enabled == nil || *c.Proxy.HTTP.Enabled) &&
		listenAddressesOverlap(net.JoinHostPort(c.Proxy.SOCKS5.Listen, fmt.Sprint(c.Proxy.SOCKS5.Port)), net.JoinHostPort(c.Proxy.HTTP.Listen, fmt.Sprint(c.Proxy.HTTP.Port))) {
		return fmt.Errorf("SOCKS5 与 HTTP 代理的监听地址和端口冲突，请使用不同端口")
	}
	if err := validateAccess(c.ExitPolicy()); err != nil {
		return fmt.Errorf("exit.access: %w", err)
	}
	if err := routing.ValidateConfig(c.Routing); err != nil {
		return fmt.Errorf("routing: %w", err)
	}
	if err := divert.ValidateConfig(c.DivertConfig()); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	return nil
}

// AgentConfigRevision identifies the normalized bytes SaveAgentConfig writes.
func AgentConfigRevision(c *AgentConfigFile) string {
	copy := CloneAgentConfig(c)
	if err := NormalizeAgentConfig(copy); err != nil {
		return ""
	}
	data, err := yaml.Marshal(copy)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// CloneAgentConfig preserves nil versus explicitly empty slices.
func CloneAgentConfig(c *AgentConfigFile) *AgentConfigFile {
	if c == nil {
		return nil
	}
	out := *c
	cloneBool := func(v *bool) *bool {
		if v == nil {
			return nil
		}
		return BoolPtr(*v)
	}
	out.Proxy.SOCKS5.Enabled = cloneBool(c.Proxy.SOCKS5.Enabled)
	out.Server.TLSEnabled = cloneBool(c.Server.TLSEnabled)
	out.Proxy.HTTP.Enabled = cloneBool(c.Proxy.HTTP.Enabled)
	out.Exit.Enabled = cloneBool(c.Exit.Enabled)
	out.RDP.Enabled = cloneBool(c.RDP.Enabled)
	out.GUI.Enabled = cloneBool(c.GUI.Enabled)
	out.GUI.MinimizeToTray = cloneBool(c.GUI.MinimizeToTray)
	out.Web.Enabled = cloneBool(c.Web.Enabled)
	out.Exit.Access.Domains = slices.Clone(c.Exit.Access.Domains)
	out.Exit.Access.CIDRs = slices.Clone(c.Exit.Access.CIDRs)
	out.Routing = routing.CloneConfig(c.Routing)
	out.Network.ExcludeProcesses = slices.Clone(c.Network.ExcludeProcesses)
	return &out
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	return err == nil && addr.IsLoopback()
}

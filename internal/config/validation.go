package config

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
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

// ServerExitPolicy is the local egress policy enforced when the Relay server
// itself is selected as the network exit.
func (c *ServerConfig) ServerExitPolicy() acl.Policy {
	allowInternet := true
	if c.Exit.AllowInternet != nil {
		allowInternet = *c.Exit.AllowInternet
	}
	return acl.Policy{
		ID:                  "server_exit_acl",
		Name:                "Server exit policy",
		AllowInternet:       allowInternet,
		AllowPrivateNetwork: c.Exit.AllowPrivateNetwork,
		AllowLoopback:       c.Exit.AllowLoopback,
		AccessMode:          acl.AccessMode(strings.ToLower(strings.TrimSpace(c.Exit.Access.Mode))),
		AccessHosts:         slices.Clone(c.Exit.Access.Domains),
		AccessCIDRs:         slices.Clone(c.Exit.Access.CIDRs),
	}
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
	c.Device.IdentityID = strings.ToLower(strings.TrimSpace(c.Device.IdentityID))
	c.Mode = strings.ToUpper(strings.TrimSpace(c.Mode))
	c.Transport.Mode = strings.ToLower(strings.TrimSpace(c.Transport.Mode))
	c.P2P.Mode = strings.ToLower(strings.TrimSpace(c.P2P.Mode))
	c.Direct.Public.Advertise = strings.TrimSpace(c.Direct.Public.Advertise)
	c.Network.Mode = strings.ToLower(strings.TrimSpace(c.Network.Mode))
	// The short-lived global handle_direct_connections setting was replaced by
	// per-routing-rule handle_direct. Keep accepting old files, then drop it.
	c.Network.HandleDirectConnections = nil
	c.Exit.Access.Mode = strings.ToLower(strings.TrimSpace(c.Exit.Access.Mode))
	c.Exit.Upstream.Mode = strings.ToLower(strings.TrimSpace(c.Exit.Upstream.Mode))
	c.Exit.Upstream.Address = strings.TrimSpace(c.Exit.Upstream.Address)
	c.Exit.Upstream.Username = strings.TrimSpace(c.Exit.Upstream.Username)
	c.Exit.UpstreamExitID = strings.TrimSpace(c.Exit.UpstreamExitID)
	for i := range c.Proxy.CustomExits {
		e := &c.Proxy.CustomExits[i]
		e.ID = strings.TrimSpace(e.ID)
		e.Name = strings.TrimSpace(e.Name)
		e.Protocol = strings.ToLower(strings.TrimSpace(e.Protocol))
		e.Address = strings.TrimSpace(e.Address)
	}
	c.Routing.Mode = routing.Mode(strings.ToLower(strings.TrimSpace(string(c.Routing.Mode))))
	c.Routing.DefaultAction = routing.Action(strings.ToUpper(strings.TrimSpace(string(c.Routing.DefaultAction))))
	c.GUI.Theme = strings.ToLower(strings.TrimSpace(c.GUI.Theme))
	c.Proxy.SOCKS5.Listen = strings.TrimSpace(c.Proxy.SOCKS5.Listen)
	c.Proxy.HTTP.Listen = strings.TrimSpace(c.Proxy.HTTP.Listen)
	c.Web.Listen = strings.TrimSpace(c.Web.Listen)
	applyAgentDefaults(c)
	// QUIC requires TLS. Keep the persisted/desired transport aligned with what
	// relay-agent can actually run, otherwise tls_enabled=false + auto/quic_only
	// becomes tcp_only only at runtime and leaves the UI permanently reporting a
	// restart mismatch after restart.
	if !c.IsServerTLSEnabled() {
		c.Transport.Mode = "tcp_only"
		c.Server.InsecureTLS = false
	}
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
	if c.Device.IdentityID != "" && !validIdentityID(c.Device.IdentityID) {
		return fmt.Errorf("device.identity_id 必须是服务端生成的 16 位小写字母数字组合")
	}
	switch c.Transport.Mode {
	case "auto", "quic_only", "tcp_only":
	default:
		return fmt.Errorf("transport.mode 必须是 auto / quic_only / tcp_only")
	}
	switch c.P2P.Mode {
	case "auto", "direct_only", "relay_only", "p2p_only":
	default:
		return fmt.Errorf("p2p.mode 必须是 auto / direct_only / relay_only / p2p_only")
	}
	if c.P2P.PunchTimeoutMs < 100 || c.P2P.PunchTimeoutMs > 10000 {
		return fmt.Errorf("p2p.punch_timeout_ms 必须在 100-10000 之间")
	}
	if c.P2P.KeepaliveSec < 5 || c.P2P.KeepaliveSec > 60 {
		return fmt.Errorf("p2p.keepalive_sec 必须在 5-60 之间")
	}
	if c.P2P.IdleTimeoutSec < 30 || c.P2P.IdleTimeoutSec > 3600 {
		return fmt.Errorf("p2p.idle_timeout_sec 必须在 30-3600 之间")
	}
	if c.P2P.MaxExitSessions < 1 || c.P2P.MaxExitSessions > 32 {
		return fmt.Errorf("p2p.max_exit_sessions 必须在 1-32 之间")
	}
	if c.Direct.Public.Advertise != "" {
		host, rawPort, err := net.SplitHostPort(c.Direct.Public.Advertise)
		if err != nil || strings.TrimSpace(host) == "" || strings.ContainsAny(host, " /\\\t\r\n") {
			return fmt.Errorf("direct.public.advertise 必须是可路由的 host:port")
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("direct.public.advertise 端口必须在 1-65535 之间")
		}
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
	if c.IsWebEnabled() {
		token := strings.TrimSpace(c.Web.Token)
		if token != "" && len(token) < 32 {
			return fmt.Errorf("web.token 必须留空或至少包含 32 字节")
		}
	}
	if c.GUI.Theme != "dark" && c.GUI.Theme != "light" && c.GUI.Theme != "system" {
		return fmt.Errorf("gui.theme 必须是 dark / light / system")
	}
	if timeout := c.VerificationPopupTimeout(); timeout < 0 || timeout > 3600 {
		return fmt.Errorf("gui.verification_popup_timeout_sec 必须在 0-3600 之间")
	}
	if c.Mode != "EXIT" && (c.Proxy.SOCKS5.Enabled == nil || *c.Proxy.SOCKS5.Enabled) && (c.Proxy.HTTP.Enabled == nil || *c.Proxy.HTTP.Enabled) &&
		listenAddressesOverlap(net.JoinHostPort(c.Proxy.SOCKS5.Listen, fmt.Sprint(c.Proxy.SOCKS5.Port)), net.JoinHostPort(c.Proxy.HTTP.Listen, fmt.Sprint(c.Proxy.HTTP.Port))) {
		return fmt.Errorf("SOCKS5 与 HTTP 代理的监听地址和端口冲突，请使用不同端口")
	}
	switch c.Exit.Upstream.Mode {
	case "direct":
	case "socks5", "http", "https":
		host, rawPort, err := net.SplitHostPort(c.Exit.Upstream.Address)
		if err != nil || strings.TrimSpace(host) == "" {
			return fmt.Errorf("exit.upstream.address 必须是 host:port")
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("exit.upstream.address 端口必须在 1-65535 之间")
		}
		if isLoopbackHost(host) {
			socksEnabled := c.Proxy.SOCKS5.Enabled == nil || *c.Proxy.SOCKS5.Enabled
			httpEnabled := c.Proxy.HTTP.Enabled == nil || *c.Proxy.HTTP.Enabled
			if socksEnabled && port == c.Proxy.SOCKS5.Port && localListenerCoversLoopback(c.Proxy.SOCKS5.Listen) {
				return fmt.Errorf("exit.upstream.address 不能指向 RelayProxy 自己的 SOCKS5 监听端口")
			}
			if httpEnabled && port == c.Proxy.HTTP.Port && localListenerCoversLoopback(c.Proxy.HTTP.Listen) {
				return fmt.Errorf("exit.upstream.address 不能指向 RelayProxy 自己的 HTTP 监听端口")
			}
		}
	default:
		return fmt.Errorf("exit.upstream.mode 必须是 direct / socks5 / http / https")
	}
	if len(c.Exit.Upstream.Username) > 255 || len(c.Exit.Upstream.Password) > 255 {
		return fmt.Errorf("exit.upstream 用户名和密码长度不能超过 255")
	}
	if err := routing.ValidateCustomExits(c.Proxy.CustomExits); err != nil { return err }
	if err := routing.ValidateCustomReferences(c.Proxy.CustomExits, c.Proxy.DefaultExitID, c.Exit.UpstreamExitID, c.Routing.Rules); err != nil { return err }
	for _, item := range c.Proxy.CustomExits {
		host, port, _ := net.SplitHostPort(item.Address)
		n, _ := strconv.Atoi(port)
		if isLoopbackHost(host) {
			if (c.Proxy.SOCKS5.Enabled == nil || *c.Proxy.SOCKS5.Enabled) && n == c.Proxy.SOCKS5.Port && localListenerCoversLoopback(c.Proxy.SOCKS5.Listen) {
				return fmt.Errorf("custom exit %q points to the Agent SOCKS5 listener", item.ID)
			}
			if (c.Proxy.HTTP.Enabled == nil || *c.Proxy.HTTP.Enabled) && n == c.Proxy.HTTP.Port && localListenerCoversLoopback(c.Proxy.HTTP.Listen) {
				return fmt.Errorf("custom exit %q points to the Agent HTTP listener", item.ID)
			}
		}
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
	out.Proxy.CustomExits = routing.CloneCustomExits(c.Proxy.CustomExits)
	out.Server.TLSEnabled = cloneBool(c.Server.TLSEnabled)
	out.Proxy.HTTP.Enabled = cloneBool(c.Proxy.HTTP.Enabled)
	out.Exit.Enabled = cloneBool(c.Exit.Enabled)
	out.RDP.Enabled = cloneBool(c.RDP.Enabled)
	out.P2P.Enabled = cloneBool(c.P2P.Enabled)
	out.P2P.Fallback = cloneBool(c.P2P.Fallback)
	out.GUI.Enabled = cloneBool(c.GUI.Enabled)
	out.GUI.MinimizeToTray = cloneBool(c.GUI.MinimizeToTray)
	if c.GUI.VerificationPopupTimeoutSec != nil {
		value := *c.GUI.VerificationPopupTimeoutSec
		out.GUI.VerificationPopupTimeoutSec = &value
	}
	out.Web.Enabled = cloneBool(c.Web.Enabled)
	out.Exit.Access.Domains = slices.Clone(c.Exit.Access.Domains)
	out.Exit.Access.CIDRs = slices.Clone(c.Exit.Access.CIDRs)
	out.Routing = routing.CloneConfig(c.Routing)
	out.Network.ExcludeProcesses = slices.Clone(c.Network.ExcludeProcesses)
	return &out
}

func localListenerCoversLoopback(host string) bool {
	host = strings.TrimSpace(host)
	return host == "0.0.0.0" || host == "::" || isLoopbackHost(host)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	return err == nil && addr.IsLoopback()
}

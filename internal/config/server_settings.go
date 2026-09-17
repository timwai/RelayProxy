package config

import (
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

var ErrConfigConflict = errors.New("配置已被其他操作修改，请重新读取后再保存")

func NormalizeServerConfig(c *ServerConfig) error {
	if c == nil {
		return errors.New("server config cannot be nil")
	}
	applyServerDefaults(c)
	c.Server.CertFile = strings.TrimSpace(c.Server.CertFile)
	c.Server.KeyFile = strings.TrimSpace(c.Server.KeyFile)
	for _, item := range []struct {
		name string
		addr *string
	}{{"server.admin.listen", &c.Server.Admin.Listen}, {"server.tls.listen", &c.Server.TLS.Listen}, {"server.quic.listen", &c.Server.QUIC.Listen}} {
		*item.addr = strings.TrimSpace(*item.addr)
		if err := validateListen(*item.addr); err != nil {
			return fmt.Errorf("%s: %w", item.name, err)
		}
	}
	c.RDP.RendezvousListen = strings.TrimSpace(c.RDP.RendezvousListen)
	if c.RDP.RendezvousListen != "" {
		if err := validateListen(c.RDP.RendezvousListen); err != nil {
			return fmt.Errorf("rdp.rendezvous_listen: %w", err)
		}
	}
	c.RDP.RendezvousAdvertise = strings.TrimSpace(c.RDP.RendezvousAdvertise)
	if c.RDP.RendezvousAdvertise != "" {
		if err := validateRendezvousAdvertise(c.RDP.RendezvousAdvertise); err != nil {
			return fmt.Errorf("rdp.rendezvous_advertise: %w", err)
		}
	}
	c.RDP.Ingress.Listen = strings.TrimSpace(c.RDP.Ingress.Listen)
	for i, raw := range c.RDP.Ingress.SourceCIDRs {
		c.RDP.Ingress.SourceCIDRs[i] = strings.TrimSpace(raw)
	}
	filteredCIDRs := c.RDP.Ingress.SourceCIDRs[:0]
	for _, raw := range c.RDP.Ingress.SourceCIDRs {
		if raw != "" {
			filteredCIDRs = append(filteredCIDRs, raw)
		}
	}
	c.RDP.Ingress.SourceCIDRs = filteredCIDRs
	if len(c.RDP.Ingress.SourceCIDRs) == 0 {
		c.RDP.Ingress.SourceCIDRs = nil
	}
	if c.RDP.Ingress.Listen != "" {
		if err := validateListen(c.RDP.Ingress.Listen); err != nil {
			return fmt.Errorf("rdp.ingress.listen: %w", err)
		}
	}
	if c.RDP.LeaseSec < 15 || c.RDP.LeaseSec > 300 {
		return errors.New("rdp.lease_sec 必须在 15-300 秒之间")
	}
	if c.RDP.Ingress.PortStart < 1 || c.RDP.Ingress.PortStart > 65535 || c.RDP.Ingress.PortEnd < c.RDP.Ingress.PortStart || c.RDP.Ingress.PortEnd > 65535 {
		return errors.New("rdp.ingress 的端口范围无效")
	}
	if c.RDP.Ingress.RateLimitPerMin < 1 || c.RDP.Ingress.RateLimitPerMin > 100000 {
		return errors.New("rdp.ingress.rate_limit_per_minute 必须在 1-100000 之间")
	}
	for _, raw := range c.RDP.Ingress.SourceCIDRs {
		if _, _, err := net.ParseCIDR(raw); err != nil {
			return fmt.Errorf("rdp.ingress.source_cidrs 包含无效 CIDR %q", raw)
		}
	}
	if listenAddressesOverlap(c.Server.Admin.Listen, c.Server.TLS.Listen) {
		return errors.New("管理页面和 TCP 隧道的监听地址与端口冲突，请使用不同端口")
	}
	if c.NeedsCertificate() && (c.Server.CertFile == "") != (c.Server.KeyFile == "") {
		return errors.New("server.cert_file and server.key_file must be configured together")
	}
	if c.Database.Driver != "sqlite" && c.Database.Driver != "sqlite3" {
		return errors.New("database.driver 必须是 sqlite 或 sqlite3")
	}
	if c.Tunnel.HeartbeatSec < 1 || c.Tunnel.HeartbeatSec > 3600 {
		return errors.New("tunnel.heartbeat_sec 必须在 1-3600 之间")
	}
	if c.Tunnel.MaxConnections < 1 || c.Tunnel.MaxConnections > 1000000 || c.Tunnel.MaxConnectionsPerDevice < 1 || c.Tunnel.MaxConnectionsPerDevice > 1000000 {
		return errors.New("连接和设备并发流上限必须在 1-1000000 之间")
	}
	if err := validateAccess(c.RelayPolicy()); err != nil {
		return fmt.Errorf("relay_acl: %w", err)
	}
	return nil
}

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.ContainsAny(host, " /\\\t\r\n") {
		return errors.New("请输入主机与端口，如 :21080、0.0.0.0:21080 或 [::]:21080")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("监听端口必须在 0-65535 之间")
	}
	return nil
}

func validateRendezvousAdvertise(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, " /\\\t\r\n") {
		return errors.New("请输入可路由的主机与端口，如 relay.example.com:3478")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("rendezvous 广播端口必须在 1-65535 之间")
	}
	return nil
}

// Detect aliases of the same TCP socket without binding a live port during
// configuration validation. Port zero always requests an independent socket.
func listenAddressesOverlap(a, b string) bool {
	hostA, rawA, errA := net.SplitHostPort(a)
	hostB, rawB, errB := net.SplitHostPort(b)
	portA, _ := strconv.Atoi(rawA)
	portB, _ := strconv.Atoi(rawB)
	if errA != nil || errB != nil || portA == 0 || portA != portB {
		return false
	}
	if strings.EqualFold(hostA, hostB) {
		return true
	}
	// Go's tcp listener on an unspecified IPv6 address normally uses dual stack.
	if hostA == "" || hostB == "" || hostA == "::" || hostB == "::" {
		return true
	}
	ipA, ipB := net.ParseIP(hostA), net.ParseIP(hostB)
	if hostA == "0.0.0.0" {
		return ipB == nil || ipB.To4() != nil
	}
	if hostB == "0.0.0.0" {
		return ipA == nil || ipA.To4() != nil
	}
	if ipA != nil && ipB != nil {
		return ipA.Equal(ipB)
	}
	return (strings.EqualFold(hostA, "localhost") && ipB != nil && ipB.IsLoopback()) ||
		(strings.EqualFold(hostB, "localhost") && ipA != nil && ipA.IsLoopback())
}

func CloneServerConfig(c *ServerConfig) *ServerConfig {
	if c == nil {
		return nil
	}
	out := *c
	if c.Server.TLSEnabled != nil {
		out.Server.TLSEnabled = BoolPtr(*c.Server.TLSEnabled)
	}
	if c.Server.Admin.TLSEnabled != nil {
		out.Server.Admin.TLSEnabled = BoolPtr(*c.Server.Admin.TLSEnabled)
	}
	if c.RDP.Ingress.Enabled != nil {
		out.RDP.Ingress.Enabled = BoolPtr(*c.RDP.Ingress.Enabled)
	}
	out.RDP.Ingress.SourceCIDRs = slices.Clone(c.RDP.Ingress.SourceCIDRs)
	if c.RelayACL != nil {
		policy := *c.RelayACL
		if policy.AllowInternet != nil {
			policy.AllowInternet = BoolPtr(*policy.AllowInternet)
		}
		policy.Access.Domains = slices.Clone(policy.Access.Domains)
		policy.Access.CIDRs = slices.Clone(policy.Access.CIDRs)
		out.RelayACL = &policy
	}
	return &out
}

func SaveServerConfig(path string, c *ServerConfig) error {
	prepared := CloneServerConfig(c)
	if err := NormalizeServerConfig(prepared); err != nil {
		return err
	}
	data, err := yaml.Marshal(prepared)
	if err != nil {
		return err
	}
	return atomicWriteConfig(path, data)
}

// ServerSettings owns persistence, while the active runtime snapshot remains
// the one that started the listeners. Saving never claims that a listener or
// target policy was replaced without a service restart.
type ServerSettings struct {
	mu      sync.Mutex
	path    string
	running *ServerConfig
	write   func(string, *ServerConfig) error
}

type ServerSettingsState struct {
	Config        *ServerConfig
	Runtime       *ServerConfig
	Revision      string
	RestartFields []string
	Path          string
}

func NewServerSettings(path string, running *ServerConfig) (*ServerSettings, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	active := CloneServerConfig(running)
	if err := NormalizeServerConfig(active); err != nil {
		return nil, err
	}
	return &ServerSettings{path: absolute, running: active, write: SaveServerConfig}, nil
}

func (s *ServerSettings) State() (*ServerSettingsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, revision, err := s.read()
	if err != nil {
		return nil, err
	}
	return s.state(cfg, revision), nil
}

func (s *ServerSettings) Update(revision string, update func(*ServerConfig) error) (*ServerSettingsState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, current, err := s.read()
	if err != nil {
		return nil, err
	}
	if revision == "" || revision != current {
		return nil, ErrConfigConflict
	}
	if err := update(cfg); err != nil {
		return nil, err
	}
	if err := NormalizeServerConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.NeedsCertificate() && cfg.Server.CertFile != "" {
		if _, err := tls.LoadX509KeyPair(cfg.Server.CertFile, cfg.Server.KeyFile); err != nil {
			return nil, fmt.Errorf("证书或私钥无法加载，配置未保存: %w", err)
		}
	}
	// Catch external edits made during validation, in addition to stale pages.
	if _, latest, err := s.read(); err != nil {
		return nil, err
	} else if latest != current {
		return nil, ErrConfigConflict
	}
	if err := s.write(s.path, cfg); err != nil {
		return nil, fmt.Errorf("写入配置失败: %w", err)
	}
	saved, savedRevision, err := s.read()
	if err != nil {
		return nil, err
	}
	return s.state(saved, savedRevision), nil
}

func (s *ServerSettings) read() (*ServerConfig, string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, "", err
	}
	cfg, err := parseServerConfig(data)
	if err != nil {
		return nil, "", err
	}
	return cfg, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (s *ServerSettings) state(cfg *ServerConfig, revision string) *ServerSettingsState {
	return &ServerSettingsState{Config: CloneServerConfig(cfg), Runtime: CloneServerConfig(s.running), Revision: revision,
		RestartFields: serverRestartFields(cfg, s.running), Path: s.path}
}

func serverRestartFields(desired, active *ServerConfig) []string {
	values := func(c *ServerConfig) map[string]any {
		policy := c.RelayPolicy()
		policy.AccessHosts = append([]string{}, policy.AccessHosts...)
		policy.AccessCIDRs = append([]string{}, policy.AccessCIDRs...)
		return map[string]any{
			"server.admin.listen": c.Server.Admin.Listen, "server.admin.tls_enabled": c.IsAdminTLSEnabled(),
			"server.tls_enabled": c.IsTLSEnabled(), "server.tls.listen": c.Server.TLS.Listen, "server.quic.listen": c.Server.QUIC.Listen,
			"server.cert_file": c.Server.CertFile, "server.key_file": c.Server.KeyFile,
			"tunnel.heartbeat_sec": c.Tunnel.HeartbeatSec, "tunnel.max_connections": c.Tunnel.MaxConnections,
			"tunnel.max_connections_per_device": c.Tunnel.MaxConnectionsPerDevice, "relay_acl": policy,
			"rdp":      c.RDP,
			"database": c.Database,
		}
	}
	want, running := values(desired), values(active)
	fields := []string{}
	for key, value := range want {
		if !reflect.DeepEqual(value, running[key]) {
			fields = append(fields, key)
		}
	}
	slices.Sort(fields)
	return fields
}

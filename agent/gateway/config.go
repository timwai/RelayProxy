// Package gateway accepts clientless LAN traffic on Linux using TPROXY.
package gateway

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
)

type Config struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	Interface      string   `yaml:"interface" json:"interface"`
	SourceCIDRs    []string `yaml:"source_cidrs" json:"source_cidrs"`
	ExitID         string   `yaml:"exit_id,omitempty" json:"exit_id,omitempty"`
	Port           int      `yaml:"port,omitempty" json:"port,omitempty"`
	MaxConnections int      `yaml:"max_connections,omitempty" json:"max_connections,omitempty"`
}

func Clone(c Config) Config { c.SourceCIDRs = slices.Clone(c.SourceCIDRs); return c }
func (c Config) ListenPort() int {
	if c.Port == 0 {
		return 15080
	}
	return c.Port
}
func (c Config) Limit() int {
	if c.MaxConnections == 0 {
		return 1024
	}
	return c.MaxConnections
}
func Validate(c Config) error {
	if c.Port < 0 || c.Port > 65535 || (c.Port > 0 && c.Port < 1024) {
		return fmt.Errorf("网关拦截端口必须为 1024–65535")
	}
	if c.MaxConnections < 0 || c.MaxConnections > 65536 {
		return fmt.Errorf("网关连接上限必须为 1–65536")
	}
	if !c.Enabled {
		return nil
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`).MatchString(c.Interface) || c.Interface == "lo" {
		return fmt.Errorf("请指定有效的 LAN 网卡（不能为 lo）")
	}
	if len(c.SourceCIDRs) == 0 || len(c.SourceCIDRs) > 32 {
		return fmt.Errorf("网关必须配置 1–32 个允许接入的 IPv4 网段")
	}
	for _, raw := range c.SourceCIDRs {
		p, e := netip.ParsePrefix(raw)
		if e != nil || !p.Addr().Is4() || p.Bits() == 0 || p.Addr().IsLoopback() || p.Addr().IsMulticast() {
			return fmt.Errorf("无效的网关 IPv4 来源网段 %q", raw)
		}
	}
	return nil
}

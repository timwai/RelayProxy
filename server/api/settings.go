package api

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"relayproxy/internal/config"
	"relayproxy/server/repository"
)

// The API only exposes settings that this process actually consumes. Database
// credentials and certificate contents are never included in an editable DTO.
type AdminSettings struct {
	Listen     string `json:"listen"`
	TLSEnabled bool   `json:"tlsEnabled"`
}

type TunnelSettings struct {
	TCPListen           string `json:"tcpListen"`
	QUICListen          string `json:"quicListen"`
	TLSEnabled          bool   `json:"tlsEnabled"`
	HeartbeatSec        int    `json:"heartbeatSec"`
	MaxConnections      int    `json:"maxConnections"`
	MaxStreamsPerDevice int    `json:"maxStreamsPerDevice"`
}

type CertificateSettings struct {
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
}

type RelayACLSettings struct {
	AllowInternet       bool     `json:"allowInternet"`
	AllowPrivateNetwork bool     `json:"allowPrivateNetwork"`
	AllowLoopback       bool     `json:"allowLoopback"`
	AccessMode          string   `json:"accessMode"`
	Domains             []string `json:"domains"`
	CIDRs               []string `json:"cidrs"`
}

type RDPIngressSettings struct {
	Enabled         bool     `json:"enabled"`
	Listen          string   `json:"listen"`
	PortStart       int      `json:"portStart"`
	PortEnd         int      `json:"portEnd"`
	SourceCIDRs     []string `json:"sourceCidrs"`
	RateLimitPerMin int      `json:"rateLimitPerMinute"`
}

type ServerEditableConfig struct {
	Admin       AdminSettings       `json:"admin"`
	Tunnel      TunnelSettings      `json:"tunnel"`
	Certificate CertificateSettings `json:"certificate"`
	RelayACL    RelayACLSettings    `json:"relayACL"`
	RDPIngress  RDPIngressSettings  `json:"rdpIngress"`
}

type CertificateInfo struct {
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	DNSNames  []string  `json:"dnsNames"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	SHA256    string    `json:"sha256"`
}

type ServerInfo struct {
	StartedAt   time.Time        `json:"startedAt"`
	Certificate *CertificateInfo `json:"certificate"`
}

type ServerSettingsResponse struct {
	Config          ServerEditableConfig `json:"config"`
	Runtime         ServerEditableConfig `json:"runtime"`
	Revision        string               `json:"revision"`
	RestartRequired bool                 `json:"restartRequired"`
	RestartFields   []string             `json:"restartFields"`
	ConfigPath      string               `json:"configPath"`
	Info            ServerInfo           `json:"info"`
}

func WithServerSettings(settings *config.ServerSettings, activeTLS *tls.Config) RouterOption {
	info := ServerInfo{StartedAt: time.Now().UTC()}
	if activeTLS != nil && len(activeTLS.Certificates) > 0 && len(activeTLS.Certificates[0].Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(activeTLS.Certificates[0].Certificate[0]); err == nil {
			info.Certificate = &CertificateInfo{Subject: leaf.Subject.String(), Issuer: leaf.Issuer.String(),
				DNSNames: append([]string{}, leaf.DNSNames...), NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
				SHA256: fmt.Sprintf("%x", sha256.Sum256(leaf.Raw))}
		}
	}
	return func(r *Router) { r.settings, r.serverInfo = settings, info }
}

func (r *Router) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		user, _ := req.Context().Value(principalContextKey).(*repository.User)
		if user == nil || user.Role != "admin" {
			writeError(w, http.StatusForbidden, "只有管理员可以查看和修改服务端配置")
			return
		}
		next(w, req)
	}
}

func serverEditableConfig(c *config.ServerConfig) ServerEditableConfig {
	p := c.RelayPolicy()
	ingressEnabled := c.RDP.Ingress.Enabled != nil && *c.RDP.Ingress.Enabled
	return ServerEditableConfig{
		Admin: AdminSettings{c.Server.Admin.Listen, c.IsAdminTLSEnabled()},
		Tunnel: TunnelSettings{c.Server.TLS.Listen, c.Server.QUIC.Listen, c.IsTLSEnabled(),
			c.Tunnel.HeartbeatSec, c.Tunnel.MaxConnections, c.Tunnel.MaxConnectionsPerDevice},
		Certificate: CertificateSettings{c.Server.CertFile, c.Server.KeyFile},
		RelayACL: RelayACLSettings{p.AllowInternet, p.AllowPrivateNetwork, p.AllowLoopback,
			string(p.AccessMode), append([]string{}, p.AccessHosts...), append([]string{}, p.AccessCIDRs...)},
		RDPIngress: RDPIngressSettings{ingressEnabled, c.RDP.Ingress.Listen, c.RDP.Ingress.PortStart,
			c.RDP.Ingress.PortEnd, append([]string{}, c.RDP.Ingress.SourceCIDRs...), c.RDP.Ingress.RateLimitPerMin},
	}
}

func (c ServerEditableConfig) apply(target *config.ServerConfig) error {
	if strings.TrimSpace(c.Admin.Listen) == "" || strings.TrimSpace(c.Tunnel.TCPListen) == "" || strings.TrimSpace(c.Tunnel.QUICListen) == "" || strings.TrimSpace(c.RDPIngress.Listen) == "" {
		return errors.New("管理、TCP、QUIC 和 RDP 公网入口监听地址不能为空")
	}
	if c.Tunnel.HeartbeatSec < 1 || c.Tunnel.HeartbeatSec > 3600 {
		return errors.New("心跳间隔必须在 1-3600 秒之间")
	}
	if c.Tunnel.MaxConnections < 1 || c.Tunnel.MaxConnections > 1000000 || c.Tunnel.MaxStreamsPerDevice < 1 || c.Tunnel.MaxStreamsPerDevice > 1000000 {
		return errors.New("连接与并发流上限必须在 1-1000000 之间")
	}
	target.Server.Admin.Listen = c.Admin.Listen
	target.Server.Admin.TLSEnabled = config.BoolPtr(c.Admin.TLSEnabled)
	target.Server.TLS.Listen, target.Server.QUIC.Listen = c.Tunnel.TCPListen, c.Tunnel.QUICListen
	target.Server.TLSEnabled = config.BoolPtr(c.Tunnel.TLSEnabled)
	target.Server.CertFile, target.Server.KeyFile = c.Certificate.CertFile, c.Certificate.KeyFile
	target.Tunnel.HeartbeatSec = c.Tunnel.HeartbeatSec
	target.Tunnel.MaxConnections = c.Tunnel.MaxConnections
	target.Tunnel.MaxConnectionsPerDevice = c.Tunnel.MaxStreamsPerDevice
	target.RelayACL = &config.RelayACLConfig{
		AllowInternet: config.BoolPtr(c.RelayACL.AllowInternet), AllowPrivateNetwork: c.RelayACL.AllowPrivateNetwork,
		AllowLoopback: c.RelayACL.AllowLoopback,
		Access: config.AccessConfig{Mode: strings.ToLower(strings.TrimSpace(c.RelayACL.AccessMode)),
			Domains: cleanSettingLines(c.RelayACL.Domains), CIDRs: cleanSettingLines(c.RelayACL.CIDRs)},
	}
	target.RDP.Ingress.Enabled = config.BoolPtr(c.RDPIngress.Enabled)
	target.RDP.Ingress.Listen = strings.TrimSpace(c.RDPIngress.Listen)
	target.RDP.Ingress.PortStart = c.RDPIngress.PortStart
	target.RDP.Ingress.PortEnd = c.RDPIngress.PortEnd
	target.RDP.Ingress.SourceCIDRs = cleanSettingLines(c.RDPIngress.SourceCIDRs)
	target.RDP.Ingress.RateLimitPerMin = c.RDPIngress.RateLimitPerMin
	return nil
}

func cleanSettingLines(lines []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !seen[line] {
			result = append(result, line)
			seen[line] = true
		}
	}
	return result
}

func (r *Router) settingsResponse(state *config.ServerSettingsState) ServerSettingsResponse {
	return ServerSettingsResponse{Config: serverEditableConfig(state.Config), Runtime: serverEditableConfig(state.Runtime),
		Revision: state.Revision, RestartRequired: len(state.RestartFields) > 0, RestartFields: state.RestartFields,
		ConfigPath: state.Path, Info: r.serverInfo}
}

func (r *Router) handleGetServerConfig(w http.ResponseWriter, req *http.Request) {
	if r.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "当前服务未启用配置管理")
		return
	}
	state, err := r.settings.State()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取配置失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, r.settingsResponse(state))
}

func (r *Router) handleSaveServerConfig(w http.ResponseWriter, req *http.Request) {
	if r.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "当前服务未启用配置管理")
		return
	}
	var body struct {
		Revision string                `json:"revision"`
		Config   *ServerEditableConfig `json:"config"`
	}
	if err := decodeJSON(w, req, &body); err != nil || body.Config == nil || body.Revision == "" {
		writeError(w, http.StatusBadRequest, "请提交完整配置和版本号（JSON），并删除未知字段")
		return
	}
	state, err := r.settings.Update(body.Revision, body.Config.apply)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, config.ErrConfigConflict) {
			code = http.StatusConflict
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, r.settingsResponse(state))
}

func decodeJSON(w http.ResponseWriter, req *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

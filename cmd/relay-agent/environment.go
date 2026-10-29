package main

import (
	"fmt"
	"strconv"
	"strings"

	"relayproxy/internal/config"
)

// Explicit CLI paths take priority over the container's configuration directory.
func resolveStartupConfigPath(file, directory string, fileExplicit, dirExplicit bool, getenv func(string) string) (string, error) {
	if !fileExplicit && !dirExplicit {
		if value := strings.TrimSpace(getenv("RELAY_AGENT_CONFIG_DIR")); value != "" {
			directory, dirExplicit = value, true
		}
	}
	return resolveConfigPath(file, directory, fileExplicit, dirExplicit)
}

func loadStartupAgentConfig(path string, getenv func(string) string) (*config.AgentConfigFile, error) {
	cfg, err := loadOrCreateAgentConfig(path)
	if err != nil {
		return nil, err
	}
	next, err := agentConfigFromEnvironment(cfg, getenv)
	if err != nil {
		return nil, err
	}
	if config.AgentConfigRevision(cfg) != config.AgentConfigRevision(next) {
		// Persist through the normal atomic writer so Web management reads
		// the same desired settings and keeps unrelated policy/identity data.
		if err := config.SaveAgentConfig(path, next); err != nil {
			return nil, fmt.Errorf("save environment configuration: %w", err)
		}
	}
	return next, nil
}

// Blank variables inherit YAML settings. In particular, Compose's empty
// optional variables must never erase a previously paired device or policy.
func agentConfigFromEnvironment(cfg *config.AgentConfigFile, getenv func(string) string) (*config.AgentConfigFile, error) {
	next := config.CloneAgentConfig(cfg)
	for _, item := range []struct {
		name string
		dest *string
	}{
		{"RELAY_AGENT_SERVER", &next.Server.Address},
		{"RELAY_AGENT_DEVICE_NAME", &next.Device.Name},
		{"RELAY_AGENT_IDENTITY_ID", &next.Device.IdentityID},
		{"RELAY_AGENT_TRANSPORT_MODE", &next.Transport.Mode},
		{"RELAY_AGENT_DEFAULT_EXIT", &next.Proxy.DefaultExitID},
		{"RELAY_AGENT_SOCKS5_LISTEN", &next.Proxy.SOCKS5.Listen},
		{"RELAY_AGENT_HTTP_LISTEN", &next.Proxy.HTTP.Listen},
		{"RELAY_AGENT_WEB_LISTEN", &next.Web.Listen},
		{"RELAY_AGENT_WEB_TOKEN", &next.Web.Token},
	} {
		if value := strings.TrimSpace(getenv(item.name)); value != "" {
			*item.dest = value
		}
	}
	for _, item := range []struct {
		name string
		dest *int
	}{
		{"RELAY_AGENT_SERVER_TCP_PORT", &next.Server.TCPPort},
		{"RELAY_AGENT_SERVER_QUIC_PORT", &next.Server.QUICPort},
		{"RELAY_AGENT_SOCKS5_PORT", &next.Proxy.SOCKS5.Port},
		{"RELAY_AGENT_HTTP_PORT", &next.Proxy.HTTP.Port},
		{"RELAY_AGENT_WEB_PORT", &next.Web.Port},
	} {
		if value := strings.TrimSpace(getenv(item.name)); value != "" {
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("%s must be an integer between 1 and 65535", item.name)
			}
			*item.dest = port
		}
	}
	for _, item := range []struct {
		name string
		set  func(bool)
	}{
		{"RELAY_AGENT_SERVER_TLS_ENABLED", func(v bool) { next.Server.TLSEnabled = config.BoolPtr(v) }},
		{"RELAY_AGENT_INSECURE_TLS", func(v bool) { next.Server.InsecureTLS = v }},
		{"RELAY_AGENT_SOCKS5_ENABLED", func(v bool) { next.Proxy.SOCKS5.Enabled = config.BoolPtr(v) }},
		{"RELAY_AGENT_HTTP_ENABLED", func(v bool) { next.Proxy.HTTP.Enabled = config.BoolPtr(v) }},
		{"RELAY_AGENT_WEB_ENABLED", func(v bool) { next.Web.Enabled = config.BoolPtr(v) }},
		{"RELAY_AGENT_TRANSPARENT_PROXY", func(v bool) {
			next.Network.Mode = ""
			if v {
				next.Network.Mode = "divert"
			}
		}},
	} {
		if value := strings.TrimSpace(getenv(item.name)); value != "" {
			enabled, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("%s must be true or false (or 1 / 0)", item.name)
			}
			item.set(enabled)
		}
	}
	if err := config.NormalizeAgentConfig(next); err != nil {
		return nil, fmt.Errorf("invalid environment configuration: %w", err)
	}
	return next, nil
}

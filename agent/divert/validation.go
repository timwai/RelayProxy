package divert

import (
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ValidateConfig validates all rules even when divert or a rule is disabled.
// It never mutates the caller's configuration or the running rule engine.
func ValidateConfig(cfg Config) error {
	_, err := validatedConfig(cfg)
	return err
}

func cloneConfig(cfg Config) Config {
	cfg.ExcludeProcesses = slices.Clone(cfg.ExcludeProcesses)
	cfg.Rules = slices.Clone(cfg.Rules)
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		r.Hosts = slices.Clone(r.Hosts)
		r.CIDRs = slices.Clone(r.CIDRs)
		r.Ports = slices.Clone(r.Ports)
		r.Protocols = slices.Clone(r.Protocols)
	}
	return cfg
}

func validatedConfig(cfg Config) (Config, error) {
	cfg = cloneConfig(cfg)
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode != "" && cfg.Mode != "divert" {
		return Config{}, fmt.Errorf("network.mode: unsupported value %q (use divert or empty)", cfg.Mode)
	}
	var err error
	if cfg.DefaultAction, err = normalizeAction(cfg.DefaultAction, ActionProxy); err != nil {
		return Config{}, fmt.Errorf("network.default_action: %w", err)
	}
	for i, pattern := range cfg.ExcludeProcesses {
		if err := validateProcessPattern(pattern, false); err != nil {
			return Config{}, fmt.Errorf("network.exclude_processes[%d]: %w", i, err)
		}
	}
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		prefix := fmt.Sprintf("network.rules[%d] (%q)", i, r.Name)
		if err := validateProcessPattern(r.Process, true); err != nil {
			return Config{}, fmt.Errorf("%s.process: %w", prefix, err)
		}
		if r.Action, err = normalizeAction(r.Action, cfg.DefaultAction); err != nil {
			return Config{}, fmt.Errorf("%s.action: %w", prefix, err)
		}
		for j, proto := range r.Protocols {
			proto = strings.ToLower(strings.TrimSpace(proto))
			if proto != string(ProtoTCP) && proto != string(ProtoUDP) {
				return Config{}, fmt.Errorf("%s.protocols[%d]: unsupported protocol %q", prefix, j, proto)
			}
			r.Protocols[j] = proto
		}
		for j, host := range r.Hosts {
			host = strings.ToLower(strings.TrimSpace(host))
			if host == "" || strings.ContainsAny(host, "/\\ \t\r\n\x00") {
				return Config{}, fmt.Errorf("%s.hosts[%d]: invalid host pattern %q", prefix, j, host)
			}
			if _, err := netip.ParseAddr(host); err != nil {
				if strings.Contains(host, ":") {
					return Config{}, fmt.Errorf("%s.hosts[%d]: use a hostname or IP without a port", prefix, j)
				}
				if _, err := path.Match(host, ""); err != nil {
					return Config{}, fmt.Errorf("%s.hosts[%d]: %w", prefix, j, err)
				}
			}
			r.Hosts[j] = host
		}
		for j, raw := range r.CIDRs {
			value := strings.TrimSpace(raw)
			_, prefixErr := netip.ParsePrefix(value)
			addr, addrErr := netip.ParseAddr(value)
			if prefixErr != nil && (addrErr != nil || addr.Zone() != "") {
				return Config{}, fmt.Errorf("%s.cidrs[%d]: invalid CIDR or IP %q", prefix, j, raw)
			}
		}
		for j, raw := range r.Ports {
			lo, hi, isRange := strings.Cut(strings.TrimSpace(raw), "-")
			if !isRange {
				hi = lo
			}
			a, err1 := validPort(lo)
			b, err2 := validPort(hi)
			if err1 != nil || err2 != nil || a > b {
				return Config{}, fmt.Errorf("%s.ports[%d]: invalid port/range %q (use 1..65535)", prefix, j, raw)
			}
		}
	}
	return cfg, nil
}

func normalizeAction(action, fallback Action) (Action, error) {
	action = Action(strings.ToUpper(strings.TrimSpace(string(action))))
	if action == "" {
		return fallback, nil
	}
	switch action {
	case ActionProxy, ActionDirect, ActionReject:
		return action, nil
	default:
		return "", fmt.Errorf("unsupported action %q", action)
	}
}

func validateProcessPattern(pattern string, allowEmpty bool) error {
	pattern = strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/")
	if (!allowEmpty && pattern == "") || strings.ContainsAny(pattern, "\x00\r\n") {
		return fmt.Errorf("invalid process pattern %q", pattern)
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("invalid process pattern %q: %w", pattern, err)
	}
	return nil
}

func validPort(value string) (uint16, error) {
	value = strings.TrimSpace(value)
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid port %q", value)
		}
	}
	n, err := strconv.ParseUint(value, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	return uint16(n), nil
}

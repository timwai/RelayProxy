package gui

import "relayproxy/agent/routing"

// routingConfigView is the single transport-independent shape read by the
// React desktop and local Web GUIs. Keep every editable field here: if a
// frontend silently receives a default instead, a later save of the full
// routing object can overwrite the user's persisted DNS policy.
func routingConfigView(cfg routing.Config) map[string]any {
	rules := cfg.Rules
	if rules == nil {
		rules = []routing.Rule{}
	}
	blockedIPs := cfg.DoHBlockedIPs
	if blockedIPs == nil {
		blockedIPs = []string{}
	}
	dnsMode := cfg.DNSMode
	if dnsMode == "" {
		dnsMode = routing.DNSModeProxy
	}
	return map[string]any{
		"mode":               cfg.Mode,
		"dns_mode":           dnsMode,
		"auto_detect_dns":    cfg.AutoDetectDNS,
		"fake_ip_enabled":    cfg.FakeIPEnabled,
		"block_doh_endpoints": cfg.BlockDoHEndpoints,
		"forward_other_dns":  cfg.ForwardOtherDNS,
		"dns_exit_id":        cfg.DNSExitID,
		"doh_blocked_ips":    blockedIPs,
		"default_action":     cfg.DefaultAction,
		"rules":              rules,
	}
}

package routing

import "strings"

// matchDNSRuleDomain uses DNS label boundaries, never a bare suffix match.
// Plain names match exactly; a leading '*.' includes the apex and subdomains.
func matchDNSRuleDomain(host, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[2:]
		return host == suffix || strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

// DNSRuleMatchesDomain reports whether one configured DNS pattern matches a
// concrete hostname. It is shared by rule diagnostics and the active engine.
func DNSRuleMatchesDomain(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host != "" && matchDNSRuleDomain(host, pattern)
}

func validDNSRuleDomain(pattern string) bool {
	if strings.HasPrefix(pattern, "*.") {
		pattern = pattern[2:]
	}
	return validDNSDirectDomain(pattern)
}

// DNSRuleExitIDs allows exit/group authorization checks to include DNS-only
// references as well as ordinary application routing references.
func DNSRuleExitIDs(rules []DNSRule) []string {
	var ids []string
	for _, rule := range rules {
		if rule.Action == DNSActionRemote && rule.ExitID != "" {
			ids = append(ids, rule.ExitID)
		}
	}
	return ids
}

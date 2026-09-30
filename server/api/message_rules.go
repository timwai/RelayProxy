package api

import (
	"fmt"
	"regexp"
	"strings"

	"relayproxy/internal/messageutil"
	"relayproxy/server/repository"
)

func normalizeVerificationRules(rules []repository.VerificationRule) ([]repository.VerificationRule, error) {
	if len(rules) > 32 {
		return nil, fmt.Errorf("too many verification rules")
	}
	out := make([]repository.VerificationRule, 0, len(rules))
	for _, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		keywords := make([]string, 0, len(rule.Keywords))
		seen := make(map[string]bool, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" || seen[keyword] {
				continue
			}
			seen[keyword] = true
			keywords = append(keywords, keyword)
		}
		rule.Keywords = keywords
		if err := messageutil.ValidateVerificationRule(messageutil.VerificationRule{
			Name: rule.Name, Keywords: rule.Keywords, Pattern: rule.Pattern,
			MaxDistance: rule.MaxDistance, CaseSensitive: rule.CaseSensitive,
		}); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func messageutilVerificationRules(rules []repository.VerificationRule) []messageutil.VerificationRule {
	out := make([]messageutil.VerificationRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, messageutil.VerificationRule{
			Name: rule.Name, Keywords: rule.Keywords, Pattern: rule.Pattern,
			MaxDistance: rule.MaxDistance, CaseSensitive: rule.CaseSensitive,
		})
	}
	return out
}

func normalizeRouteRules(rules []repository.MessageRouteRule, knownDevices map[string]bool) ([]repository.MessageRouteRule, error) {
	if len(rules) > 64 {
		return nil, fmt.Errorf("too many routing rules")
	}
	out := make([]repository.MessageRouteRule, 0, len(rules))
	for _, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.MatchType = strings.ToLower(strings.TrimSpace(rule.MatchType))
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		rule.DeviceIDs = normalizeChannelDeviceIDs(rule.DeviceIDs)
		if rule.Name == "" || len(rule.Name) > 120 {
			return nil, fmt.Errorf("routing rule name must contain 1-120 characters")
		}
		if rule.MatchType != "contains" && rule.MatchType != "regex" {
			return nil, fmt.Errorf("routing rule %q matchType must be contains or regex", rule.Name)
		}
		if rule.Pattern == "" || len(rule.Pattern) > 500 {
			return nil, fmt.Errorf("routing rule %q pattern must contain 1-500 characters", rule.Name)
		}
		if rule.MatchType == "regex" {
			pattern := rule.Pattern
			if !rule.CaseSensitive {
				pattern = "(?i:" + pattern + ")"
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf("invalid routing regex in %q: %w", rule.Name, err)
			}
		}
		if !rule.AllDevices && len(rule.DeviceIDs) == 0 {
			return nil, fmt.Errorf("routing rule %q requires at least one device or allDevices", rule.Name)
		}
		for _, deviceID := range rule.DeviceIDs {
			if !knownDevices[deviceID] {
				return nil, fmt.Errorf("unknown device in routing rule %q: %s", rule.Name, deviceID)
			}
		}
		if rule.AllDevices {
			rule.DeviceIDs = nil
		}
		out = append(out, rule)
	}
	return out, nil
}

func matchRouteRule(content string, rules []repository.MessageRouteRule) (*repository.MessageRouteRule, error) {
	for i := range rules {
		rule := &rules[i]
		matched, err := routeRuleMatches(content, *rule)
		if err != nil {
			return nil, err
		}
		if matched {
			return rule, nil
		}
	}
	return nil, nil
}

func routeRuleMatches(content string, rule repository.MessageRouteRule) (bool, error) {
	switch rule.MatchType {
	case "contains":
		if rule.CaseSensitive {
			return strings.Contains(content, rule.Pattern), nil
		}
		return strings.Contains(strings.ToLower(content), strings.ToLower(rule.Pattern)), nil
	case "regex":
		pattern := rule.Pattern
		if !rule.CaseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return false, err
		}
		return compiled.MatchString(content), nil
	default:
		return false, fmt.Errorf("unsupported routing matchType %q", rule.MatchType)
	}
}

package api

import (
	"fmt"
	"strings"

	"relayproxy/internal/messageutil"
	"relayproxy/server/repository"
)

func normalizeMessageRules(rules []repository.MessageRule) ([]repository.MessageRule, error) {
	if len(rules) > 64 {
		return nil, fmt.Errorf("too many message rules")
	}
	out := make([]repository.MessageRule, 0, len(rules))
	var defaultRule *repository.MessageRule
	for _, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.Type = strings.ToLower(strings.TrimSpace(rule.Type))
		rule.Match.MatchType = strings.ToLower(strings.TrimSpace(rule.Match.MatchType))
		rule.Match.KeywordMode = strings.ToLower(strings.TrimSpace(rule.Match.KeywordMode))
		rule.Match.Pattern = strings.TrimSpace(rule.Match.Pattern)
		if rule.Match.KeywordMode == "" {
			rule.Match.KeywordMode = messageutil.KeywordModeAny
		}
		keywords := make([]string, 0, len(rule.Match.Keywords))
		seen := make(map[string]bool, len(rule.Match.Keywords))
		for _, keyword := range rule.Match.Keywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" || seen[keyword] {
				continue
			}
			seen[keyword] = true
			keywords = append(keywords, keyword)
		}
		rule.Match.Keywords = keywords
		if rule.Popup == nil {
			enabled := true
			rule.Popup = &enabled
		}
		if rule.Default {
			if defaultRule != nil {
				return nil, fmt.Errorf("only one default message rule is allowed")
			}
			if rule.Type != messageutil.MessageTypeVerification {
				return nil, fmt.Errorf("default message rule must be verification_code")
			}
			if rule.Name == "" {
				rule.Name = "默认验证码"
			}
		}
		if rule.Verification != nil {
			rule.Verification.Type = strings.ToLower(strings.TrimSpace(rule.Verification.Type))
			rule.Verification.Pattern = strings.TrimSpace(rule.Verification.Pattern)
			if rule.Verification.Type == messageutil.ExtractorAuto {
				// Treat a completely omitted auto-extractor configuration as the
				// historical smart defaults. Once any tuning field is supplied,
				// invalid combinations (such as allowing neither letters nor
				// digits) are rejected by ValidateMessageRule instead of being
				// silently rewritten.
				omittedAutoConfig := rule.Verification.MinLength == 0 &&
					rule.Verification.MaxLength == 0 &&
					rule.Verification.MaxDistance == 0 &&
					!rule.Verification.AllowLetters &&
					!rule.Verification.AllowDigits &&
					!rule.Verification.RequireDigit
				if omittedAutoConfig {
					rule.Verification.AllowLetters = true
					rule.Verification.AllowDigits = true
					rule.Verification.RequireDigit = true
				}
				if rule.Verification.MinLength == 0 {
					rule.Verification.MinLength = 4
				}
				if rule.Verification.MaxLength == 0 {
					rule.Verification.MaxLength = 8
				}
				if rule.Verification.MaxDistance == 0 {
					rule.Verification.MaxDistance = 64
				}
			}
		}
		if err := messageutil.ValidateMessageRule(toMessageutilRule(rule)); err != nil {
			return nil, err
		}
		if rule.Default {
			copy := rule
			defaultRule = &copy
			continue
		}
		out = append(out, rule)
	}
	if defaultRule != nil {
		out = append(out, *defaultRule)
	}
	return out, nil
}

func toMessageutilRule(rule repository.MessageRule) messageutil.MessageRule {
	out := messageutil.MessageRule{
		Name:    rule.Name,
		Type:    rule.Type,
		Enabled: rule.Enabled,
		Default: rule.Default,
		Match: messageutil.MessageMatch{
			MatchType:     rule.Match.MatchType,
			Keywords:      append([]string(nil), rule.Match.Keywords...),
			KeywordMode:   rule.Match.KeywordMode,
			Pattern:       rule.Match.Pattern,
			CaseSensitive: rule.Match.CaseSensitive,
		},
		Popup: rule.Popup,
	}
	if rule.Verification != nil {
		out.Verification = &messageutil.VerificationExtractor{
			Type:         rule.Verification.Type,
			Pattern:      rule.Verification.Pattern,
			MaxDistance:  rule.Verification.MaxDistance,
			MinLength:    rule.Verification.MinLength,
			MaxLength:    rule.Verification.MaxLength,
			AllowLetters: rule.Verification.AllowLetters,
			AllowDigits:  rule.Verification.AllowDigits,
			RequireDigit: rule.Verification.RequireDigit,
		}
	}
	return out
}

func messageutilMessageRules(rules []repository.MessageRule) []messageutil.MessageRule {
	out := make([]messageutil.MessageRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toMessageutilRule(rule))
	}
	return out
}
